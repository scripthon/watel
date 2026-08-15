package wa

import (
	"context"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// maxMediaBytes caps what the bridge will download and hand to Telegram.
// Telegram bots cannot upload files larger than 50 MB; leave headroom.
const maxMediaBytes = 45 << 20

// extract converts a whatsmeow message event into an Incoming, downloading
// media when present. It returns nil for messages with nothing to show
// (protocol messages, empty payloads, oversized media that also had no text).
func (c *Client) extract(ctx context.Context, evt *events.Message) *Incoming {
	msg := evt.Message
	if msg == nil {
		return nil
	}

	chat := c.canonicalJID(ctx, evt.Info.Chat)
	nameSource := evt.Info.Chat
	if evt.Info.IsFromMe {
		// For our own messages the chat is the recipient, so the push name on
		// the event belongs to us and must not be used as the contact name.
		nameSource = chat
	}

	out := &Incoming{
		ChatJID:     chat,
		ID:          evt.Info.ID,
		DisplayName: c.DisplayName(ctx, nameSource, ownPushName(evt)),
		Kind:        KindText,
		FromMe:      evt.Info.IsFromMe,
		Timestamp:   evt.Info.Timestamp,
		Quoted:      quotedExcerpt(msg),
	}
	out.Phone = phoneOf(chat)

	switch {
	case msg.GetConversation() != "":
		out.Text = msg.GetConversation()

	case msg.GetExtendedTextMessage() != nil:
		out.Text = msg.GetExtendedTextMessage().GetText()

	case msg.GetImageMessage() != nil:
		m := msg.GetImageMessage()
		out.Kind, out.Text = KindImage, m.GetCaption()
		out.Media = c.download(ctx, m, m.GetFileLength(), m.GetMimetype(), "image", out)

	case msg.GetVideoMessage() != nil:
		m := msg.GetVideoMessage()
		out.Kind, out.Text = KindVideo, m.GetCaption()
		out.Media = c.download(ctx, m, m.GetFileLength(), m.GetMimetype(), "video", out)
		if out.Media != nil {
			out.Media.Duration = int(m.GetSeconds())
		}

	case msg.GetPtvMessage() != nil: // round "video note"
		m := msg.GetPtvMessage()
		out.Kind = KindVideo
		out.Media = c.download(ctx, m, m.GetFileLength(), m.GetMimetype(), "video", out)

	case msg.GetAudioMessage() != nil:
		m := msg.GetAudioMessage()
		out.Kind = KindAudio
		out.Media = c.download(ctx, m, m.GetFileLength(), m.GetMimetype(), "audio", out)
		if out.Media != nil {
			out.Media.IsVoice = m.GetPTT()
			out.Media.Duration = int(m.GetSeconds())
		}

	case msg.GetDocumentMessage() != nil:
		m := msg.GetDocumentMessage()
		out.Kind, out.Text = KindDocument, m.GetCaption()
		out.Media = c.download(ctx, m, m.GetFileLength(), m.GetMimetype(), "document", out)
		if out.Media != nil && m.GetFileName() != "" {
			out.Media.Filename = m.GetFileName()
		}

	case msg.GetStickerMessage() != nil:
		m := msg.GetStickerMessage()
		out.Kind = KindSticker
		out.Media = c.download(ctx, m, m.GetFileLength(), m.GetMimetype(), "sticker", out)

	case msg.GetLocationMessage() != nil:
		m := msg.GetLocationMessage()
		lat, lon := m.GetDegreesLatitude(), m.GetDegreesLongitude()
		label := strings.TrimSpace(m.GetName() + " " + m.GetAddress())
		if label == "" {
			label = fmt.Sprintf("%.6f, %.6f", lat, lon)
		}
		out.Kind = KindNotice
		out.Text = fmt.Sprintf("📍 %s\nhttps://maps.google.com/?q=%.6f,%.6f", label, lat, lon)

	case msg.GetLiveLocationMessage() != nil:
		m := msg.GetLiveLocationMessage()
		out.Kind = KindNotice
		out.Text = fmt.Sprintf("📍 Live location: %.6f, %.6f", m.GetDegreesLatitude(), m.GetDegreesLongitude())

	case msg.GetContactMessage() != nil:
		m := msg.GetContactMessage()
		out.Kind = KindNotice
		out.Text = "👤 Contact: " + m.GetDisplayName() + "\n" + vcardPhones(m.GetVcard())

	case msg.GetContactsArrayMessage() != nil:
		names := make([]string, 0, len(msg.GetContactsArrayMessage().GetContacts()))
		for _, contact := range msg.GetContactsArrayMessage().GetContacts() {
			names = append(names, contact.GetDisplayName())
		}
		out.Kind = KindNotice
		out.Text = "👤 Contacts: " + strings.Join(names, ", ")

	case msg.GetReactionMessage() != nil:
		out.Kind = KindNotice
		out.Text = "Reacted " + msg.GetReactionMessage().GetText() + " to an earlier message"

	case msg.GetPollCreationMessage() != nil || msg.GetPollCreationMessageV2() != nil || msg.GetPollCreationMessageV3() != nil:
		poll := msg.GetPollCreationMessage()
		if poll == nil {
			poll = msg.GetPollCreationMessageV2()
		}
		if poll == nil {
			poll = msg.GetPollCreationMessageV3()
		}
		options := make([]string, 0, len(poll.GetOptions()))
		for _, opt := range poll.GetOptions() {
			options = append(options, "• "+opt.GetOptionName())
		}
		out.Kind = KindNotice
		out.Text = "📊 Poll: " + poll.GetName() + "\n" + strings.Join(options, "\n")

	case msg.GetProtocolMessage() != nil, msg.GetSenderKeyDistributionMessage() != nil:
		// Deletions, key distribution, history sync notifications: nothing to show.
		return nil

	default:
		return nil
	}

	// A text reply that quotes a view-once message carries the quoted media in
	// ContextInfo, so it can be recovered even though the original payload was
	// withheld from linked devices (the RVO trick). Only when the reply itself
	// has no media.
	if out.Media == nil && !c.applyQuotedViewOnce(ctx, msg, out) {
		if strings.TrimSpace(out.Text) == "" {
			return nil
		}
	}
	if evt.IsViewOnce {
		out.Text = strings.TrimSpace("👁 (view once) " + out.Text)
	}
	if evt.IsEdit {
		out.Text = strings.TrimSpace("✏️ (edited) " + out.Text)
	}
	return out
}

// applyQuotedViewOnce implements the RVO (read-view-once) trick: when a reply
// quotes a view-once message, WhatsApp includes the full media of the quoted
// view-once message in the reply's ContextInfo.QuotedMessage, and that media
// can be downloaded normally. This is how a view-once message is read even
// though its own payload is withheld from linked devices. It returns true if
// a message was produced (media or a notice), false if nothing to forward.
func (c *Client) applyQuotedViewOnce(ctx context.Context, msg *waE2E.Message, out *Incoming) bool {
	dm, kind, size, mime, ok := viewOnceQuoted(msg)
	if !ok {
		return false
	}
	out.Kind = kind
	out.Quoted = "👁 view once"
	out.Media = c.download(ctx, dm, size, mime, string(kind), out)
	return out.Media != nil
}

// viewOnceQuoted finds the view-once media inside a reply's quoted message.
// WhatsApp embeds the quoted view-once media in ContextInfo, so it is
// downloadable even though the original message never reached this device.
func viewOnceQuoted(msg *waE2E.Message) (whatsmeow.DownloadableMessage, Kind, uint64, string, bool) {
	quoted := quotedMessage(msg)
	if quoted == nil || quoted.GetReactionMessage() != nil {
		return nil, "", 0, "", false
	}

	// The quoted media may arrive wrapped in a ViewOnceMessage (or V2) payload;
	// unwrap it first so GetImageMessage etc. see the media.
	quoted = unwrapViewOnce(quoted)

	var (
		dm   whatsmeow.DownloadableMessage
		kind Kind
		size uint64
		mime string
		isVO bool
	)
	switch {
	case quoted.GetImageMessage() != nil:
		m := quoted.GetImageMessage()
		dm, kind, size, mime, isVO = m, KindImage, m.GetFileLength(), m.GetMimetype(), m.GetViewOnce()
	case quoted.GetVideoMessage() != nil:
		m := quoted.GetVideoMessage()
		dm, kind, size, mime, isVO = m, KindVideo, m.GetFileLength(), m.GetMimetype(), m.GetViewOnce()
	case quoted.GetAudioMessage() != nil:
		m := quoted.GetAudioMessage()
		dm, kind, size, mime, isVO = m, KindAudio, m.GetFileLength(), m.GetMimetype(), m.GetViewOnce()
	case quoted.GetPtvMessage() != nil:
		m := quoted.GetPtvMessage()
		dm, kind, size, mime, isVO = m, KindVideo, m.GetFileLength(), m.GetMimetype(), m.GetViewOnce()
	default:
		return nil, "", 0, "", false
	}
	if !isVO {
		return nil, "", 0, "", false
	}
	return dm, kind, size, mime, true
}

// unwrapViewOnce pulls the inner message out of a ViewOnceMessage wrapper.
func unwrapViewOnce(msg *waE2E.Message) *waE2E.Message {
	if inner := msg.GetViewOnceMessage().GetMessage(); inner != nil {
		return inner
	}
	if inner := msg.GetViewOnceMessageV2().GetMessage(); inner != nil {
		return inner
	}
	if inner := msg.GetViewOnceMessageV2Extension().GetMessage(); inner != nil {
		return inner
	}
	return msg
}

// download fetches and decrypts media, refusing anything Telegram could not
// accept. On failure it records a note on the message instead of dropping it.
func (c *Client) download(ctx context.Context, msg whatsmeow.DownloadableMessage, size uint64, mime, kind string, out *Incoming) *Media {
	if size > maxMediaBytes {
		out.Kind = KindNotice
		out.Text = strings.TrimSpace(fmt.Sprintf("📎 %s too large to forward (%.1f MB)\n%s", kind, float64(size)/(1<<20), out.Text))
		return nil
	}

	data, err := c.cli.Download(ctx, msg)
	if err != nil {
		c.log.Warnf("Download %s failed: %v", kind, err)
		out.Kind = KindNotice
		out.Text = strings.TrimSpace(fmt.Sprintf("📎 %s could not be downloaded (%v)\n%s", kind, err, out.Text))
		return nil
	}
	if len(data) > maxMediaBytes {
		out.Kind = KindNotice
		out.Text = strings.TrimSpace(fmt.Sprintf("📎 %s too large to forward (%.1f MB)\n%s", kind, float64(len(data))/(1<<20), out.Text))
		return nil
	}

	return &Media{
		Data:     data,
		MIME:     mime,
		Filename: filenameFor(kind, mime),
	}
}

// phoneOf returns the bare phone number of a chat, or "" for LID-only contacts.
func phoneOf(chat types.JID) string {
	if chat.Server == types.DefaultUserServer {
		return chat.User
	}
	return ""
}

// ownPushName returns the push name attached to the event, which is only
// meaningful for messages received from the other party.
func ownPushName(evt *events.Message) string {
	if evt.Info.IsFromMe {
		return ""
	}
	return evt.Info.PushName
}

// quotedMessage returns the message a reply is quoting, or nil.
func quotedMessage(msg *waE2E.Message) *waE2E.Message {
	var ctxInfo *waE2E.ContextInfo
	switch {
	case msg.GetExtendedTextMessage() != nil:
		ctxInfo = msg.GetExtendedTextMessage().GetContextInfo()
	case msg.GetImageMessage() != nil:
		ctxInfo = msg.GetImageMessage().GetContextInfo()
	case msg.GetVideoMessage() != nil:
		ctxInfo = msg.GetVideoMessage().GetContextInfo()
	case msg.GetAudioMessage() != nil:
		ctxInfo = msg.GetAudioMessage().GetContextInfo()
	case msg.GetDocumentMessage() != nil:
		ctxInfo = msg.GetDocumentMessage().GetContextInfo()
	case msg.GetStickerMessage() != nil:
		ctxInfo = msg.GetStickerMessage().GetContextInfo()
	}
	if ctxInfo == nil {
		return nil
	}
	return ctxInfo.GetQuotedMessage()
}

// quotedExcerpt renders a short preview of the message being replied to.
func quotedExcerpt(msg *waE2E.Message) string {
	quoted := quotedMessage(msg)
	if quoted == nil {
		return ""
	}

	var text string
	switch {
	case quoted.GetConversation() != "":
		text = quoted.GetConversation()
	case quoted.GetExtendedTextMessage() != nil:
		text = quoted.GetExtendedTextMessage().GetText()
	case quoted.GetImageMessage() != nil:
		text = "🖼 " + quoted.GetImageMessage().GetCaption()
	case quoted.GetVideoMessage() != nil:
		text = "🎬 " + quoted.GetVideoMessage().GetCaption()
	case quoted.GetAudioMessage() != nil:
		text = "🎙 voice message"
	case quoted.GetDocumentMessage() != nil:
		text = "📎 " + quoted.GetDocumentMessage().GetFileName()
	case quoted.GetStickerMessage() != nil:
		text = "🌟 sticker"
	default:
		text = "message"
	}

	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	const limit = 80
	if len([]rune(text)) > limit {
		text = string([]rune(text)[:limit]) + "…"
	}
	return text
}

// vcardPhones pulls the TEL lines out of a vCard so the number is visible
// without opening an attachment.
func vcardPhones(vcard string) string {
	var phones []string
	for line := range strings.SplitSeq(vcard, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(line), "TEL") {
			continue
		}
		if _, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(value) != "" {
			phones = append(phones, strings.TrimSpace(value))
		}
	}
	return strings.Join(phones, "\n")
}

// filenameFor invents a reasonable file name from the media kind and mime type.
func filenameFor(kind, mime string) string {
	var ext string
	if _, subtype, ok := strings.Cut(mime, "/"); ok {
		ext, _, _ = strings.Cut(subtype, ";") // drop parameters like "; codecs=opus"
		ext = strings.TrimSpace(ext)
	}
	switch ext {
	case "jpeg":
		ext = "jpg"
	case "quicktime":
		ext = "mov"
	case "ogg":
		ext = "ogg"
	case "vnd.openxmlformats-officedocument.wordprocessingml.document":
		ext = "docx"
	case "vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		ext = "xlsx"
	case "":
		ext = "bin"
	}
	if strings.ContainsAny(ext, "./ ") || len(ext) > 8 {
		ext = "bin"
	}
	return kind + "." + ext
}
