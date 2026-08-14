// Package tg wraps the Telegram Bot API with the forum-topic operations the
// bridge relies on: create a topic per contact, post into it, and read replies.
package tg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// maxDownloadBytes is the Bot API limit for getFile downloads.
const maxDownloadBytes = 20 << 20

// Reply is a message a user typed inside a topic (or in the group's General tab).
type Reply struct {
	// TopicID is the forum topic the message was posted in, 0 for General.
	TopicID int
	// Text is the message body or media caption.
	Text string
	// Command is the leading /command, without the slash, if any.
	Command string
	// Args is whatever followed the command.
	Args string
	// File is the attachment, already downloaded, or nil.
	File *File
	// SenderID is the Telegram user who sent it.
	SenderID int64
	// MessageID identifies the message for reactions/replies.
	MessageID int
}

// File is an attachment downloaded from Telegram.
type File struct {
	Kind     string // photo, video, audio, voice, document, sticker, video_note
	Data     []byte
	MIME     string
	Filename string
	Duration int
}

// Options configures the Telegram side.
type Options struct {
	Token   string
	ChatID  int64
	OwnerID int64 // 0 means "anyone in the group may use the bridge"
}

// Client talks to one Telegram supergroup.
type Client struct {
	bot     *bot.Bot
	opts    Options
	handler func(context.Context, Reply)
	http    *http.Client
}

// ErrTopicMissing means the forum topic no longer exists on Telegram's side.
var ErrTopicMissing = errors.New("tg: topic no longer exists")

// New builds the bot. Nothing is sent until Start is called.
func New(opts Options, handler func(context.Context, Reply)) (*Client, error) {
	c := &Client{
		opts:    opts,
		handler: handler,
		http:    &http.Client{Timeout: 2 * time.Minute},
	}

	b, err := bot.New(opts.Token, bot.WithDefaultHandler(c.onUpdate))
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}
	c.bot = b
	return c, nil
}

// Start begins long-polling and blocks until ctx is cancelled.
func (c *Client) Start(ctx context.Context) { c.bot.Start(ctx) }

// Me returns the bot's own account, useful for a startup sanity check.
func (c *Client) Me(ctx context.Context) (*models.User, error) { return c.bot.GetMe(ctx) }

// VerifyGroup checks that the configured chat is a forum supergroup, which is
// the one setup mistake that would otherwise fail silently on every message.
func (c *Client) VerifyGroup(ctx context.Context) error {
	chat, err := c.bot.GetChat(ctx, &bot.GetChatParams{ChatID: c.opts.ChatID})
	if err != nil {
		return fmt.Errorf("read group %d (is the bot a member?): %w", c.opts.ChatID, err)
	}
	if chat.Type != models.ChatTypeSupergroup {
		return fmt.Errorf("chat %d is a %q, but the bridge needs a supergroup", c.opts.ChatID, chat.Type)
	}
	if !chat.IsForum {
		return fmt.Errorf("group %q does not have Topics enabled (Manage group > Topics)", chat.Title)
	}
	return nil
}

// CreateTopic opens a new forum topic and returns its thread id.
func (c *Client) CreateTopic(ctx context.Context, name string) (int, error) {
	topic, err := c.bot.CreateForumTopic(ctx, &bot.CreateForumTopicParams{
		ChatID: c.opts.ChatID,
		Name:   truncateName(name),
	})
	if err != nil {
		return 0, fmt.Errorf("create topic %q: %w", name, err)
	}
	return topic.MessageThreadID, nil
}

// RenameTopic updates a topic's title, e.g. once a contact's name is known.
func (c *Client) RenameTopic(ctx context.Context, topicID int, name string) error {
	_, err := c.bot.EditForumTopic(ctx, &bot.EditForumTopicParams{
		ChatID:          c.opts.ChatID,
		MessageThreadID: topicID,
		Name:            truncateName(name),
	})
	return wrapTopicErr(err)
}

// SendText posts a text message into a topic.
func (c *Client) SendText(ctx context.Context, topicID int, text string) error {
	for _, chunk := range splitMessage(text) {
		_, err := c.bot.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          c.opts.ChatID,
			MessageThreadID: topicID,
			Text:            chunk,
		})
		if err != nil {
			return wrapTopicErr(err)
		}
	}
	return nil
}

// Attachment is a file the bridge wants to put into a topic.
type Attachment struct {
	Kind     string // image, video, audio, voice, document, sticker
	Data     []byte
	Filename string
	Caption  string
}

