// Package bridge glues the WhatsApp and Telegram sides together: it owns the
// "one topic per contact" rule and both message directions.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/thxrhmn/watel/internal/store"
	"github.com/thxrhmn/watel/internal/tg"
	"github.com/thxrhmn/watel/internal/wa"
)

// Bridge routes messages between one WhatsApp account and one Telegram supergroup.
type Bridge struct {
	wa    *wa.Client
	tg    *tg.Client
	store *store.Store

	// topicLocks serialises topic creation per chat so two messages arriving
	// together from a new contact cannot create two topics.
	topicLocks sync.Map // chat jid string -> *sync.Mutex

	// lastMessages remembers recent WhatsApp message ids per chat so a reply
	// from Telegram can also mark the conversation read on the phone.
	lastMessages sync.Map // chat jid string -> []types.MessageID
}

// New wires the three components together.
func New(waClient *wa.Client, tgClient *tg.Client, st *store.Store) *Bridge {
	return &Bridge{wa: waClient, tg: tgClient, store: st}
}

// HandleWhatsApp forwards one incoming WhatsApp message into its topic,
// creating the topic first if this contact has never written before.
func (b *Bridge) HandleWhatsApp(msg wa.Incoming) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	topicID, err := b.topicFor(ctx, msg.ChatJID, msg.DisplayName)
	if err != nil {
		log.Printf("bridge: no topic for %s: %v", msg.ChatJID, err)
		return
	}

	b.rememberMessageID(msg.ChatJID, msg.ID)

	if err := b.deliverToTopic(ctx, topicID, msg); err != nil {
		if !errors.Is(err, tg.ErrTopicMissing) {
			log.Printf("bridge: deliver to topic %d failed: %v", topicID, err)
			return
		}
		// The topic was deleted in Telegram. Drop the mapping, make a new one
		// and deliver again so the message is not lost.
		log.Printf("bridge: topic %d is gone, recreating for %s", topicID, msg.ChatJID)
		if err := b.store.UnbindChat(ctx, msg.ChatJID.String()); err != nil {
			log.Printf("bridge: unbind %s: %v", msg.ChatJID, err)
			return
		}
		topicID, err = b.topicFor(ctx, msg.ChatJID, msg.DisplayName)
		if err != nil {
			log.Printf("bridge: recreate topic for %s: %v", msg.ChatJID, err)
			return
		}
		if err := b.deliverToTopic(ctx, topicID, msg); err != nil {
			log.Printf("bridge: deliver after recreate failed: %v", err)
		}
	}
}

