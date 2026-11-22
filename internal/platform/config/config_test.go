package config

import "testing"

import "os"

func TestLoadUsesDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:password@localhost:5432/hikari?sslmode=disable")
	t.Setenv("APP_ENV", "")
	t.Setenv("HTTP_ADDRESS", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("TELEGRAM_INIT_DATA_MAX_AGE", "")
	t.Setenv("ADMIN_TELEGRAM_IDS", "123, 456")
	t.Setenv("BOT_SERVICE_TOKEN", "test-service-token")

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

	t.Run("missing bot service token", func(t *testing.T) {
		setRequiredEnvironment(t)
		t.Setenv("BOT_SERVICE_TOKEN", "")
		if _, err := Load(); err == nil {
			t.Fatal("Load() error = nil, want an error")
		}
	})
}

func TestLoadAcceptsLegacyAdminIDsAlias(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("ADMIN_TELEGRAM_IDS", "")
	t.Setenv("ADMIN_IDS", "789")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.AdminTelegramIDs[789]; !ok {
		t.Error("AdminTelegramIDs does not contain legacy ADMIN_IDS value")
	}
}

func TestReminderListAcceptsEmptySeparators(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("REMINDER_OPTIONS_MINUTES", " 30, ,60,")
	cfg, err := Load()
	if err != nil || len(cfg.ReminderOptions) != 2 || cfg.ReminderOptions[0] != 30 || cfg.ReminderOptions[1] != 60 {
		t.Fatalf("unexpected reminder options %v, err %v", cfg.ReminderOptions, err)
	}
	t.Setenv("REMINDER_OPTIONS_MINUTES", ", ,")
	if _, err := Load(); err == nil {
		t.Fatal("accepted empty reminder list")
	}
}

func TestLoadConfiguresAIFromTargetFile(t *testing.T) {
	setRequiredEnvironment(t)
	file, err := os.CreateTemp(t.TempDir(), "targets-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString(`{"telegram":[{"key":"club","name":"Club","chat_id":-100123,"message_thread_id":7}]}`); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY", "secret")
	t.Setenv("AI_TOPIC_TARGET_KEY", "club")
	t.Setenv("AI_TOPIC_AUTO_PUBLISH_TIME", "12:30")
	t.Setenv("TARGETS_PATH", file.Name())
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AI.Enabled || cfg.AI.Target.Destination != "-100123" || cfg.AI.Target.MessageThreadID == nil || *cfg.AI.Target.MessageThreadID != 7 {
		t.Fatalf("unexpected AI config: %+v", cfg.AI)
	}
}

func setRequiredEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://user:password@localhost:5432/hikari")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("ADMIN_TELEGRAM_IDS", "123")
	t.Setenv("BOT_SERVICE_TOKEN", "test-service-token")
}