// SendAttachment uploads a file into a topic, falling back to a document when
// Telegram rejects the specialised type (e.g. an unsupported sticker format).
func (c *Client) SendAttachment(ctx context.Context, topicID int, a Attachment) error {
	upload := &models.InputFileUpload{Filename: a.Filename, Data: bytes.NewReader(a.Data)}
	caption := truncateCaption(a.Caption)

	var err error
	switch a.Kind {
	case "image":
		_, err = c.bot.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID: c.opts.ChatID, MessageThreadID: topicID, Photo: upload, Caption: caption,
		})
	case "video":
		_, err = c.bot.SendVideo(ctx, &bot.SendVideoParams{
			ChatID: c.opts.ChatID, MessageThreadID: topicID, Video: upload, Caption: caption,
		})
	case "voice":
		_, err = c.bot.SendVoice(ctx, &bot.SendVoiceParams{
			ChatID: c.opts.ChatID, MessageThreadID: topicID, Voice: upload, Caption: caption,
		})
	case "audio":
		_, err = c.bot.SendAudio(ctx, &bot.SendAudioParams{
			ChatID: c.opts.ChatID, MessageThreadID: topicID, Audio: upload, Caption: caption,
		})
	case "sticker":
		_, err = c.bot.SendSticker(ctx, &bot.SendStickerParams{
			ChatID: c.opts.ChatID, MessageThreadID: topicID, Sticker: upload,
		})
	default:
		_, err = c.bot.SendDocument(ctx, &bot.SendDocumentParams{
			ChatID: c.opts.ChatID, MessageThreadID: topicID, Document: upload, Caption: caption,
		})
	}

	if err == nil {
		return nil
	}
	if topicGone(err) {
		return ErrTopicMissing
	}
	if a.Kind == "document" {
		return fmt.Errorf("send document: %w", err)
	}

	// Retry as a generic document: better a downloadable file than a lost message.
	_, retryErr := c.bot.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:          c.opts.ChatID,
		MessageThreadID: topicID,
		Document:        &models.InputFileUpload{Filename: a.Filename, Data: bytes.NewReader(a.Data)},
		Caption:         caption,
	})
	if retryErr != nil {
		return fmt.Errorf("send %s (and document fallback): %w", a.Kind, err)
	}
	return nil
}

// React puts an emoji reaction on a message; used to confirm delivery to WhatsApp.
func (c *Client) React(ctx context.Context, messageID int, emoji string) error {
	_, err := c.bot.SetMessageReaction(ctx, &bot.SetMessageReactionParams{
		ChatID:    c.opts.ChatID,
		MessageID: messageID,
		Reaction:  []models.ReactionType{{Type: models.ReactionTypeTypeEmoji, ReactionTypeEmoji: &models.ReactionTypeEmoji{Emoji: emoji}}},
	})
	return err
}

func (c *Client) onUpdate(ctx context.Context, _ *bot.Bot, update *models.Update) {
	msg := update.Message
	if msg == nil {
		return
	}
	if msg.Chat.ID != c.opts.ChatID {
		return // ignore DMs and other groups entirely
	}
	if msg.From == nil || msg.From.IsBot {
		return
	}
	if c.opts.OwnerID != 0 && msg.From.ID != c.opts.OwnerID {
		return
	}
	// Service messages (topic created/closed, members joining) carry no content.
	if msg.ForumTopicCreated != nil || msg.ForumTopicClosed != nil || msg.ForumTopicReopened != nil {
		return
	}

	reply := Reply{
		TopicID:   msg.MessageThreadID,
		Text:      firstNonEmpty(msg.Text, msg.Caption),
		SenderID:  msg.From.ID,
		MessageID: msg.ID,
	}
	if !msg.IsTopicMessage {
		// A message in the General tab has no thread of its own.
		reply.TopicID = 0
	}
	reply.Command, reply.Args = parseCommand(msg)

	file, err := c.downloadAttachment(ctx, msg)
	if err != nil {
		_ = c.SendText(ctx, reply.TopicID, "⚠️ Could not fetch that attachment: "+err.Error())
		return
	}
	reply.File = file

	if reply.Text == "" && reply.File == nil && reply.Command == "" {
		return
	}
	c.handler(ctx, reply)
}