// topicFor returns the topic bound to a chat, creating it on first contact.
func (b *Bridge) topicFor(ctx context.Context, chat types.JID, displayName string) (int, error) {
	key := chat.String()

	lock, _ := b.topicLocks.LoadOrStore(key, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	mapping, err := b.store.TopicForChat(ctx, key)
	switch {
	case err == nil:
		return mapping.TopicID, nil
	case !errors.Is(err, store.ErrNotFound):
		return 0, fmt.Errorf("look up topic: %w", err)
	}

	title := topicTitle(displayName, chat)
	topicID, err := b.tg.CreateTopic(ctx, title)
	if err != nil {
		return 0, err
	}
	if err := b.store.BindTopic(ctx, key, topicID, title); err != nil {
		return 0, fmt.Errorf("save mapping: %w", err)
	}

	log.Printf("bridge: created topic %d for %s (%s)", topicID, key, title)
	if err := b.tg.SendText(ctx, topicID, topicHeader(displayName, chat)); err != nil {
		log.Printf("bridge: topic header failed: %v", err)
	}
	return topicID, nil
}

// deliverToTopic renders one WhatsApp message inside a Telegram topic.
func (b *Bridge) deliverToTopic(ctx context.Context, topicID int, msg wa.Incoming) error {
	var prefix strings.Builder
	if msg.FromMe {
		prefix.WriteString("↪️ you: ")
	}
	if msg.Quoted != "" {
		prefix.WriteString("↩️ re: ")
		prefix.WriteString(msg.Quoted)
		prefix.WriteString("\n")
	}

	body := prefix.String() + msg.Text

	if msg.Media == nil {
		if strings.TrimSpace(body) == "" {
			return nil
		}
		return b.tg.SendText(ctx, topicID, body)
	}

	attachment := tg.Attachment{
		Kind:     attachmentKind(msg.Kind),
		Data:     msg.Media.Data,
		Filename: msg.Media.Filename,
		Caption:  body,
	}
	if msg.Media.IsVoice {
		attachment.Kind = "voice"
	}
	return b.tg.SendAttachment(ctx, topicID, attachment)
}

// HandleTelegram sends a message typed in a topic back to the matching WhatsApp chat.
func (b *Bridge) HandleTelegram(ctx context.Context, reply tg.Reply) {
	if reply.Command != "" {
		b.handleCommand(ctx, reply)
		return
	}
	if reply.TopicID == 0 {
		// The General tab is not bound to any contact; only commands work there.
		return
	}

	mapping, err := b.store.ChatForTopic(ctx, reply.TopicID)
	if errors.Is(err, store.ErrNotFound) {
		b.notify(ctx, reply.TopicID, "⚠️ This topic is not linked to a WhatsApp chat. Use /new <number> in the General tab to start one.")
		return
	}
	if err != nil {
		log.Printf("bridge: look up chat for topic %d: %v", reply.TopicID, err)
		return
	}

	chat, err := types.ParseJID(mapping.ChatJID)
	if err != nil {
		b.notify(ctx, reply.TopicID, "⚠️ Stored contact id is invalid: "+mapping.ChatJID)
		return
	}

	if err := b.sendToWhatsApp(ctx, chat, reply); err != nil {
		b.notify(ctx, reply.TopicID, "⚠️ Not delivered to WhatsApp: "+err.Error())
		return
	}

	if err := b.tg.React(ctx, reply.MessageID, "👍"); err != nil {
		log.Printf("bridge: reaction failed: %v", err)
	}
	b.markRead(ctx, chat)
}

func (b *Bridge) sendToWhatsApp(ctx context.Context, chat types.JID, reply tg.Reply) error {
	if reply.File == nil {
		return b.wa.SendText(ctx, chat, reply.Text)
	}

	out := wa.Outgoing{
		Kind:     outgoingKind(reply.File.Kind),
		Data:     reply.File.Data,
		MIME:     reply.File.MIME,
		Filename: reply.File.Filename,
		Caption:  reply.Text,
		Duration: reply.File.Duration,
	}
	return b.wa.SendMedia(ctx, chat, out)
}

// markRead clears the unread badge on the phone for messages already shown in Telegram.
func (b *Bridge) markRead(ctx context.Context, chat types.JID) {
	key := chat.String()
	value, ok := b.lastMessages.LoadAndDelete(key)
	if !ok {
		return
	}
	ids, _ := value.([]types.MessageID)
	if err := b.wa.MarkRead(ctx, chat, ids); err != nil {
		log.Printf("bridge: mark read for %s: %v", key, err)
	}
}

// rememberMessageID keeps a bounded list of recent ids per chat.
func (b *Bridge) rememberMessageID(chat types.JID, id types.MessageID) {
	if id == "" {
		return
	}
	const keep = 50
	key := chat.String()
	value, _ := b.lastMessages.Load(key)
	ids, _ := value.([]types.MessageID)
	ids = append(ids, id)
	if len(ids) > keep {
		ids = ids[len(ids)-keep:]
	}
	b.lastMessages.Store(key, ids)
}

func (b *Bridge) notify(ctx context.Context, topicID int, text string) {
	if err := b.tg.SendText(ctx, topicID, text); err != nil {
		log.Printf("bridge: notify topic %d: %v", topicID, err)
	}
}

// topicTitle builds the visible name of a contact's topic.
func topicTitle(displayName string, chat types.JID) string {
	displayName = strings.TrimSpace(displayName)
	number := phoneOf(chat)

	switch {
	case displayName == "" && number == "":
		return chat.User
	case displayName == "":
		return number
	case number == "" || displayName == number:
		return displayName
	default:
		return displayName + " (" + number + ")"
	}
}

// topicHeader is the first message posted in a freshly created topic.
func topicHeader(displayName string, chat types.JID) string {
	lines := []string{"🟢 New WhatsApp conversation"}
	if name := strings.TrimSpace(displayName); name != "" {
		lines = append(lines, "Name: "+name)
	}
	if number := phoneOf(chat); number != "" {
		lines = append(lines, "Number: "+number)
	}
	lines = append(lines, "Id: "+chat.String(), "", "Reply in this topic to answer on WhatsApp.")
	return strings.Join(lines, "\n")
}

func phoneOf(chat types.JID) string {
	if chat.Server == types.DefaultUserServer {
		return "+" + chat.User
	}
	return ""
}

// attachmentKind maps a WhatsApp message kind to a Telegram send method.
func attachmentKind(kind wa.Kind) string {
	switch kind {
	case wa.KindImage:
		return "image"
	case wa.KindVideo:
		return "video"
	case wa.KindAudio:
		return "audio"
	case wa.KindSticker:
		return "sticker"
	default:
		return "document"
	}
}

// outgoingKind maps a Telegram attachment to a WhatsApp message type.
func outgoingKind(kind string) wa.OutgoingKind {
	switch kind {
	case "photo":
		return wa.OutImage
	case "video", "video_note":
		return wa.OutVideo
	case "voice":
		return wa.OutVoice
	case "audio":
		return wa.OutAudio
	case "sticker":
		return wa.OutSticker
	default:
		return wa.OutDocument
	}
}
