package config

import "testing"

func TestLoadUsesDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:password@localhost:5432/hikari?sslmode=disable")
	t.Setenv("APP_ENV", "")
	t.Setenv("HTTP_ADDRESS", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("TELEGRAM_INIT_DATA_MAX_AGE", "")
	t.Setenv("ADMIN_TELEGRAM_IDS", "123, 456")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Environment != "development" {
		t.Errorf("Environment = %q, want development", cfg.Environment)
	}
	if cfg.HTTPAddress != ":8080" {
		t.Errorf("HTTPAddress = %q, want :8080", cfg.HTTPAddress)
	}
	if cfg.ShutdownTimeout.String() != "10s" {
		t.Errorf("ShutdownTimeout = %s, want 10s", cfg.ShutdownTimeout)
	}
	if cfg.TelegramInitDataMaxAge.String() != "1h0m0s" {
		t.Errorf("TelegramInitDataMaxAge = %s, want 1h", cfg.TelegramInitDataMaxAge)
	}
	if _, ok := cfg.AdminTelegramIDs[123]; !ok {
		t.Error("AdminTelegramIDs does not contain 123")
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	t.Run("missing database URL", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		if _, err := Load(); err == nil {
			t.Fatal("Load() error = nil, want an error")
		}
	})

	t.Run("invalid shutdown timeout", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://user:password@localhost:5432/hikari")
		t.Setenv("SHUTDOWN_TIMEOUT", "never")
		if _, err := Load(); err == nil {
			t.Fatal("Load() error = nil, want an error")
		}
	})

	t.Run("missing Telegram bot token", func(t *testing.T) {
		setRequiredEnvironment(t)
		t.Setenv("TELEGRAM_BOT_TOKEN", "")
		if _, err := Load(); err == nil {
			t.Fatal("Load() error = nil, want an error")
		}
	})

	t.Run("invalid admin Telegram ID", func(t *testing.T) {
		setRequiredEnvironment(t)
		t.Setenv("ADMIN_TELEGRAM_IDS", "admin")
		if _, err := Load(); err == nil {
			t.Fatal("Load() error = nil, want an error")
		}
	})
}

func setRequiredEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://user:password@localhost:5432/hikari")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("ADMIN_TELEGRAM_IDS", "123")
}