// downloadAttachment pulls whichever media the message carries into memory.
func (c *Client) downloadAttachment(ctx context.Context, msg *models.Message) (*File, error) {
	var fileID, kind, filename, mime string
	var duration int

	switch {
	case len(msg.Photo) > 0:
		largest := msg.Photo[len(msg.Photo)-1]
		fileID, kind, filename, mime = largest.FileID, "photo", "photo.jpg", "image/jpeg"
	case msg.Video != nil:
		fileID, kind, filename, mime = msg.Video.FileID, "video", orDefault(msg.Video.FileName, "video.mp4"), orDefault(msg.Video.MimeType, "video/mp4")
		duration = msg.Video.Duration
	case msg.VideoNote != nil:
		fileID, kind, filename, mime = msg.VideoNote.FileID, "video_note", "video_note.mp4", "video/mp4"
		duration = msg.VideoNote.Duration
	case msg.Voice != nil:
		fileID, kind, filename, mime = msg.Voice.FileID, "voice", "voice.ogg", orDefault(msg.Voice.MimeType, "audio/ogg; codecs=opus")
		duration = msg.Voice.Duration
	case msg.Audio != nil:
		fileID, kind, filename, mime = msg.Audio.FileID, "audio", orDefault(msg.Audio.FileName, "audio.mp3"), orDefault(msg.Audio.MimeType, "audio/mpeg")
		duration = msg.Audio.Duration
	case msg.Sticker != nil:
		// Animated (.tgs) and video (.webm) stickers have no WhatsApp
		// equivalent, so pass them through as files instead of broken stickers.
		switch {
		case msg.Sticker.IsAnimated:
			fileID, kind, filename, mime = msg.Sticker.FileID, "document", "sticker.tgs", "application/gzip"
		case msg.Sticker.IsVideo:
			fileID, kind, filename, mime = msg.Sticker.FileID, "document", "sticker.webm", "video/webm"
		default:
			fileID, kind, filename, mime = msg.Sticker.FileID, "sticker", "sticker.webp", "image/webp"
		}
	case msg.Document != nil:
		fileID, kind, filename, mime = msg.Document.FileID, "document", orDefault(msg.Document.FileName, "file"), orDefault(msg.Document.MimeType, "application/octet-stream")
	default:
		return nil, nil
	}

	info, err := c.bot.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("get file info: %w", err)
	}
	if info.FileSize > maxDownloadBytes {
		return nil, fmt.Errorf("file is %.1f MB, above the %d MB bot download limit", float64(info.FileSize)/(1<<20), maxDownloadBytes>>20)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.bot.FileDownloadLink(info), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download file: telegram returned %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if len(data) > maxDownloadBytes {
		return nil, fmt.Errorf("file exceeds the %d MB bot download limit", maxDownloadBytes>>20)
	}

	return &File{Kind: kind, Data: data, MIME: mime, Filename: filename, Duration: duration}, nil
}

func parseCommand(msg *models.Message) (command, args string) {
	text := msg.Text
	if text == "" || !strings.HasPrefix(text, "/") {
		return "", ""
	}
	for _, e := range msg.Entities {
		if e.Type == models.MessageEntityTypeBotCommand && e.Offset == 0 {
			runes := []rune(text)
			command = strings.TrimPrefix(string(runes[:e.Length]), "/")
			command, _, _ = strings.Cut(command, "@") // strip /cmd@BotName
			args = strings.TrimSpace(string(runes[e.Length:]))
			return command, args
		}
	}
	return "", ""
}

// topicGone reports whether the error means the thread was deleted.
func topicGone(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "message thread not found") || strings.Contains(msg, "topic_deleted")
}

func wrapTopicErr(err error) error {
	if topicGone(err) {
		return ErrTopicMissing
	}
	return err
}

// splitMessage breaks text into Telegram-sized chunks, preferring line breaks.
func splitMessage(text string) []string {
	const limit = 4000
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}

	var chunks []string
	for len(runes) > limit {
		cut := limit
		// Prefer breaking on the last newline in the second half of the chunk.
		for i := limit - 1; i > limit/2; i-- {
			if runes[i] == '\n' {
				cut = i + 1
				break
			}
		}
		chunks = append(chunks, string(runes[:cut]))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		chunks = append(chunks, string(runes))
	}
	return chunks
}

func truncateName(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\n", " "))
	if name == "" {
		name = "WhatsApp"
	}
	if runes := []rune(name); len(runes) > 128 {
		return string(runes[:127]) + "…"
	}
	return name
}

func truncateCaption(caption string) string {
	if runes := []rune(caption); len(runes) > 1024 {
		return string(runes[:1023]) + "…"
	}
	return caption
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
