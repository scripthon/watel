// Package wa wraps whatsmeow with just the surface the bridge needs:
// login, one callback per incoming private message, and outgoing send helpers.
package wa

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/mdp/qrterminal/v3"
	_ "modernc.org/sqlite" // pure-Go sqlite driver, no cgo
)

// Kind classifies an incoming message so the Telegram side can pick the right send method.
type Kind string

const (
	KindText     Kind = "text"
	KindImage    Kind = "image"
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindDocument Kind = "document"
	KindSticker  Kind = "sticker"
	KindNotice   Kind = "notice" // reactions, polls, calls, unsupported types: text-only summary
)

// Media carries downloaded bytes plus enough metadata for Telegram to render them.
type Media struct {
	Data     []byte
	MIME     string
	Filename string
	IsVoice  bool // WhatsApp PTT (push-to-talk) audio
	Duration int  // seconds, 0 if unknown
}

// Incoming is one private WhatsApp message, normalised for the bridge.
type Incoming struct {
	// ChatJID is the canonical key for the conversation (phone-number JID when known).
	ChatJID types.JID
	// Phone is the bare number, e.g. "6281234567890". Empty for LID-only contacts.
	Phone string
	// DisplayName is the best name known for the contact.
	DisplayName string
	// ID is the WhatsApp message id, used to send read receipts later.
	ID types.MessageID

	Kind      Kind
	Text      string // body or media caption
	Media     *Media
	Quoted    string // short excerpt of the replied-to message, if any
	FromMe    bool
	Timestamp time.Time
}

// Options configures the WhatsApp client.
type Options struct {
	SessionDB string
	// PairPhone, if set, uses code pairing instead of a QR code.
	PairPhone string
	// MirrorOwnMessages forwards messages you send from your own phone too.
	MirrorOwnMessages bool
	LogLevel          string
}

// Client is a connected WhatsApp account.
type Client struct {
	cli       *whatsmeow.Client
	container *sqlstore.Container
	log       waLog.Logger
	opts      Options

	mu      sync.RWMutex
	handler func(Incoming)
	closed  bool

	// queue hands events from whatsmeow's socket goroutine to a single worker,
	// so downloading media never stalls the connection while still delivering
	// messages in the order they arrived. It carries *events.Message and
	// *events.UndecryptableMessage values.
	queue chan any
	wg    sync.WaitGroup
}

// New opens the session database and builds a client. It does not connect yet.
func New(ctx context.Context, opts Options) (*Client, error) {
	if dir := filepath.Dir(opts.SessionDB); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create session dir: %w", err)
		}
	}

	logLevel := opts.LogLevel
	if logLevel == "" {
		logLevel = "INFO"
	}
	dbLog := waLog.Stdout("wa-db", logLevel, true)
	clientLog := waLog.Stdout("wa", logLevel, true)

	dsn := "file:" + opts.SessionDB + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	container, err := sqlstore.New(ctx, "sqlite", dsn, dbLog)
	if err != nil {
		return nil, fmt.Errorf("open whatsapp session store: %w", err)
	}

	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("load device: %w", err)
	}

	c := &Client{
		cli:       whatsmeow.NewClient(device, clientLog),
		container: container,
		log:       clientLog,
		opts:      opts,
		queue:     make(chan any, 512),
	}
	c.cli.AddEventHandler(c.handleEvent)

	c.wg.Add(1)
	go c.worker()

	return c, nil
}

// worker processes queued messages one at a time, preserving arrival order.
func (c *Client) worker() {
	defer c.wg.Done()
	for evt := range c.queue {
		c.process(evt)
	}
}

// OnMessage registers the callback invoked for every bridged private message.
func (c *Client) OnMessage(fn func(Incoming)) {
	c.mu.Lock()
	c.handler = fn
	c.mu.Unlock()
}

