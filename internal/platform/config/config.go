package config

import (
	"encoding/json"
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
	BotServiceToken        string
	AI                     AIConfig
	ReminderOptions        []int
}

type Target struct {
	Key             string
	Name            string
	Destination     string
	MessageThreadID *int64
}
type AIConfig struct {
	Enabled                                                   bool
	APIKey, Model, BaseURL, PublishTime, Timezone             string
	Timeout                                                   time.Duration
	QuestionCount, VocabularyCount, GrammarCount, MaxAttempts int
	Target                                                    Target
}

// LoadDatabaseURL loads only the connection settings needed by database tools.
func LoadDatabaseURL() (string, error) {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return "", fmt.Errorf("DATABASE_URL is required")
	}
	parsedURL, err := url.Parse(databaseURL)
	if err != nil || (parsedURL.Scheme != "postgres" && parsedURL.Scheme != "postgresql") || parsedURL.Host == "" {
		return "", fmt.Errorf("DATABASE_URL must be a valid PostgreSQL connection URL")
	}
	return databaseURL, nil
}

func Load() (Config, error) {
	databaseURL, err := LoadDatabaseURL()
	if err != nil {
		return Config{}, err
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

	adminTelegramIDsRaw := strings.TrimSpace(os.Getenv("ADMIN_TELEGRAM_IDS"))
	if adminTelegramIDsRaw == "" {
		// ADMIN_IDS was used by the Telegram bot before the backend was split
		// into a separate service. Keep it as a compatibility alias so an
		// existing deployment can migrate without an avoidable startup outage.
		adminTelegramIDsRaw = os.Getenv("ADMIN_IDS")
	}
	adminTelegramIDs, err := parseTelegramIDs(adminTelegramIDsRaw)
	if err != nil {
		return Config{}, err
	}

	botServiceToken := strings.TrimSpace(os.Getenv("BOT_SERVICE_TOKEN"))
	if botServiceToken == "" {
		return Config{}, fmt.Errorf("BOT_SERVICE_TOKEN is required")
	}

	ai, err := loadAIConfig()
	if err != nil {
		return Config{}, err
	}

	var options []int
	seen := map[int]bool{}
	for _, raw := range strings.Split(valueOrDefault("REMINDER_OPTIONS_MINUTES", "30,60,120,360,1440"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 2147483647 {
			return Config{}, fmt.Errorf("invalid REMINDER_OPTIONS_MINUTES")
		}
		if !seen[n] {
			options = append(options, n)
			seen[n] = true
		}
	}
	if len(options) == 0 {
		return Config{}, fmt.Errorf("REMINDER_OPTIONS_MINUTES must include at least one reminder")
	}
	return Config{
		ReminderOptions:        options,
		Environment:            valueOrDefault("APP_ENV", "development"),
		HTTPAddress:            valueOrDefault("HTTP_ADDRESS", ":8080"),
		DatabaseURL:            databaseURL,
		ShutdownTimeout:        shutdownTimeout,
		TelegramBotToken:       telegramBotToken,
		TelegramInitDataMaxAge: initDataMaxAge,
		AdminTelegramIDs:       adminTelegramIDs,
		BotServiceToken:        botServiceToken,
		AI:                     ai,
	}, nil
}

func loadAIConfig() (AIConfig, error) {
	key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	targetKey := strings.TrimSpace(os.Getenv("AI_TOPIC_TARGET_KEY"))
	publish := strings.TrimSpace(os.Getenv("AI_TOPIC_AUTO_PUBLISH_TIME"))
	if key == "" {
		return AIConfig{Timezone: valueOrDefault("TIMEZONE", "Europe/Moscow")}, nil
	}
	if targetKey == "" {
		return AIConfig{}, fmt.Errorf("AI_TOPIC_TARGET_KEY is required when DeepSeek is enabled")
	}
	if publish != "" {
		if _, err := time.Parse("15:04", publish); err != nil {
			return AIConfig{}, fmt.Errorf("AI_TOPIC_AUTO_PUBLISH_TIME must use HH:MM")
		}
	}
	timeoutSeconds, err := strconv.Atoi(valueOrDefault("DEEPSEEK_TIMEOUT_SECONDS", "90"))
	if err != nil || timeoutSeconds <= 0 {
		return AIConfig{}, fmt.Errorf("DEEPSEEK_TIMEOUT_SECONDS must be positive")
	}
	timeout := time.Duration(timeoutSeconds) * time.Second
	bounded := func(name string, fallback, maximum int) (int, error) {
		value, err := strconv.Atoi(valueOrDefault(name, strconv.Itoa(fallback)))
		if err != nil || value < 1 || value > maximum {
			return 0, fmt.Errorf("%s must be between 1 and %d", name, maximum)
		}
		return value, nil
	}
	questions, err := bounded("AI_TOPIC_QUESTION_COUNT", 5, 8)
	if err != nil {
		return AIConfig{}, err
	}
	vocabulary, err := bounded("AI_TOPIC_VOCABULARY_COUNT", 10, 20)
	if err != nil {
		return AIConfig{}, err
	}
	grammar, err := bounded("AI_TOPIC_GRAMMAR_COUNT", 3, 8)
	if err != nil {
		return AIConfig{}, err
	}
	attempts, err := bounded("AI_TOPIC_MAX_ATTEMPTS", 5, 20)
	if err != nil {
		return AIConfig{}, err
	}
	raw, err := os.ReadFile(valueOrDefault("TARGETS_PATH", "/app/targets.json"))
	if err != nil {
		return AIConfig{}, fmt.Errorf("read targets: %w", err)
	}
	var file struct {
		Telegram []struct {
			Key, Name       string
			ChatID          json.RawMessage `json:"chat_id"`
			MessageThreadID *int64          `json:"message_thread_id"`
		} `json:"telegram"`
	}
	if err = json.Unmarshal(raw, &file); err != nil {
		return AIConfig{}, fmt.Errorf("parse targets: %w", err)
	}
	var target Target
	for _, item := range file.Telegram {
		if item.Key == targetKey {
			var destination string
			if len(item.ChatID) > 0 && item.ChatID[0] == '"' {
				_ = json.Unmarshal(item.ChatID, &destination)
			} else {
				destination = string(item.ChatID)
			}
			target = Target{item.Key, item.Name, destination, item.MessageThreadID}
			break
		}
	}
	if target.Key == "" || target.Destination == "" {
		return AIConfig{}, fmt.Errorf("AI topic target %q not found", targetKey)
	}
	return AIConfig{true, key, valueOrDefault("DEEPSEEK_MODEL", "deepseek-flash"), valueOrDefault("DEEPSEEK_BASE_URL", "https://api.deepseek.com"), publish, valueOrDefault("TIMEZONE", "Europe/Moscow"), timeout, questions, vocabulary, grammar, attempts, target}, nil
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
