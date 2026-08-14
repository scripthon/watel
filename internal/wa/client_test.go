package wa

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// TestNewCreatesSession verifies the whatsmeow session store works with the
// pure-Go sqlite driver, including running its schema migrations. This is the
// one piece of wiring that silently fails at runtime if the driver name or DSN
// is wrong, so it is worth exercising without a real WhatsApp connection.
func TestNewCreatesSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "whatsapp.db")

	client, err := New(context.Background(), Options{SessionDB: path, LogLevel: "ERROR"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Disconnect()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("session database was not created: %v", err)
	}
	if client.IsLoggedIn() {
		t.Error("a fresh session should not report being logged in")
	}
	if !client.OwnJID().IsEmpty() {
		t.Errorf("OwnJID = %v, want the zero JID before login", client.OwnJID())
	}
}

func TestDisconnectIsIdempotent(t *testing.T) {
	client, err := New(context.Background(), Options{
		SessionDB: filepath.Join(t.TempDir(), "whatsapp.db"),
		LogLevel:  "ERROR",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	client.Disconnect()
	client.Disconnect() // must not panic on the already-closed queue
}

func TestDisplayNameFallsBackToNumber(t *testing.T) {
	client, err := New(context.Background(), Options{
		SessionDB: filepath.Join(t.TempDir(), "whatsapp.db"),
		LogLevel:  "ERROR",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Disconnect()

	ctx := context.Background()
	pn := types.NewJID("6281234567890", types.DefaultUserServer)
	lid := types.NewJID("112233445566", types.HiddenUserServer)

	if got := client.DisplayName(ctx, pn, ""); got != "+6281234567890" {
		t.Errorf("DisplayName without a contact = %q, want the number", got)
	}
	if got := client.DisplayName(ctx, pn, "Budi"); got != "Budi" {
		t.Errorf("DisplayName with a push name = %q, want %q", got, "Budi")
	}
	if got := client.DisplayName(ctx, lid, ""); got != "112233445566" {
		t.Errorf("DisplayName for a LID = %q, want the bare user part", got)
	}
}

// newOfflineClient builds a client with a throwaway session database. It is
// never connected, so only the local event-handling path is exercised.
func newOfflineClient(t *testing.T) *Client {
	t.Helper()
	client, err := New(context.Background(), Options{
		SessionDB:         filepath.Join(t.TempDir(), "whatsapp.db"),
		LogLevel:          "ERROR",
		MirrorOwnMessages: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(client.Disconnect)
	return client
}

func undecryptable(chat types.JID, unavailable bool, kind events.UnavailableType) *events.UndecryptableMessage {
	return &events.UndecryptableMessage{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat},
			ID:            "ABC123",
			Timestamp:     time.Now(),
		},
		IsUnavailable:   unavailable,
		UnavailableType: kind,
	}
}

// A view-once message carries no ciphertext for linked devices, so the bridge
// must still surface a placeholder rather than dropping it silently.
func TestViewOnceIsReportedAsNotice(t *testing.T) {
	client := newOfflineClient(t)

	got := make(chan Incoming, 1)
	client.OnMessage(func(msg Incoming) { got <- msg })

	chat := types.NewJID("6281234567890", types.DefaultUserServer)
	client.handleEvent(undecryptable(chat, true, events.UnavailableTypeViewOnce))

	select {
	case msg := <-got:
		if msg.Kind != KindNotice {
			t.Errorf("Kind = %q, want %q", msg.Kind, KindNotice)
		}
		if msg.ChatJID != chat {
			t.Errorf("ChatJID = %v, want %v", msg.ChatJID, chat)
		}
		if !strings.Contains(strings.ToLower(msg.Text), "view-once") {
			t.Errorf("Text = %q, want it to mention view-once", msg.Text)
		}
		if !strings.Contains(strings.ToLower(msg.Text), "phone") {
			t.Errorf("Text = %q, want it to tell the user to open their phone", msg.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notice was delivered for a view-once message")
	}
}

// A plain decryption failure is transient: whatsmeow re-requests the message
// and the real one arrives shortly after, so announcing it would duplicate.
func TestTransientDecryptionFailureIsNotReported(t *testing.T) {
	client := newOfflineClient(t)

	got := make(chan Incoming, 1)
	client.OnMessage(func(msg Incoming) { got <- msg })

	chat := types.NewJID("6281234567890", types.DefaultUserServer)
	client.handleEvent(undecryptable(chat, true, events.UnavailableTypeUnknown))
	client.handleEvent(undecryptable(chat, false, events.UnavailableTypeViewOnce))

	select {
	case msg := <-got:
		t.Errorf("unexpected notice for a transient failure: %q", msg.Text)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestGroupUndecryptableIsIgnored(t *testing.T) {
	client := newOfflineClient(t)

	got := make(chan Incoming, 1)
	client.OnMessage(func(msg Incoming) { got <- msg })

	evt := undecryptable(types.NewJID("12345", types.GroupServer), true, events.UnavailableTypeViewOnce)
	evt.Info.IsGroup = true
	client.handleEvent(evt)

	select {
	case msg := <-got:
		t.Errorf("group message should not be bridged, got %q", msg.Text)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestResolveNumberRejectsShortInput(t *testing.T) {
	client, err := New(context.Background(), Options{
		SessionDB: filepath.Join(t.TempDir(), "whatsapp.db"),
		LogLevel:  "ERROR",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Disconnect()

	// Rejected before any network call, so this is safe offline.
	if _, err := client.ResolveNumber(context.Background(), "+62 812"); err == nil {
		t.Error("ResolveNumber should reject a number that is too short")
	}
}
