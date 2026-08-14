// Package config loads runtime configuration from the environment.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds every knob the bridge needs to run.
type Config struct {
	// TelegramToken is the bot token from @BotFather.
	TelegramToken string
	// TelegramChatID is the supergroup (with Topics enabled) that hosts one topic per contact.
	TelegramChatID int64
	// TelegramOwnerID, when non-zero, restricts who may send messages through the bridge.
	TelegramOwnerID int64

	// BridgeDB stores the chat<->topic mapping.
	BridgeDB string
	// SessionDB stores the whatsmeow device session.
	SessionDB string

	// PairPhone, when set, logs in with an 8-digit pair code instead of a QR code.
	// Format: full international number without +, e.g. 6281234567890.
	PairPhone string

	// MirrorOwnMessages also forwards messages you send from your own phone.
	MirrorOwnMessages bool

	// LogLevel is passed to whatsmeow's logger: DEBUG, INFO, WARN, ERROR.
	LogLevel string
}

// Load reads configuration from .env (if present) and then the process environment.
// Real environment variables win over .env entries.
func Load(envFile string) (*Config, error) {
	loadDotEnv(envFile)

	cfg := &Config{
		TelegramToken:     os.Getenv("TELEGRAM_BOT_TOKEN"),
		BridgeDB:          envOr("BRIDGE_DB", "data/bridge.db"),
		SessionDB:         envOr("SESSION_DB", "data/whatsapp.db"),
		PairPhone:         strings.TrimPrefix(strings.TrimSpace(os.Getenv("WA_PAIR_PHONE")), "+"),
		MirrorOwnMessages: envBool("MIRROR_OWN_MESSAGES", true),
		LogLevel:          envOr("LOG_LEVEL", "INFO"),
	}

	if cfg.TelegramToken == "" {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}

	raw := strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID"))
	if raw == "" {
		return nil, fmt.Errorf("TELEGRAM_CHAT_ID is required (the supergroup id, usually negative)")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("TELEGRAM_CHAT_ID %q is not a number: %w", raw, err)
	}
	cfg.TelegramChatID = id

	if raw := strings.TrimSpace(os.Getenv("TELEGRAM_OWNER_ID")); raw != "" {
		owner, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("TELEGRAM_OWNER_ID %q is not a number: %w", raw, err)
		}
		cfg.TelegramOwnerID = owner
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return parsed
}

// loadDotEnv sets any KEY=VALUE pairs from path that are not already in the environment.
// A missing file is not an error: the bridge is expected to run from plain env vars too.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	// A truncated env file is not worth failing the whole startup over; the
	// required-value checks in Load will report anything actually missing.
	_ = scanner.Err()
}