// Connect logs in (QR or pair code on first run) and starts receiving events.
func (c *Client) Connect(ctx context.Context) error {
	if c.cli.Store.ID != nil {
		if err := c.cli.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		return nil
	}

	qrChan, err := c.cli.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("qr channel: %w", err)
	}
	if err := c.cli.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	pairCodeRequested := false
	for item := range qrChan {
		switch item.Event {
		case "code":
			if c.opts.PairPhone == "" {
				fmt.Println("\nScan this QR code in WhatsApp > Linked devices > Link a device:")
				qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, os.Stdout)
				fmt.Printf("(code expires in %s)\n\n", item.Timeout.Round(time.Second))
				continue
			}
			// Code pairing still emits QR events; request the pairing code once
			// the socket is up and ignore the rest.
			if pairCodeRequested {
				continue
			}
			pairCodeRequested = true
			code, err := c.cli.PairPhone(ctx, c.opts.PairPhone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
			if err != nil {
				return fmt.Errorf("request pair code: %w", err)
			}
			fmt.Printf("\nEnter this code in WhatsApp > Linked devices > Link with phone number:\n\n    %s\n\n", code)
		case "success":
			c.log.Infof("Logged in to WhatsApp")
			return nil
		case "timeout":
			return fmt.Errorf("login timed out, run the bridge again")
		case "error":
			return fmt.Errorf("login failed: %w", item.Error)
		default:
			c.log.Debugf("QR channel event: %s", item.Event)
		}
	}
	if c.cli.Store.ID == nil {
		return fmt.Errorf("login did not complete")
	}
	return nil
}

// Disconnect closes the WhatsApp socket, drains queued messages and releases
// the session database.
func (c *Client) Disconnect() {
	c.cli.Disconnect()

	c.mu.Lock()
	alreadyClosed := c.closed
	c.closed = true
	c.mu.Unlock()
	if alreadyClosed {
		return
	}

	close(c.queue)
	c.wg.Wait()

	if err := c.container.Close(); err != nil {
		c.log.Warnf("Close session store: %v", err)
	}
}

// IsLoggedIn reports whether the socket is up and authenticated.
func (c *Client) IsLoggedIn() bool { return c.cli.IsLoggedIn() }

// OwnJID returns the logged-in account's JID, or the zero JID before login.
func (c *Client) OwnJID() types.JID {
	if c.cli.Store.ID == nil {
		return types.EmptyJID
	}
	return *c.cli.Store.ID
}

func (c *Client) handleEvent(rawEvt any) {
	switch evt := rawEvt.(type) {
	case *events.Message:
		c.handleMessage(evt)
	case *events.UndecryptableMessage:
		c.handleUndecryptable(evt)
	case *events.Connected:
		c.log.Infof("Connected to WhatsApp")
	case *events.LoggedOut:
		c.log.Errorf("Logged out of WhatsApp (reason: %s). Delete the session db and pair again.", evt.Reason)
	case *events.StreamReplaced:
		c.log.Errorf("Stream replaced: another WhatsApp Web session took over")
	}
}

// handleMessage runs on whatsmeow's socket goroutine, so it only filters and
// enqueues; the real work happens in worker.
func (c *Client) handleMessage(evt *events.Message) {
	if !c.isBridgeablePrivateChat(evt.Info.MessageSource) {
		return
	}
	if evt.Info.IsFromMe && !c.opts.MirrorOwnMessages {
		return
	}
	c.enqueue(evt, evt.Info.ID, evt.Info.Chat)
}

// handleUndecryptable reports messages WhatsApp refuses to hand to linked
// devices at all. View-once media is the common case: the server sends an
// empty placeholder, whatsmeow asks the phone for the content, and the phone
// declines. Nothing can recover the body, so the least bad outcome is a note
// in the topic instead of silence.
//
// Only *intentionally* unavailable messages are reported. A plain decryption
// failure is transient: whatsmeow re-requests it and the real message usually
// arrives moments later, so announcing it would just duplicate the message.
func (c *Client) handleUndecryptable(evt *events.UndecryptableMessage) {
	if !evt.IsUnavailable || evt.UnavailableType == "" {
		return
	}
	if !c.isBridgeablePrivateChat(evt.Info.MessageSource) {
		return
	}
	if evt.Info.IsFromMe && !c.opts.MirrorOwnMessages {
		return
	}
	c.enqueue(evt, evt.Info.ID, evt.Info.Chat)
}

// enqueue hands an event to the worker without blocking the socket goroutine.
func (c *Client) enqueue(evt any, id types.MessageID, chat types.JID) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed || c.handler == nil {
		return
	}
	select {
	case c.queue <- evt:
	default:
		c.log.Warnf("Message queue is full, dropping message %s from %s", id, chat)
	}
}

