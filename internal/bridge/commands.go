package bridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/thxrhmn/tewas/internal/store"
	"github.com/thxrhmn/tewas/internal/tg"
)

const helpText = `WhatsApp ↔ Telegram bridge

Every WhatsApp contact gets its own topic in this group. Reply inside a topic
and the message goes back to that contact on WhatsApp.

Commands (work anywhere in this group):
/new <number>  start a chat with a number that has no topic yet, e.g. /new 6281234567890
/whois         show which WhatsApp chat the current topic is bound to
/status        connection state and number of bridged chats
/help          this message`

func (b *Bridge) handleCommand(ctx context.Context, reply tg.Reply) {
	switch reply.Command {
	case "start", "help":
		b.notify(ctx, reply.TopicID, helpText)
	case "status":
		b.notify(ctx, reply.TopicID, b.statusText(ctx))
	case "whois":
		b.notify(ctx, reply.TopicID, b.whoisText(ctx, reply.TopicID))
	case "new":
		b.startNewChat(ctx, reply)
	default:
		b.notify(ctx, reply.TopicID, "Unknown command. Try /help.")
	}
}

func (b *Bridge) statusText(ctx context.Context) string {
	var lines []string
	if b.wa.IsLoggedIn() {
		lines = append(lines, "WhatsApp: connected as "+b.wa.OwnJID().User)
	} else {
		lines = append(lines, "WhatsApp: not connected")
	}
	if n, err := b.store.Count(ctx); err == nil {
		lines = append(lines, fmt.Sprintf("Bridged chats: %d", n))
	}
	return strings.Join(lines, "\n")
}

func (b *Bridge) whoisText(ctx context.Context, topicID int) string {
	if topicID == 0 {
		return "This is the General tab, not a contact topic."
	}
	mapping, err := b.store.ChatForTopic(ctx, topicID)
	if errors.Is(err, store.ErrNotFound) {
		return "This topic is not linked to any WhatsApp chat."
	}
	if err != nil {
		return "Could not read the mapping: " + err.Error()
	}
	return fmt.Sprintf("Linked to %s\nTitle: %s\nSince: %s",
		mapping.ChatJID, mapping.Title, mapping.CreatedAt.Format("2006-01-02 15:04"))
}

// startNewChat opens a topic for a number that has not written first.
func (b *Bridge) startNewChat(ctx context.Context, reply tg.Reply) {
	number := strings.TrimSpace(reply.Args)
	if number == "" {
		b.notify(ctx, reply.TopicID, "Usage: /new <number in international format>, e.g. /new 6281234567890")
		return
	}

	jid, err := b.wa.ResolveNumber(ctx, number)
	if err != nil {
		b.notify(ctx, reply.TopicID, "⚠️ "+err.Error())
		return
	}

	key := jid.String()
	if mapping, err := b.store.TopicForChat(ctx, key); err == nil {
		b.notify(ctx, reply.TopicID, fmt.Sprintf("That contact already has a topic (id %d).", mapping.TopicID))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		log.Printf("bridge: look up %s: %v", key, err)
	}

	name := b.wa.DisplayName(ctx, jid, "")
	if _, err := b.topicFor(ctx, jid, name); err != nil {
		b.notify(ctx, reply.TopicID, "⚠️ Could not create the topic: "+err.Error())
		return
	}
	b.notify(ctx, reply.TopicID, fmt.Sprintf("✅ Topic created for %s. Open it and send your first message.", topicTitle(name, jid)))
}
