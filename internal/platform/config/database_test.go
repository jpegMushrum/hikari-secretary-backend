package config

import "testing"

func TestDatabaseToolsIgnoreApplicationSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:password@database:5432/hikari?sslmode=disable")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("BOT_SERVICE_TOKEN", "")
	t.Setenv("ADMIN_TELEGRAM_IDS", "invalid")
	t.Setenv("DEEPSEEK_API_KEY", "enabled-but-not-needed-for-migrations")
	t.Setenv("AI_TOPIC_TARGET_KEY", "test")
	t.Setenv("TARGETS_PATH", "/nonexistent/targets.json")
	t.Setenv("SHUTDOWN_TIMEOUT", "invalid")
	got, err := LoadDatabaseURL()
	if err != nil || got != "postgres://user:password@database:5432/hikari?sslmode=disable" {
		t.Fatalf("database configuration: %q %v", got, err)
	}
}

func TestDatabaseToolsRejectMissingOrInvalidURL(t *testing.T) {
	for _, value := range []string{"", "http://database/hikari", "postgres:///hikari", "postgres://user:%xx@host/db"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("DATABASE_URL", value)
			if _, err := LoadDatabaseURL(); err == nil {
				t.Fatal("expected invalid DATABASE_URL")
			}
		})
	}
}
