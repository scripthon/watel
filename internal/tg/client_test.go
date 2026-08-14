package tg

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestSplitMessageKeepsShortTextIntact(t *testing.T) {
	chunks := splitMessage("halo")
	if len(chunks) != 1 || chunks[0] != "halo" {
		t.Fatalf("splitMessage = %q, want one chunk %q", chunks, "halo")
	}
}

func TestSplitMessageRespectsLimitAndLosesNothing(t *testing.T) {
	// Lines make the newline-aware split path do real work.
	var b strings.Builder
	for range 900 {
		b.WriteString("a reasonably long line of message text\n")
	}
	original := b.String()

	chunks := splitMessage(original)
	if len(chunks) < 2 {
		t.Fatalf("expected the text to be split, got %d chunk(s)", len(chunks))
	}
	for i, chunk := range chunks {
		if n := len([]rune(chunk)); n > 4000 {
			t.Errorf("chunk %d has %d runes, above the 4000 limit", i, n)
		}
	}
	if rejoined := strings.Join(chunks, ""); rejoined != original {
		t.Errorf("rejoined text differs from the original (%d vs %d bytes)", len(rejoined), len(original))
	}
}

func TestSplitMessageHandlesMultibyteRunes(t *testing.T) {
	original := strings.Repeat("emoji 🙂 text ", 800)
	chunks := splitMessage(original)

	for i, chunk := range chunks {
		if n := len([]rune(chunk)); n > 4000 {
			t.Errorf("chunk %d has %d runes, above the 4000 limit", i, n)
		}
		if !strings.ContainsRune(chunk, '🙂') {
			t.Errorf("chunk %d lost its emoji, likely cut mid-rune", i)
		}
	}
	if rejoined := strings.Join(chunks, ""); rejoined != original {
		t.Error("rejoined text differs from the original")
	}
}

func TestTruncateName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Budi", "Budi"},
		{"newlines flattened", "Budi\nSantoso", "Budi Santoso"},
		{"empty falls back", "   ", "WhatsApp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateName(tt.in); got != tt.want {
				t.Errorf("truncateName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	long := truncateName(strings.Repeat("a", 300))
	if n := len([]rune(long)); n > 128 {
		t.Errorf("truncateName produced %d runes, Telegram allows 128", n)
	}
}

func TestParseCommand(t *testing.T) {
	newMsg := func(text string, entities ...models.MessageEntity) *models.Message {
		return &models.Message{Text: text, Entities: entities}
	}
	cmdEntity := func(length int) models.MessageEntity {
		return models.MessageEntity{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: length}
	}

	tests := []struct {
		name     string
		msg      *models.Message
		wantCmd  string
		wantArgs string
	}{
		{"no command", newMsg("halo"), "", ""},
		{"bare command", newMsg("/status", cmdEntity(7)), "status", ""},
		{"command with args", newMsg("/new 6281234567890", cmdEntity(4)), "new", "6281234567890"},
		{"command addressed to bot", newMsg("/new@MyBot 628123", cmdEntity(10)), "new", "628123"},
		{"slash but no entity", newMsg("/notacommand"), "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCmd, gotArgs := parseCommand(tt.msg)
			if gotCmd != tt.wantCmd || gotArgs != tt.wantArgs {
				t.Errorf("parseCommand = (%q, %q), want (%q, %q)", gotCmd, gotArgs, tt.wantCmd, tt.wantArgs)
			}
		})
	}
}

func TestTopicGone(t *testing.T) {
	if !topicGone(errMsg("Bad Request: message thread not found")) {
		t.Error("a deleted topic error should be recognised")
	}
	if topicGone(errMsg("Bad Request: chat not found")) {
		t.Error("an unrelated error should not be treated as a deleted topic")
	}
	if topicGone(nil) {
		t.Error("nil is not a topic error")
	}
}

type errMsg string

func (e errMsg) Error() string { return string(e) }
