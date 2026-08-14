package config

import (
	"os"
	"path/filepath"
	"testing"
)

// clearEnv removes every variable Load reads so tests do not leak into each other.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "TELEGRAM_OWNER_ID",
		"BRIDGE_DB", "SESSION_DB", "WA_PAIR_PHONE", "MIRROR_OWN_MESSAGES", "LOG_LEVEL",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func TestLoadRequiresTokenAndChatID(t *testing.T) {
	clearEnv(t)
	missing := filepath.Join(t.TempDir(), "absent.env")

	if _, err := Load(missing); err == nil {
		t.Fatal("Load without a token should fail")
	}

	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	if _, err := Load(missing); err == nil {
		t.Fatal("Load without a chat id should fail")
	}

	t.Setenv("TELEGRAM_CHAT_ID", "not-a-number")
	if _, err := Load(missing); err == nil {
		t.Fatal("Load with a non-numeric chat id should fail")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("TELEGRAM_CHAT_ID", "-1001234567890")

	cfg, err := Load(filepath.Join(t.TempDir(), "absent.env"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.TelegramChatID != -1001234567890 {
		t.Errorf("TelegramChatID = %d, want -1001234567890", cfg.TelegramChatID)
	}
	if cfg.BridgeDB != "data/bridge.db" || cfg.SessionDB != "data/whatsapp.db" {
		t.Errorf("unexpected default db paths: %q, %q", cfg.BridgeDB, cfg.SessionDB)
	}
	if !cfg.MirrorOwnMessages {
		t.Error("MirrorOwnMessages should default to true")
	}
	if cfg.LogLevel != "INFO" {
		t.Errorf("LogLevel = %q, want INFO", cfg.LogLevel)
	}
}

func TestLoadReadsDotEnvAndStripsPlus(t *testing.T) {
	clearEnv(t)
	envPath := filepath.Join(t.TempDir(), ".env")
	contents := "# comment\n" +
		"TELEGRAM_BOT_TOKEN=\"123:abc\"\n" +
		"TELEGRAM_CHAT_ID=-1001234567890\n" +
		"export WA_PAIR_PHONE=+6281234567890\n" +
		"MIRROR_OWN_MESSAGES=false\n"
	if err := os.WriteFile(envPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	cfg, err := Load(envPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.TelegramToken != "123:abc" {
		t.Errorf("TelegramToken = %q, want quotes stripped", cfg.TelegramToken)
	}
	if cfg.PairPhone != "6281234567890" {
		t.Errorf("PairPhone = %q, want the leading + removed", cfg.PairPhone)
	}
	if cfg.MirrorOwnMessages {
		t.Error("MIRROR_OWN_MESSAGES=false should disable mirroring")
	}
}

func TestRealEnvBeatsDotEnv(t *testing.T) {
	clearEnv(t)
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("TELEGRAM_CHAT_ID=-100111\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("TELEGRAM_CHAT_ID", "-100222")

	cfg, err := Load(envPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TelegramChatID != -100222 {
		t.Errorf("TelegramChatID = %d, want the process environment to win", cfg.TelegramChatID)
	}
}
