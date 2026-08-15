package wa

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// viewOnceImage builds a reply that quotes a view-once image, the shape the
// RVO trick relies on.
func viewOnceImage() *waE2E.Message {
	return &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String("baca"),
			ContextInfo: &waE2E.ContextInfo{
				QuotedMessage: &waE2E.Message{
					ImageMessage: &waE2E.ImageMessage{
						ViewOnce:  proto.Bool(true),
						Mimetype:  proto.String("image/jpeg"),
						FileLength: proto.Uint64(1234),
					},
				},
			},
		},
	}
}

func TestViewOnceQuotedDetectsImage(t *testing.T) {
	dm, kind, size, mime, ok := viewOnceQuoted(viewOnceImage())
	if !ok {
		t.Fatal("expected a view-once media to be detected")
	}
	img, isImg := dm.(*waE2E.ImageMessage)
	if !isImg {
		t.Fatalf("expected *ImageMessage, got %T", dm)
	}
	if !img.GetViewOnce() {
		t.Error("quoted media should carry the view-once flag")
	}
	if kind != KindImage || size != 1234 || mime != "image/jpeg" {
		t.Errorf("got kind=%q size=%d mime=%q", kind, size, mime)
	}
}

func TestViewOnceQuotedUnwrapsWrapper(t *testing.T) {
	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String("baca"),
			ContextInfo: &waE2E.ContextInfo{
				QuotedMessage: &waE2E.Message{
					ViewOnceMessage: &waE2E.FutureProofMessage{
						Message: &waE2E.Message{
							ImageMessage: &waE2E.ImageMessage{
								ViewOnce: proto.Bool(true),
							},
						},
					},
				},
			},
		},
	}
	dm, kind, _, _, ok := viewOnceQuoted(msg)
	if !ok {
		t.Fatal("expected wrapped view-once media to be detected")
	}
	if _, isImg := dm.(*waE2E.ImageMessage); !isImg {
		t.Fatalf("expected *ImageMessage after unwrap, got %T", dm)
	}
	if kind != KindImage {
		t.Errorf("kind = %q, want %q", kind, KindImage)
	}
}

func TestViewOnceQuotedIgnoresNormalQuote(t *testing.T) {
	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String("ok"),
			ContextInfo: &waE2E.ContextInfo{
				QuotedMessage: &waE2E.Message{
					Conversation: proto.String("pesan biasa"),
				},
			},
		},
	}
	if _, _, _, _, ok := viewOnceQuoted(msg); ok {
		t.Error("a plain text quote must not be treated as view-once")
	}
}

func TestViewOnceQuotedIgnoresNonViewOnceMedia(t *testing.T) {
	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String("ok"),
			ContextInfo: &waE2E.ContextInfo{
				QuotedMessage: &waE2E.Message{
					ImageMessage: &waE2E.ImageMessage{
						ViewOnce: proto.Bool(false),
					},
				},
			},
		},
	}
	if _, _, _, _, ok := viewOnceQuoted(msg); ok {
		t.Error("a normal (non-view-once) quoted image must not match")
	}
}

func TestViewOnceQuotedIgnoresPlainMessage(t *testing.T) {
	if _, _, _, _, ok := viewOnceQuoted(&waE2E.Message{Conversation: proto.String("hi")}); ok {
		t.Error("a message without a quote must not match")
	}
}

func TestQuotedMessage(t *testing.T) {
	quoted := quotedMessage(viewOnceImage())
	if quoted == nil || quoted.GetImageMessage() == nil {
		t.Fatal("expected the quoted image message")
	}
	if quoted.GetImageMessage().GetMimetype() != "image/jpeg" {
		t.Errorf("mime = %q", quoted.GetImageMessage().GetMimetype())
	}
}

func TestFilenameFor(t *testing.T) {
	tests := []struct {
		name string
		kind string
		mime string
		want string
	}{
		{"jpeg becomes jpg", "image", "image/jpeg", "image.jpg"},
		{"png kept", "image", "image/png", "image.png"},
		{"opus parameters stripped", "audio", "audio/ogg; codecs=opus", "audio.ogg"},
		{"quicktime becomes mov", "video", "video/quicktime", "video.mov"},
		{"docx shortened", "document", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "document.docx"},
		{"empty mime", "document", "", "document.bin"},
		{"unknown long subtype", "document", "application/some-very-long-subtype-name", "document.bin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filenameFor(tt.kind, tt.mime); got != tt.want {
				t.Errorf("filenameFor(%q, %q) = %q, want %q", tt.kind, tt.mime, got, tt.want)
			}
		})
	}
}

func TestFilenameForNeverProducesPathSeparators(t *testing.T) {
	// A hostile mime type must not turn into a path or a traversal attempt.
	for _, mime := range []string{"application/../../etc/passwd", "image/a b", "x/./y"} {
		got := filenameFor("document", mime)
		if strings.ContainsAny(got[len("document."):], "/\\ ") {
			t.Errorf("filenameFor(%q) = %q, which contains a path separator", mime, got)
		}
	}
}

func TestVcardPhones(t *testing.T) {
	vcard := "BEGIN:VCARD\nVERSION:3.0\nFN:Budi\nTEL;type=CELL;waid=6281234567890:+62 812-3456-7890\nEND:VCARD"
	got := vcardPhones(vcard)
	if got != "+62 812-3456-7890" {
		t.Errorf("vcardPhones = %q, want the TEL value", got)
	}
}

func TestVcardPhonesMultipleNumbers(t *testing.T) {
	vcard := "BEGIN:VCARD\nTEL:+6281111\nTEL:+6282222\nEND:VCARD"
	got := vcardPhones(vcard)
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, "+6281111") || !strings.Contains(got, "+6282222") {
		t.Errorf("vcardPhones = %q, want both numbers on separate lines", got)
	}
}

func TestVcardPhonesWithoutTel(t *testing.T) {
	if got := vcardPhones("BEGIN:VCARD\nFN:Budi\nEND:VCARD"); got != "" {
		t.Errorf("vcardPhones = %q, want empty", got)
	}
}
