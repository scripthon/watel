// Package store persists the WhatsApp chat <-> Telegram topic mapping.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go sqlite driver, no cgo
)

// ErrNotFound is returned when a lookup has no matching row.
var ErrNotFound = errors.New("store: not found")

// Store is the bridge's own database. It is separate from the whatsmeow
// session database so that re-pairing WhatsApp never wipes the topic mapping.
type Store struct {
	db *sql.DB
}

// Mapping ties one WhatsApp chat to one Telegram forum topic.
type Mapping struct {
	ChatJID   string
	TopicID   int
	Title     string
	CreatedAt time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS chat_topics (
	chat_jid   TEXT    NOT NULL PRIMARY KEY,
	topic_id   INTEGER NOT NULL,
	title      TEXT    NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_topics_topic ON chat_topics(topic_id);
`

// Open creates (or opens) the bridge database at path and applies the schema.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open bridge db: %w", err)
	}
	// SQLite handles one writer at a time; serialising here avoids spurious
	// "database is locked" errors when WhatsApp and Telegram events overlap.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// TopicForChat returns the topic bound to chatJID, or ErrNotFound.
func (s *Store) TopicForChat(ctx context.Context, chatJID string) (Mapping, error) {
	return s.queryOne(ctx, `SELECT chat_jid, topic_id, title, created_at FROM chat_topics WHERE chat_jid = ?`, chatJID)
}

// ChatForTopic returns the chat bound to topicID, or ErrNotFound.
func (s *Store) ChatForTopic(ctx context.Context, topicID int) (Mapping, error) {
	return s.queryOne(ctx, `SELECT chat_jid, topic_id, title, created_at FROM chat_topics WHERE topic_id = ?`, topicID)
}

func (s *Store) queryOne(ctx context.Context, query string, arg any) (Mapping, error) {
	var m Mapping
	var created int64
	err := s.db.QueryRowContext(ctx, query, arg).Scan(&m.ChatJID, &m.TopicID, &m.Title, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Mapping{}, ErrNotFound
	}
	if err != nil {
		return Mapping{}, err
	}
	m.CreatedAt = time.Unix(created, 0)
	return m, nil
}

// BindTopic records that chatJID is served by topicID. An existing row for the
// same chat is replaced, which is what happens when a topic is deleted in
// Telegram and the bridge has to create a fresh one.
func (s *Store) BindTopic(ctx context.Context, chatJID string, topicID int, title string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO chat_topics (chat_jid, topic_id, title, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(chat_jid) DO UPDATE SET topic_id = excluded.topic_id, title = excluded.title`,
		chatJID, topicID, title, time.Now().Unix())
	return err
}

// UnbindChat drops the mapping for chatJID, forcing a new topic next time.
func (s *Store) UnbindChat(ctx context.Context, chatJID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chat_topics WHERE chat_jid = ?`, chatJID)
	return err
}

// UpdateTitle stores the latest human-readable title for a chat.
func (s *Store) UpdateTitle(ctx context.Context, chatJID, title string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chat_topics SET title = ? WHERE chat_jid = ?`, title, chatJID)
	return err
}

// Count returns how many chats are currently bridged.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_topics`).Scan(&n)
	return n, err
}
