package wa

import (
	"strings"
	"testing"
)

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
