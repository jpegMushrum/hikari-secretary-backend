package application

import (
	"context"
	"testing"
)

type cancellationRepository struct {
	Repository
	id, actor int64
	calls     int
}

func (r *cancellationRepository) Cancel(_ context.Context, id, actor int64) (CancelResult, error) {
	r.id, r.actor = id, actor
	r.calls++
	return CancelResult{NotificationsQueued: 3}, nil
}

func TestCancelRejectsInvalidIDBeforeRepository(t *testing.T) {
	repo := &cancellationRepository{}
	_, err := NewService(repo).Cancel(context.Background(), 0, 123)
	if err != ErrEventNotFound || repo.calls != 0 {
		t.Fatalf("error=%v calls=%d", err, repo.calls)
	}
}

func TestCancelPreservesActorAndQueuedResult(t *testing.T) {
	for _, actor := range []int64{0, 123} {
		repo := &cancellationRepository{}
		result, err := NewService(repo).Cancel(context.Background(), 7, actor)
		if err != nil || repo.id != 7 || repo.actor != actor || result.NotificationsQueued != 3 {
			t.Fatalf("actor=%d repo=%+v result=%+v error=%v", actor, repo, result, err)
		}
	}
}
