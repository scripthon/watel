// Command tewas bridges one WhatsApp account to one Telegram supergroup,
// giving every WhatsApp contact its own forum topic.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/thxrhmn/tewas/internal/bridge"
	"github.com/thxrhmn/tewas/internal/config"
	"github.com/thxrhmn/tewas/internal/store"
	"github.com/thxrhmn/tewas/internal/tg"
	"github.com/thxrhmn/tewas/internal/wa"
)

func main() {
	envFile := flag.String("env", ".env", "path to the env file")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("tewas ")

	if err := run(*envFile); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run(envFile string) error {
	cfg, err := config.Load(envFile)
	if err != nil {
		return err
	}

	// Ctrl-C and SIGTERM shut everything down in order.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(cfg.BridgeDB)
	if err != nil {
		return err
	}
	defer db.Close()

	// The bridge needs both clients, but the Telegram client needs a handler
	// that lives on the bridge, so the handler closes over a variable filled
	// in below. It is only ever called once polling starts.
	var br *bridge.Bridge
	tgClient, err := tg.New(tg.Options{
		Token:   cfg.TelegramToken,
		ChatID:  cfg.TelegramChatID,
		OwnerID: cfg.TelegramOwnerID,
	}, func(ctx context.Context, reply tg.Reply) {
		br.HandleTelegram(ctx, reply)
	})
	if err != nil {
		return err
	}

	// Check the Telegram setup before touching WhatsApp: a wrong token or a
	// group without Topics should not leave a half-created session behind.
	me, err := tgClient.Me(ctx)
	if err != nil {
		return err
	}
	log.Printf("telegram bot: @%s", me.Username)

	if err := tgClient.VerifyGroup(ctx); err != nil {
		return err
	}

	waClient, err := wa.New(ctx, wa.Options{
		SessionDB:         cfg.SessionDB,
		PairPhone:         cfg.PairPhone,
		MirrorOwnMessages: cfg.MirrorOwnMessages,
		LogLevel:          cfg.LogLevel,
	})
	if err != nil {
		return err
	}
	defer waClient.Disconnect()

	br = bridge.New(waClient, tgClient, db)
	waClient.OnMessage(br.HandleWhatsApp)

	if err := waClient.Connect(ctx); err != nil {
		return err
	}
	log.Printf("bridge is running; every WhatsApp contact gets its own topic")

	// Blocks until the context is cancelled by a signal.
	tgClient.Start(ctx)
	log.Printf("shutting down")
	return nil
}
