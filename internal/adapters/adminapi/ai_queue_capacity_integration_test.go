//go:build integration

package adminapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent"
	publishing "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/adapters/postgres"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

func TestAIQueueHasOneSlotAndNextGenerationWaitsForIt(t *testing.T) {
	ctx := context.Background()
	pool := managementSortTestPool(t)
	posts := publishing.NewRepository(pool)
	jobs := learningcontent.NewPostgresRepository(pool)
	now := time.Now().UTC()
	draft := domain.ScheduledDraft{
		Draft:       domain.Draft{CreatorTelegramID: 42, Source: "ai_daily_topic", Text: "test"},
		ScheduledAt: now, Targets: []domain.Target{{Key: "club", Name: "Club", Destination: "test"}},
	}
	type result struct {
		id  int64
		err error
	}
	start, done := make(chan struct{}), make(chan result, 2)
	for _, key := range []string{"first", "second"} {
		input := draft
		input.SourceKey = key
		go func() {
			<-start
			id, err := posts.CreateScheduled(ctx, input)
			done <- result{id, err}
		}()
	}
	close(start)
	first, second := <-done, <-done
	if first.err != nil {
		first, second = second, first
	}
	if first.err != nil || !errors.Is(second.err, domain.ErrPendingPublicationLimit) {
		t.Fatalf("concurrent AI publications: %+v, %+v", first, second)
	}
	draft.SourceKey = "existing-ai-draft"
	if _, err := posts.CreateDraft(ctx, draft.Draft); err != nil {
		t.Fatal(err)
	}
	draft.Source = "admin"
	if _, err := posts.CreateScheduled(ctx, draft); !errors.Is(err, domain.ErrPendingPublicationLimit) {
		t.Fatalf("used request source instead of existing draft source: %v", err)
	}
	draft.SourceKey = "ordinary"
	if _, err := posts.CreateScheduled(ctx, draft); err != nil {
		t.Fatalf("ordinary publication should still fit: %v", err)
	}
	next, err := jobs.Enqueue(ctx, now.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if claim, err := jobs.Claim(ctx, true, time.Minute); err != nil || claim != nil {
		t.Fatalf("generation started with occupied queue: %v %v", claim, err)
	}
	if ok, err := posts.Cancel(ctx, first.id); err != nil || !ok {
		t.Fatalf("cancel previous: %t %v", ok, err)
	}
	if claim, err := jobs.Claim(ctx, true, time.Minute); err != nil || claim == nil || claim.ID != next {
		t.Fatalf("generation did not resume after cancellation: %v %v", claim, err)
	}
}

func TestResumePreservesLegacyPausedEditionsAndOnlyFillsFreeAISlot(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "occupied"}[existing], func(t *testing.T) {
			ctx := context.Background()
			pool := managementSortTestPool(t)
			repo := learningcontent.NewPostgresRepository(pool)
			now := time.Now().UTC()
			if _, err := pool.Exec(ctx, `UPDATE ai_topic_settings SET automation_enabled=false;
				INSERT INTO publications(id,creator_telegram_id,text,source,status,scheduled_at)
				SELECT n,42,'legacy','ai_daily_topic','cancelled',NOW()+n*interval '1 day' FROM generate_series(1,2) n;
				INSERT INTO ai_topic_jobs(scheduled_for,status,next_attempt_at,publication_id,paused_by_automation)
				SELECT scheduled_at,'cancelled',NOW(),id,true FROM publications`); err != nil {
				t.Fatal(err)
			}
			if existing {
				if _, err := pool.Exec(ctx, `INSERT INTO publications(id,creator_telegram_id,source,status,scheduled_at)
					VALUES(3,42,'ai_daily_topic','scheduled',NOW())`); err != nil {
					t.Fatal(err)
				}
			}
			if err := repo.SetAutomation(ctx, true, now, 42); err != nil {
				t.Fatal(err)
			}
			var count, id, released int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*),MAX(id) FROM publications WHERE status='scheduled'`).Scan(&count, &id); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM ai_topic_jobs WHERE slot_released AND status='cancelled'`).Scan(&released); err != nil {
				t.Fatal(err)
			}
			if count != 1 || (existing && (id != 3 || released != 2)) || (!existing && (id != 1 || released != 1)) {
				t.Fatalf("active=%d publication=%d archived=%d", count, id, released)
			}
		})
	}
}
