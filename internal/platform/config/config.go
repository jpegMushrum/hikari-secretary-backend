package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment            string
	HTTPAddress            string
	DatabaseURL            string
	ShutdownTimeout        time.Duration
	TelegramBotToken       string
	TelegramInitDataMaxAge time.Duration
	AdminTelegramIDs       map[int64]struct{}
}

func Load() (Config, error) {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	parsedURL, err := url.Parse(databaseURL)
	if err != nil || (parsedURL.Scheme != "postgres" && parsedURL.Scheme != "postgresql") || parsedURL.Host == "" {
		return Config{}, fmt.Errorf("DATABASE_URL must be a valid PostgreSQL connection URL")
	}

	shutdownTimeout, err := time.ParseDuration(valueOrDefault("SHUTDOWN_TIMEOUT", "10s"))
	if err != nil || shutdownTimeout <= 0 {
		return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT must be a positive duration")
	}

	telegramBotToken := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if telegramBotToken == "" {
		return Config{}, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}

	initDataMaxAge, err := time.ParseDuration(valueOrDefault("TELEGRAM_INIT_DATA_MAX_AGE", "1h"))
	if err != nil || initDataMaxAge <= 0 {
		return Config{}, fmt.Errorf("TELEGRAM_INIT_DATA_MAX_AGE must be a positive duration")
	}

	adminTelegramIDs, err := parseTelegramIDs(os.Getenv("ADMIN_TELEGRAM_IDS"))
	if err != nil {
		return Config{}, err
	}

	return Config{
		Environment:            valueOrDefault("APP_ENV", "development"),
		HTTPAddress:            valueOrDefault("HTTP_ADDRESS", ":8080"),
		DatabaseURL:            databaseURL,
		ShutdownTimeout:        shutdownTimeout,
		TelegramBotToken:       telegramBotToken,
		TelegramInitDataMaxAge: initDataMaxAge,
		AdminTelegramIDs:       adminTelegramIDs,
	}, nil
}

func parseTelegramIDs(raw string) (map[int64]struct{}, error) {
	ids := make(map[int64]struct{})
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}

		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("ADMIN_TELEGRAM_IDS must contain positive numeric Telegram IDs")
		}
		ids[id] = struct{}{}
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("ADMIN_TELEGRAM_IDS must contain at least one Telegram ID")
	}

	return ids, nil
}

func valueOrDefault(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}
