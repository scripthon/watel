package bridge

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestTopicTitle(t *testing.T) {
	pn := types.NewJID("6281234567890", types.DefaultUserServer)
	lid := types.NewJID("112233445566", types.HiddenUserServer)

	tests := []struct {
		name        string
		displayName string
		jid         types.JID
		want        string
	}{
		{"name and number", "Budi", pn, "Budi (+6281234567890)"},
		{"number only", "", pn, "+6281234567890"},
		{"name equals number", "+6281234567890", pn, "+6281234567890"},
		{"lid without number", "Siti", lid, "Siti"},
		{"lid without name", "", lid, "112233445566"},
		{"whitespace name", "   ", pn, "+6281234567890"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := topicTitle(tt.displayName, tt.jid); got != tt.want {
				t.Errorf("topicTitle(%q) = %q, want %q", tt.displayName, got, tt.want)
			}
		})
	}
}

func TestTopicHeaderIncludesIdentity(t *testing.T) {
	jid := types.NewJID("6281234567890", types.DefaultUserServer)
	header := topicHeader("Budi", jid)

	for _, want := range []string{"Budi", "+6281234567890", jid.String()} {
		if !strings.Contains(header, want) {
			t.Errorf("header %q is missing %q", header, want)
		}
	}
}