// process turns a queued event into an Incoming and hands it to the bridge.
func (c *Client) process(evt any) {
	c.mu.RLock()
	handler := c.handler
	c.mu.RUnlock()
	if handler == nil {
		return
	}

	// Downloads and contact lookups need a context; the event handler has none.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var msg *Incoming
	switch e := evt.(type) {
	case *events.Message:
		msg = c.extract(ctx, e)
	case *events.UndecryptableMessage:
		msg = c.describeUnavailable(ctx, e)
	}
	if msg == nil {
		return
	}
	handler(*msg)
}

// describeUnavailable builds a placeholder for content WhatsApp withheld.
func (c *Client) describeUnavailable(ctx context.Context, evt *events.UndecryptableMessage) *Incoming {
	chat := c.canonicalJID(ctx, evt.Info.Chat)

	text := "🔒 A message could not be delivered to this linked device. Open WhatsApp on your phone to see it."
	if evt.UnavailableType == events.UnavailableTypeViewOnce {
		text = "👁 View-once message. WhatsApp does not send these to linked devices, so open it on your phone."
	}

	// The push name on our own messages is ours, not the contact's.
	pushName := evt.Info.PushName
	if evt.Info.IsFromMe {
		pushName = ""
	}

	return &Incoming{
		ChatJID:     chat,
		ID:          evt.Info.ID,
		Phone:       phoneOf(chat),
		DisplayName: c.DisplayName(ctx, chat, pushName),
		Kind:        KindNotice,
		Text:        text,
		FromMe:      evt.Info.IsFromMe,
		Timestamp:   evt.Info.Timestamp,
	}
}

// isBridgeablePrivateChat filters out groups, broadcasts, status updates,
// newsletters and bot chats. Only one-to-one conversations are bridged.
func (c *Client) isBridgeablePrivateChat(src types.MessageSource) bool {
	chat := src.Chat
	if src.IsGroup || chat.IsBroadcastList() || chat.IsBot() {
		return false
	}
	switch chat.Server {
	case types.DefaultUserServer, types.LegacyUserServer, types.HiddenUserServer:
		return true
	default:
		return false
	}
}

// canonicalJID resolves a chat JID to the phone-number form when possible so
// that LID and PN addressing of the same contact map to one topic.
func (c *Client) canonicalJID(ctx context.Context, jid types.JID) types.JID {
	jid = jid.ToNonAD()
	if jid.Server != types.HiddenUserServer {
		if jid.Server == types.LegacyUserServer {
			jid.Server = types.DefaultUserServer
		}
		return jid
	}
	// The LID map only exists once the device has been paired.
	if c.cli.Store.LIDs == nil {
		return jid
	}
	if pn, err := c.cli.Store.GetAltJID(ctx, jid); err == nil && !pn.IsEmpty() {
		return pn.ToNonAD()
	}
	return jid
}

// DisplayName picks the friendliest name available for a contact.
func (c *Client) DisplayName(ctx context.Context, jid types.JID, pushName string) string {
	// Contacts is only populated after pairing, so this can be nil on a fresh device.
	if c.cli.Store.Contacts != nil {
		if info, err := c.cli.Store.Contacts.GetContact(ctx, jid); err == nil && info.Found {
			for _, candidate := range []string{info.FullName, info.BusinessName, info.FirstName, info.PushName} {
				if candidate = strings.TrimSpace(candidate); candidate != "" {
					return candidate
				}
			}
		}
	}
	if pushName = strings.TrimSpace(pushName); pushName != "" {
		return pushName
	}
	if jid.Server == types.DefaultUserServer {
		return "+" + jid.User
	}
	return jid.User
}

// ResolveNumber turns a user-typed phone number into a JID, verifying that the
// number actually has a WhatsApp account.
func (c *Client) ResolveNumber(ctx context.Context, number string) (types.JID, error) {
	cleaned := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, number)
	if len(cleaned) < 7 {
		return types.EmptyJID, fmt.Errorf("%q does not look like an international phone number", number)
	}

	resp, err := c.cli.IsOnWhatsApp(ctx, []string{"+" + cleaned})
	if err != nil {
		return types.EmptyJID, fmt.Errorf("check number: %w", err)
	}
	if len(resp) == 0 || !resp[0].IsIn {
		return types.EmptyJID, fmt.Errorf("+%s is not on WhatsApp", cleaned)
	}
	return resp[0].JID.ToNonAD(), nil
}

// Store exposes the underlying device store for callers that need contact data.
func (c *Client) Store() *store.Device { return c.cli.Store }
