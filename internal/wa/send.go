package wa

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// OutgoingKind selects which WhatsApp message type to build for an upload.
type OutgoingKind string

const (
	OutImage    OutgoingKind = "image"
	OutVideo    OutgoingKind = "video"
	OutAudio    OutgoingKind = "audio"
	OutVoice    OutgoingKind = "voice"
	OutDocument OutgoingKind = "document"
	OutSticker  OutgoingKind = "sticker"
)

// Outgoing is a file to upload to WhatsApp.
type Outgoing struct {
	Kind     OutgoingKind
	Data     []byte
	MIME     string
	Filename string
	Caption  string
	Duration int // seconds, for audio/video
}

// SendText delivers a plain text message.
func (c *Client) SendText(ctx context.Context, to types.JID, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("refusing to send an empty message")
	}
	_, err := c.cli.SendMessage(ctx, to, &waE2E.Message{
		Conversation: proto.String(text),
	})
	return err
}

// SendMedia uploads the payload and sends it as the matching WhatsApp message type.
func (c *Client) SendMedia(ctx context.Context, to types.JID, out Outgoing) error {
	mediaType, err := uploadTypeFor(out.Kind)
	if err != nil {
		return err
	}

	uploaded, err := c.cli.Upload(ctx, out.Data, mediaType)
	if err != nil {
		return fmt.Errorf("upload %s: %w", out.Kind, err)
	}

	mime := out.MIME
	if mime == "" {
		mime = "application/octet-stream"
	}

	var msg *waE2E.Message
	switch out.Kind {
	case OutImage:
		msg = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Caption:       optionalString(out.Caption),
			Mimetype:      proto.String(mime),
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
		}}
	case OutVideo:
		msg = &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			Caption:       optionalString(out.Caption),
			Mimetype:      proto.String(mime),
			Seconds:       optionalUint32(out.Duration),
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
		}}
	case OutAudio, OutVoice:
		msg = &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			Mimetype:      proto.String(mime),
			Seconds:       optionalUint32(out.Duration),
			PTT:           proto.Bool(out.Kind == OutVoice),
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
		}}
	case OutSticker:
		msg = &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
			Mimetype:      proto.String(mime),
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
		}}
	default: // OutDocument
		filename := out.Filename
		if filename == "" {
			filename = "file"
		}
		msg = &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			Caption:       optionalString(out.Caption),
			FileName:      proto.String(filename),
			Mimetype:      proto.String(mime),
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			MediaKey:      uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
		}}
	}

	if _, err := c.cli.SendMessage(ctx, to, msg); err != nil {
		return fmt.Errorf("send %s: %w", out.Kind, err)
	}
	return nil
}

// MarkRead flags messages as read on WhatsApp, so replying from Telegram
// clears the unread badge on the phone too.
func (c *Client) MarkRead(ctx context.Context, chat types.JID, messageIDs []types.MessageID) error {
	if len(messageIDs) == 0 {
		return nil
	}
	return c.cli.MarkRead(ctx, messageIDs, time.Now(), chat, chat)
}

func uploadTypeFor(kind OutgoingKind) (whatsmeow.MediaType, error) {
	switch kind {
	case OutImage:
		return whatsmeow.MediaImage, nil
	case OutVideo:
		return whatsmeow.MediaVideo, nil
	case OutAudio, OutVoice:
		return whatsmeow.MediaAudio, nil
	case OutSticker:
		// Stickers are uploaded on the image channel.
		return whatsmeow.MediaImage, nil
	case OutDocument:
		return whatsmeow.MediaDocument, nil
	default:
		return "", fmt.Errorf("unknown outgoing kind %q", kind)
	}
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return proto.String(s)
}

func optionalUint32(n int) *uint32 {
	if n <= 0 {
		return nil
	}
	v := uint32(n)
	return &v
}
