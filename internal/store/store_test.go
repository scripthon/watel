package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBindAndLookup(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	const jid = "6281234567890@s.whatsapp.net"
	if err := s.BindTopic(ctx, jid, 42, "Budi (+6281234567890)"); err != nil {
		t.Fatalf("BindTopic: %v", err)
	}

	byChat, err := s.TopicForChat(ctx, jid)
	if err != nil {
		t.Fatalf("TopicForChat: %v", err)
	}
	if byChat.TopicID != 42 {
		t.Errorf("TopicForChat topic = %d, want 42", byChat.TopicID)
	}

	byTopic, err := s.ChatForTopic(ctx, 42)
	if err != nil {
		t.Fatalf("ChatForTopic: %v", err)
	}
	if byTopic.ChatJID != jid {
		t.Errorf("ChatForTopic jid = %q, want %q", byTopic.ChatJID, jid)
	}
	if byTopic.CreatedAt.IsZero() {
		t.Error("CreatedAt was not populated")
	}
}

func TestLookupMissingReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.TopicForChat(ctx, "nobody@s.whatsapp.net"); !errors.Is(err, ErrNotFound) {
		t.Errorf("TopicForChat err = %v, want ErrNotFound", err)
	}
	if _, err := s.ChatForTopic(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("ChatForTopic err = %v, want ErrNotFound", err)
	}
}

// Rebinding is what happens after someone deletes a topic in Telegram: the
// chat must point at the new topic and the old topic id must be gone.
func TestRebindReplacesTopic(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	const jid = "6281234567890@s.whatsapp.net"
	if err := s.BindTopic(ctx, jid, 10, "old"); err != nil {
		t.Fatalf("BindTopic: %v", err)
	}
	if err := s.UnbindChat(ctx, jid); err != nil {
		t.Fatalf("UnbindChat: %v", err)
	}
	if err := s.BindTopic(ctx, jid, 11, "new"); err != nil {
		t.Fatalf("BindTopic again: %v", err)
	}

	got, err := s.TopicForChat(ctx, jid)
	if err != nil {
		t.Fatalf("TopicForChat: %v", err)
	}
	if got.TopicID != 11 {
		t.Errorf("topic = %d, want 11", got.TopicID)
	}
	if _, err := s.ChatForTopic(ctx, 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("old topic still mapped: err = %v", err)
	}
}

func TestConcurrentBindsKeepOneRowPerChat(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	const jid = "6281234567890@s.whatsapp.net"
	var wg sync.WaitGroup
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func(topic int) {
			defer wg.Done()
			_ = s.BindTopic(ctx, jid, topic, "concurrent")
		}(i)
	}
	wg.Wait()

	n, err := s.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Errorf("Count = %d, want 1 (one row per chat)", n)
	}
}

func TestUpdateTitle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	const jid = "6281234567890@s.whatsapp.net"
	if err := s.BindTopic(ctx, jid, 7, "+6281234567890"); err != nil {
		t.Fatalf("BindTopic: %v", err)
	}
	if err := s.UpdateTitle(ctx, jid, "Budi"); err != nil {
		t.Fatalf("UpdateTitle: %v", err)
	}

	got, err := s.TopicForChat(ctx, jid)
	if err != nil {
		t.Fatalf("TopicForChat: %v", err)
	}
	if got.Title != "Budi" {
		t.Errorf("Title = %q, want %q", got.Title, "Budi")
	}
}
