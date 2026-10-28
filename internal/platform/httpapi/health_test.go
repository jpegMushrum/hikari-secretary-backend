package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
)

type fakePinger struct {
	err error
}

func (p fakePinger) Ping(context.Context) error {
	return p.err
}

func TestHealthzIsAlwaysAvailable(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	NewRouter(nil, nil, nil).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestReadyzRequiresDatabase(t *testing.T) {
	tests := []struct {
		name       string
		pinger     Pinger
		wantStatus int
	}{
		{name: "database available", pinger: fakePinger{}, wantStatus: http.StatusOK},
		{name: "database unavailable", pinger: fakePinger{err: errors.New("offline")}, wantStatus: http.StatusServiceUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)

			NewRouter(test.pinger, nil, nil).ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Errorf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}

func TestAdminEndpointRequiresTelegramAuthorization(t *testing.T) {
	now := time.Now()
	validator := telegramauth.NewValidator("test-bot-token", time.Hour)
	router := NewRouter(nil, validator, map[int64]struct{}{123: {}})

	tests := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{name: "missing authorization", wantStatus: http.StatusUnauthorized},
		{name: "invalid authorization", authorization: "tma invalid", wantStatus: http.StatusUnauthorized},
		{name: "non administrator", authorization: "tma " + signedInitData(t, "test-bot-token", 999, now), wantStatus: http.StatusForbidden},
		{name: "administrator", authorization: "tma " + signedInitData(t, "test-bot-token", 123, now), wantStatus: http.StatusOK},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/me", nil)
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}

			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Errorf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func signedInitData(t *testing.T, botToken string, telegramID int64, authDate time.Time) string {
	t.Helper()
	user, err := json.Marshal(map[string]any{"id": telegramID, "first_name": "Hikari", "username": "hikari_admin"})
	if err != nil {
		t.Fatal(err)
	}

	values := url.Values{
		"auth_date": {strconv.FormatInt(authDate.Unix(), 10)},
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
