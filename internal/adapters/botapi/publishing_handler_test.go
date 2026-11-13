package botapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	publishing "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type publishingRepositoryStub struct {
	queueLimit    int
	scheduled     domain.ScheduledDraft
	createError   error
	recoveryCalls int
}

func (r *publishingRepositoryStub) CreateDraft(context.Context, domain.Draft) (int64, error) {
	return 1, nil
}
func (r *publishingRepositoryStub) CreateScheduled(_ context.Context, draft domain.ScheduledDraft) (int64, error) {
	r.scheduled = draft
	if r.createError != nil {
		return 0, r.createError
	}
	return 73, nil
}
func (r *publishingRepositoryStub) ToggleTarget(context.Context, int64, domain.Target) error {
	return nil
}
func (r *publishingRepositoryStub) Schedule(context.Context, int64, time.Time) (bool, error) {
	return true, nil
}
func (r *publishingRepositoryStub) Publication(context.Context, int64) (map[string]any, bool, error) {
	return nil, false, nil
}
func (r *publishingRepositoryStub) Queue(_ context.Context, limit int) ([]map[string]any, error) {
	r.queueLimit = limit
	return []map[string]any{}, nil
}
func (r *publishingRepositoryStub) Cancel(context.Context, int64) (bool, error) { return false, nil }
func (r *publishingRepositoryStub) ClaimDue(context.Context) (*domain.Delivery, error) {
	return nil, nil
}
func (r *publishingRepositoryStub) Succeeded(context.Context, int64, string) error { return nil }
func (r *publishingRepositoryStub) Failed(context.Context, int64, string, int) error {
	return nil
}
func (r *publishingRepositoryStub) Notices(context.Context) ([]domain.Notice, error) { return nil, nil }
func (r *publishingRepositoryStub) MarkNotified(context.Context, int64) error        { return nil }
func (r *publishingRepositoryStub) RecoverInterruptedDeliveries(context.Context) error {
	r.recoveryCalls++
	return nil
}

func publishingTestRouter(repository *publishingRepositoryStub) http.Handler {
	return NewRouter(nil, nil, publishing.NewService(repository))
}

func TestListPublicationsUsesValidatedLimit(t *testing.T) {
	repository := &publishingRepositoryStub{}
	router := publishingTestRouter(repository)

	request := httptest.NewRequest(http.MethodGet, "/internal/bot/v1/publications?limit=5", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || repository.queueLimit != 5 {
		t.Fatalf("status=%d limit=%d", recorder.Code, repository.queueLimit)
	}

	invalid := httptest.NewRecorder()
	router.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/internal/bot/v1/publications?limit=101", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status=%d", invalid.Code)
	}
}

func TestCreateScheduledPublicationIsOneBackendCommand(t *testing.T) {
	repository := &publishingRepositoryStub{}
	router := publishingTestRouter(repository)
	body := `{"creator_id":7,"text":"scheduled","entities":[],"media_paths":[],"source":"worker","source_key":"job-1","scheduled_at":"2026-11-13T12:00:00Z","targets":[{"key":"main","name":"Main","destination":"-100"}]}`
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/bot/v1/publications/scheduled", strings.NewReader(body)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if repository.scheduled.SourceKey != "job-1" || len(repository.scheduled.Targets) != 1 || repository.scheduled.ScheduledAt.Location() != time.UTC {
		t.Fatalf("unexpected scheduled command: %#v", repository.scheduled)
	}
}

func TestCreateScheduledPublicationReportsCapacityLimit(t *testing.T) {
	repository := &publishingRepositoryStub{createError: domain.ErrPendingPublicationLimit}
	router := publishingTestRouter(repository)
	body := `{"creator_id":7,"scheduled_at":"2026-11-13T12:00:00Z","targets":[{"key":"main","name":"Main","destination":"-100"}]}`
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/bot/v1/publications/scheduled", strings.NewReader(body)))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "capacity_limit") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRecoverDeliveriesDelegatesToPublishingService(t *testing.T) {
	repository := &publishingRepositoryStub{}
	router := publishingTestRouter(repository)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/bot/v1/deliveries/recover", nil))
	if recorder.Code != http.StatusOK || repository.recoveryCalls != 1 {
		t.Fatalf("status=%d recoveries=%d", recorder.Code, repository.recoveryCalls)
	}
}

func TestCreateScheduledPublicationDoesNotHideUnexpectedErrors(t *testing.T) {
	repository := &publishingRepositoryStub{createError: errors.New("database unavailable")}
	router := publishingTestRouter(repository)
	body := `{"creator_id":7,"scheduled_at":"2026-11-13T12:00:00Z","targets":[{"key":"main","name":"Main","destination":"-100"}]}`
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/bot/v1/publications/scheduled", strings.NewReader(body)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
