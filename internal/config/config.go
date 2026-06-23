// Package config loads and validates configuration from the environment (.env).
package config

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config is the validated application configuration.
type Config struct {
	TelegramToken      string
	OwnerIDs           []int64
	DBPath             string
	EncryptionKey      string // base64, validated to decode to 32 bytes
	DefaultIntervalSec int    // mipped default
	LolzIntervalSec    int    // lolz default
	JitterSec          int
	StatsPollSec       int
	RequestDelayMS     int
	LogLevel           slog.Level
}

// Load reads .env (if present) and the process environment, then validates.
func Load() (*Config, error) {
	// Best-effort: a missing .env is fine if the env is already populated.
	_ = godotenv.Load()

	cfg := &Config{
		TelegramToken:      os.Getenv("TELEGRAM_BOT_TOKEN"),
		DBPath:             envDefault("DB_PATH", "./bot.db"),
		EncryptionKey:      os.Getenv("ENCRYPTION_KEY"),
		DefaultIntervalSec: envInt("DEFAULT_INTERVAL_SEC", 86400),
		LolzIntervalSec:    envInt("LOLZ_INTERVAL_SEC", 14400),
		JitterSec:          envInt("JITTER_SEC", 1800),
		StatsPollSec:       envInt("STATS_POLL_SEC", 3600),
		RequestDelayMS:     envInt("REQUEST_DELAY_MS", 3000),
		LogLevel:           parseLevel(envDefault("LOG_LEVEL", "info")),
	}

	if cfg.TelegramToken == "" {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	owners, err := parseOwnerIDs(os.Getenv("OWNER_IDS"))
	if err != nil {
		return nil, err
	}
	if len(owners) == 0 {
		return nil, fmt.Errorf("OWNER_IDS is required (comma-separated Telegram IDs)")
	}
	cfg.OwnerIDs = owners

	if cfg.EncryptionKey == "" {
		return nil, fmt.Errorf("ENCRYPTION_KEY is required (32 bytes, base64)")
	}
	if key, err := base64.StdEncoding.DecodeString(cfg.EncryptionKey); err != nil || len(key) != 32 {
		return nil, fmt.Errorf("ENCRYPTION_KEY must be base64 of exactly 32 bytes")
	}

	return cfg, nil
}

// IsOwner reports whether id is in the whitelist.
func (c *Config) IsOwner(id int64) bool {
	for _, o := range c.OwnerIDs {
		if o == id {
			return true
		}
	}
	return false
}

func parseOwnerIDs(raw string) ([]int64, error) {
	var ids []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid OWNER_IDS entry %q: %w", part, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
