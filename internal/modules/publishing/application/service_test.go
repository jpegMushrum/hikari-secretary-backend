package application

import (
	"context"
	"testing"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type fakeRepository struct {
	draft          domain.Draft
	toggled        domain.Target
	scheduledAt    time.Time
	publicationID  int64
	scheduleResult bool
	scheduleErr    error
	scheduledDraft domain.ScheduledDraft
}

func (r *fakeRepository) CreateScheduled(_ context.Context, draft domain.ScheduledDraft) (int64, error) {
	r.scheduledDraft = draft
	return 43, nil
}

func (r *fakeRepository) CreateDraft(_ context.Context, draft domain.Draft) (int64, error) {
	r.draft = draft
	return 42, nil
}
func (r *fakeRepository) ToggleTarget(_ context.Context, id int64, target domain.Target) error {
	r.publicationID = id
	r.toggled = target
	return nil
}
func (r *fakeRepository) Schedule(_ context.Context, id int64, at time.Time) (bool, error) {
	r.publicationID = id
	r.scheduledAt = at
	return r.scheduleResult, r.scheduleErr
}
func (r *fakeRepository) Publication(context.Context, int64) (map[string]any, bool, error) {
	return nil, false, nil
}
func (r *fakeRepository) Queue(context.Context, int) ([]map[string]any, error) { return nil, nil }
func (r *fakeRepository) Cancel(context.Context, int64) (bool, error)          { return false, nil }
func (r *fakeRepository) ClaimDue(context.Context) (*domain.Delivery, error)   { return nil, nil }
func (r *fakeRepository) Succeeded(context.Context, int64, string) error       { return nil }
func (r *fakeRepository) Failed(context.Context, int64, string, int) error     { return nil }
func (r *fakeRepository) Notices(context.Context) ([]domain.Notice, error)     { return nil, nil }
func (r *fakeRepository) MarkNotified(context.Context, int64) error            { return nil }
func (r *fakeRepository) RecoverInterruptedDeliveries(context.Context) error   { return nil }
func (r *fakeRepository) ReplaceScheduled(context.Context, int64, string, map[string]any) (bool, error) {
	return true, nil
}

func TestCreateDraftRejectsInvalidInputBeforeRepository(t *testing.T) {
	repo := &fakeRepository{}
	service := NewService(repo)
	if _, err := service.CreateDraft(context.Background(), domain.Draft{}); err == nil {
		t.Fatal("expected creator validation error")
	}
	if _, err := service.CreateDraft(context.Background(), domain.Draft{CreatorTelegramID: 1, Media: []domain.Media{{Position: 2, Path: "x"}}}); err == nil {
		t.Fatal("expected media validation error")
	}
	if repo.draft.CreatorTelegramID != 0 {
		t.Fatal("repository must not be called for invalid draft")
	}
}
func TestCreateDraftSetsAdminSource(t *testing.T) {
	repo := &fakeRepository{}
	id, err := NewService(repo).CreateDraft(context.Background(), domain.Draft{CreatorTelegramID: 7})
	if err != nil || id != 42 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if repo.draft.Source != "admin" {
		t.Fatalf("source=%q", repo.draft.Source)
	}
}
func TestScheduleNormalizesUTCAndRejectsInvalidID(t *testing.T) {
	repo := &fakeRepository{scheduleResult: true}
	service := NewService(repo)
	if _, err := service.Schedule(context.Background(), 0, time.Now()); err == nil {
		t.Fatal("expected validation error")
	}
	local := time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("UTC+3", 3*3600))
	ok, err := service.Schedule(context.Background(), 10, local)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if repo.scheduledAt.Location() != time.UTC {
		t.Fatalf("not UTC: %s", repo.scheduledAt.Location())
	}
}
func TestFailedRejectsZeroAttempts(t *testing.T) {
	if err := NewService(&fakeRepository{}).Failed(context.Background(), 1, "error", 0); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestCreateScheduledValidatesAndNormalizesCommand(t *testing.T) {
	repo := &fakeRepository{}
	service := NewService(repo)
	if _, err := service.CreateScheduled(context.Background(), domain.ScheduledDraft{Draft: domain.Draft{CreatorTelegramID: 1}}); err == nil {
		t.Fatal("expected missing schedule and target error")
	}
	local := time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("UTC+3", 3*3600))
	id, err := service.CreateScheduled(context.Background(), domain.ScheduledDraft{
		Draft:       domain.Draft{CreatorTelegramID: 7},
		ScheduledAt: local,
		Targets:     []domain.Target{{Key: "main", Name: "Main", Destination: "-100"}},
	})
	if err != nil || id != 43 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if repo.scheduledDraft.ScheduledAt.Location() != time.UTC || repo.scheduledDraft.Draft.Source != "admin" {
		t.Fatalf("unexpected scheduled command: %#v", repo.scheduledDraft)
	}
}
