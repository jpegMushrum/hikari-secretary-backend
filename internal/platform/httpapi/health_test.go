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

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/serviceauth"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
)

type fakePinger struct {
	err error
}

func (p fakePinger) Ping(context.Context) error {
	return p.err
}

type fakePeopleService struct {
	identity domain.TelegramIdentity
	list     application.ListPeopleResult
	syncErr  error
	listErr  error
	command  domain.SyncIdentityCommand
	query    application.ListPeopleQuery
}

func (s *fakePeopleService) SyncTelegramIdentity(_ context.Context, command domain.SyncIdentityCommand) (domain.TelegramIdentity, error) {
	s.command = command
	if s.syncErr != nil {
		return domain.TelegramIdentity{}, s.syncErr
	}
	return s.identity, nil
}

func (s *fakePeopleService) ListPeople(_ context.Context, query application.ListPeopleQuery) (application.ListPeopleResult, error) {
	s.query = query
	if s.listErr != nil {
		return application.ListPeopleResult{}, s.listErr
	}
	return s.list, nil
}

func TestHealthzIsAlwaysAvailable(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	NewRouter(nil, nil, nil, nil, nil).ServeHTTP(response, request)

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

			NewRouter(test.pinger, nil, nil, nil, nil).ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Errorf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}

func TestAdminEndpointRequiresTelegramAuthorization(t *testing.T) {
	now := time.Now()
	validator := telegramauth.NewValidator("test-bot-token", time.Hour)
	router := NewRouter(nil, validator, map[int64]struct{}{123: {}}, nil, nil)

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

func TestListPeopleRequiresAdministratorAndParsesQuery(t *testing.T) {
	now := time.Now()
	validator := telegramauth.NewValidator("test-bot-token", time.Hour)
	service := &fakePeopleService{list: application.ListPeopleResult{
		Items:      []application.PersonListItem{{ID: 17, DisplayName: "Hikari", TelegramUserID: 123, Visits: 4}},
		TotalItems: 1,
	}}
	router := NewRouter(nil, validator, map[int64]struct{}{123: {}}, nil, service)

	t.Run("missing authorization", func(t *testing.T) {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/people", nil)

		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
		}
	})

	t.Run("valid request", func(t *testing.T) {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/people?page=2&page_size=20&search=%20Hikari%20&is_russian_citizen=true&is_itmo_student=false&sort=visits&order=asc", nil)
		request.Header.Set("Authorization", "tma "+signedInitData(t, "test-bot-token", 123, now))

		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
		}
		if service.query.Page != (pagination.Page{Number: 2, Size: 20}) {
			t.Errorf("page = %#v, want {Number: 2, Size: 20}", service.query.Page)
		}
		if service.query.Search != "Hikari" || service.query.Sort != application.SortByVisits || service.query.Order != application.SortAscending {
			t.Errorf("query = %#v", service.query)
		}
		if service.query.IsRussianCitizen == nil || !*service.query.IsRussianCitizen || service.query.IsITMOStudent == nil || *service.query.IsITMOStudent {
			t.Errorf("filters = %#v", service.query)
		}

		var body struct {
			Page struct {
				Number     int   `json:"number"`
				Size       int   `json:"size"`
				TotalItems int64 `json:"total_items"`
				TotalPages int64 `json:"total_pages"`
			} `json:"page"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Page.Number != 2 || body.Page.Size != 20 || body.Page.TotalItems != 1 || body.Page.TotalPages != 1 {
			t.Errorf("page response = %#v", body.Page)
		}
	})

	t.Run("invalid page size", func(t *testing.T) {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/people?page_size=31", nil)
		request.Header.Set("Authorization", "tma "+signedInitData(t, "test-bot-token", 123, now))

		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
		}
	})
}

func TestSyncTelegramIdentityRequiresBotServiceAuthorization(t *testing.T) {
	service := &fakePeopleService{identity: domain.TelegramIdentity{
		PersonID:       42,
		TelegramUserID: 123,
		Username:       "hikari",
		DisplayName:    "Hikari ITMO",
	}}
	router := NewRouter(nil, nil, nil, serviceauth.NewValidator("service-secret"), service)

	t.Run("missing authorization", func(t *testing.T) {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/internal/bot/v1/people/identity", strings.NewReader(`{"telegram_user_id":123,"display_name":"Hikari ITMO"}`))

		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
		}
	})

	t.Run("valid request", func(t *testing.T) {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/internal/bot/v1/people/identity", strings.NewReader(`{"telegram_user_id":123,"username":"@hikari","display_name":"Hikari ITMO"}`))
		request.Header.Set("Authorization", "Bearer service-secret")

		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
		}
		if service.command != (domain.SyncIdentityCommand{TelegramUserID: 123, Username: "@hikari", DisplayName: "Hikari ITMO"}) {
			t.Errorf("command = %#v", service.command)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/internal/bot/v1/people/identity", strings.NewReader(`{"telegram_user_id":123,"display_name":"Hikari ITMO","ignored":true}`))
		request.Header.Set("Authorization", "Bearer service-secret")

		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
		}
	})
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
