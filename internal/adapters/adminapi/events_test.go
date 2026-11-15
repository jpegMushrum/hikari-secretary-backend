package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	events "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/requestcontext"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
)

type eventsStub struct {
	list             events.ListEventsResult
	event            events.Event
	participants     events.ListParticipantsResult
	cancel           events.CancelResult
	err              error
	listQuery        events.ListEventsQuery
	participantQuery events.ListParticipantsQuery
	actor            int64
}

func (s *eventsStub) List(_ context.Context, q events.ListEventsQuery) (events.ListEventsResult, error) {
	s.listQuery = q
	return s.list, s.err
}
func (s *eventsStub) Get(context.Context, int64) (events.Event, error) { return s.event, s.err }
func (s *eventsStub) ListParticipants(_ context.Context, _ int64, q events.ListParticipantsQuery) (events.ListParticipantsResult, error) {
	s.participantQuery = q
	return s.participants, s.err
}
func (s *eventsStub) Cancel(_ context.Context, _ int64, actor int64) (events.CancelResult, error) {
	s.actor = actor
	return s.cancel, s.err
}

func TestListEventsParsesStableAdminContract(t *testing.T) {
	service := &eventsStub{list: events.ListEventsResult{Items: []events.Event{{ID: 7, Title: "Клуб"}}, TotalItems: 1}}
	router := NewRouter(service)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/events?page=2&page_size=20&status=completed&search=%20club%20&sort=created_at&order=desc&starts_from=2026-01-01T00:00:00Z", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.listQuery.Page != (pagination.Page{Number: 2, Size: 20}) || service.listQuery.Search != "club" || service.listQuery.Status != "completed" || service.listQuery.Sort != events.SortByCreatedAt || service.listQuery.Order != events.SortDescending {
		t.Fatalf("query=%+v", service.listQuery)
	}
	var body struct {
		Page struct {
			TotalItems int64 `json:"total_items"`
		} `json:"page"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Page.TotalItems != 1 {
		t.Fatalf("total=%d", body.Page.TotalItems)
	}
}

func TestParticipantsRejectUnsupportedSort(t *testing.T) {
	router := NewRouter(&eventsStub{})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/events/1/participants?sort=sql", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 400 {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestCancelUsesAuthenticatedAdministrator(t *testing.T) {
	service := &eventsStub{cancel: events.CancelResult{NotificationsQueued: 2}}
	router := NewRouter(service)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/events/9/cancel", nil)
	request = request.WithContext(requestcontext.WithAdministrator(request.Context(), telegramauth.Identity{TelegramID: 123}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || service.actor != 123 {
		t.Fatalf("status=%d actor=%d", response.Code, service.actor)
	}
}

func TestEventErrorsHaveStableCodes(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{{events.ErrEventNotFound, 404, "event_not_found"}, {events.ErrEventNotActive, 409, "event_not_active"}, {errors.New("db"), 500, "internal_error"}}
	for _, test := range tests {
		service := &eventsStub{err: test.err}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/events/9/cancel", nil)
		request = request.WithContext(requestcontext.WithAdministrator(request.Context(), telegramauth.Identity{TelegramID: 1}))
		response := httptest.NewRecorder()
		NewRouter(service).ServeHTTP(response, request)
		if response.Code != test.status {
			t.Errorf("err=%v status=%d", test.err, response.Code)
		}
		var body map[string]string
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		if body["error"] != test.code {
			t.Errorf("err=%v code=%q", test.err, body["error"])
		}
	}
}
