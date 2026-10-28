package telegramauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	now := time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)
	validator := NewValidator("test-bot-token", time.Hour)
	validator.now = func() time.Time { return now }

	identity, err := validator.Validate(signedInitData(t, "test-bot-token", 123, now.Add(-time.Minute)))
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if identity.TelegramID != 123 || identity.Username != "hikari_admin" {
		t.Errorf("identity = %#v, want Telegram ID 123 and username hikari_admin", identity)
	}
}

func TestValidateRejectsInvalidOrOldData(t *testing.T) {
	now := time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)
	validator := NewValidator("test-bot-token", time.Hour)
	validator.now = func() time.Time { return now }

	t.Run("tampered", func(t *testing.T) {
		raw := signedInitData(t, "test-bot-token", 123, now)
		raw = strings.Replace(raw, "hikari_admin", "intruder", 1)
		if _, err := validator.Validate(raw); err != ErrInvalidInitData {
			t.Fatalf("Validate() error = %v, want ErrInvalidInitData", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		raw := signedInitData(t, "test-bot-token", 123, now.Add(-2*time.Hour))
		if _, err := validator.Validate(raw); err != ErrExpiredInitData {
			t.Fatalf("Validate() error = %v, want ErrExpiredInitData", err)
		}
	})

	t.Run("duplicate field", func(t *testing.T) {
		raw := signedInitData(t, "test-bot-token", 123, now) + "&auth_date=1"
		if _, err := validator.Validate(raw); err != ErrInvalidInitData {
			t.Fatalf("Validate() error = %v, want ErrInvalidInitData", err)
		}
	})
}

func signedInitData(t *testing.T, botToken string, telegramID int64, authDate time.Time) string {
	t.Helper()
	user, err := json.Marshal(map[string]any{
		"id":         telegramID,
		"first_name": "Hikari",
		"last_name":  "Admin",
		"username":   "hikari_admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	values := url.Values{
		"auth_date": {strconv.FormatInt(authDate.Unix(), 10)},
		"query_id":  {"AAEAAAE"},
		"user":      {string(user)},
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	check := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = check.Write([]byte(strings.Join(parts, "\n")))
	values.Set("hash", hex.EncodeToString(check.Sum(nil)))
	return values.Encode()
}
