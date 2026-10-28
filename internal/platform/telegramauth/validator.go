package telegramauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidInitData = errors.New("invalid Telegram init data")
	ErrExpiredInitData = errors.New("expired Telegram init data")
)

type Identity struct {
	TelegramID int64
	FirstName  string
	LastName   string
	Username   string
}

type Validator struct {
	botToken string
	maxAge   time.Duration
	now      func() time.Time
}

func NewValidator(botToken string, maxAge time.Duration) *Validator {
	return &Validator{
		botToken: botToken,
		maxAge:   maxAge,
		now:      time.Now,
	}
}

// Validate verifies Telegram.WebApp.initData and returns the user identity that
// Telegram signed. The client-side initDataUnsafe object must never be trusted.
func (v *Validator) Validate(raw string) (Identity, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return Identity{}, ErrInvalidInitData
	}
	if err := requireSingleValues(values); err != nil {
		return Identity{}, err
	}

	receivedHash := values.Get("hash")
	if receivedHash == "" {
		return Identity{}, ErrInvalidInitData
	}
	values.Del("hash")

	if !validSignature(v.botToken, values, receivedHash) {
		return Identity{}, ErrInvalidInitData
	}
	if err := v.validateAuthDate(values.Get("auth_date")); err != nil {
		return Identity{}, err
	}

	var user struct {
		ID        int64  `json:"id"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Username  string `json:"username"`
	}
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID <= 0 {
		return Identity{}, ErrInvalidInitData
	}

	return Identity{
		TelegramID: user.ID,
		FirstName:  user.FirstName,
		LastName:   user.LastName,
		Username:   user.Username,
	}, nil
}

func requireSingleValues(values url.Values) error {
	for key, entries := range values {
		if key == "" || len(entries) != 1 {
			return ErrInvalidInitData
		}
	}
	return nil
}

func validSignature(botToken string, values url.Values, receivedHash string) bool {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	dataCheckParts := make([]string, 0, len(keys))
	for _, key := range keys {
		dataCheckParts = append(dataCheckParts, key+"="+values.Get(key))
	}

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))

	check := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = check.Write([]byte(strings.Join(dataCheckParts, "\n")))

	received, err := hex.DecodeString(receivedHash)
	return err == nil && hmac.Equal(check.Sum(nil), received)
}

func (v *Validator) validateAuthDate(raw string) error {
	authDate, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || authDate <= 0 {
		return ErrInvalidInitData
	}

	now := v.now()
	timestamp := time.Unix(authDate, 0)
	if timestamp.After(now.Add(30 * time.Second)) {
		return ErrInvalidInitData
	}
	if now.Sub(timestamp) > v.maxAge {
		return ErrExpiredInitData
	}
	return nil
}
