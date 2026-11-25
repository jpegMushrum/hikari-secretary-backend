//go:build integration

package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	learningpostgres "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent/adapters/postgres"
	learningdomain "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

func TestDailyAutomationPreservesTodayAndResumesOnlyFutureSlots(t *testing.T) {
	ctx := context.Background()
	pool := managementSortTestPool(t)
	repo := learningpostgres.NewRepository(pool)
	h := &Management{Pool: pool, AIAutomationConfigured: true, AILocation: time.FixedZone("club", 3*3600)}
	handler := h.Router(http.NotFoundHandler())
	call := func(method, path, body string, code int) learningpostgres.AutomationState {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/admin/ai/automation"+path, strings.NewReader(body)))
		if w.Code != code {
			t.Fatalf("%s %s = %d: %s", method, path, w.Code, w.Body)
		}
		var state learningpostgres.AutomationState
		if code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
				t.Fatal(err)
			}
		}
		return state
	}
	today := h.today()
	tomorrow := today.AddDate(0, 0, 1)
	_, err := pool.Exec(ctx, `INSERT INTO publications(id,creator_telegram_id,source,status,scheduled_at)
		VALUES(1,42,'ai_daily_topic','scheduled',$1),(2,42,'ai_daily_topic','scheduled',$2),
		      (3,42,'ai_daily_topic','scheduled',$1);
	`, today.Add(time.Hour), tomorrow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO ai_topic_jobs(scheduled_for,status,next_attempt_at,publication_id,automatic)
		SELECT scheduled_at+id*interval '1 second','scheduled',NOW(),id,id<>3 FROM publications`)
	if err != nil {
		t.Fatal(err)
	}
	check := func(id int, status string) {
		t.Helper()
		var got string
		if err := pool.QueryRow(ctx, "SELECT status FROM publications WHERE id=$1", id).Scan(&got); err != nil || got != status {
			t.Fatalf("publication %d=%s: %v", id, got, err)
		}
	}
	if s := call("GET", "", "", 200); !s.Configured || !s.Enabled || !s.CanCancelToday {
		t.Fatal(s)
	}
	call("PUT", "", `{}`, 400)
	call("PUT", "", `{"enabled":false}`, 200)
	check(1, "scheduled")
	check(2, "cancelled")
	check(3, "scheduled")
	if id, err := repo.Enqueue(ctx, tomorrow.Add(2*time.Hour)); id != 0 || err != nil {
		t.Fatalf("enqueued while disabled %d %v", id, err)
	}
	if s := call("POST", "/cancel-today", "", 200); s.CanCancelToday || s.Enabled {
		t.Fatal(s)
	}
	check(1, "cancelled")
	check(3, "scheduled")
	call("POST", "/cancel-today", "", 409)
	if _, err := pool.Exec(ctx, `UPDATE publications SET status='sent' WHERE id=3;
		INSERT INTO publications(id,creator_telegram_id,status)
		SELECT n,42,'scheduled' FROM generate_series(10,14) n`); err != nil {
		t.Fatal(err)
	}
	call("PUT", "", `{"enabled":true}`, 409)
	if s := call("GET", "", "", 200); s.Enabled {
		t.Fatal("full queue enabled automation")
	}
	check(2, "cancelled")
	if _, err := pool.Exec(ctx, "UPDATE publications SET status='cancelled' WHERE id=13"); err != nil {
		t.Fatal(err)
	}
	if s := call("PUT", "", `{"enabled":true}`, 200); !s.Enabled || s.CanCancelToday {
		t.Fatal(s)
	}
	check(1, "cancelled")
	check(2, "scheduled")
	if id, err := repo.Enqueue(ctx, time.Now().Add(-time.Hour)); id != 0 || err != nil {
		t.Fatalf("enqueued skipped slot %d %v", id, err)
	}
	if id, err := repo.Enqueue(ctx, tomorrow.Add(2*time.Hour)); id == 0 || err != nil {
		t.Fatalf("missed future slot %d %v", id, err)
	}
	h.AIAutomationConfigured = false
	if s := call("GET", "", "", 200); s.Enabled || s.Configured {
		t.Fatal(s)
	}
	call("PUT", "", `{"enabled":true}`, 409)
}

func TestDailyAutomationRevokesLeaseAndRespectsDeliveryClaim(t *testing.T) {
	ctx := context.Background()
	pool := managementSortTestPool(t)
	repo := learningpostgres.NewRepository(pool)
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	id, err := repo.Enqueue(ctx, today.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	job, err := repo.Claim(ctx, true, time.Minute)
	if err != nil || job == nil || job.ID != id {
		t.Fatalf("claim %v %v", job, err)
	}
	if err := repo.SetAutomation(ctx, false, today, 42); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetAutomation(ctx, true, today, 42); err != nil {
		t.Fatal(err)
	}
	if latest, err := repo.Claim(ctx, true, 2*time.Minute); err != nil || latest == nil || latest.ID != job.ID {
		t.Fatalf("claim after resume: %v %v", latest, err)
	}
	if err := repo.Complete(ctx, job, domain.ScheduledDraft{}, learningdomain.Topic{}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked claim passed lease validation: %v", err)
	}
	if err := repo.MarkFailed(ctx, job, "stale failure", nil); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM ai_topic_jobs WHERE id=$1", id).Scan(&status); err != nil || status != "generating" {
		t.Fatalf("%s %v", status, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO publications(id,creator_telegram_id,source,status,scheduled_at)
		VALUES(55,42,'ai_daily_topic','scheduled',NOW());
		INSERT INTO ai_topic_jobs(scheduled_for,status,next_attempt_at,publication_id) VALUES(NOW(),'scheduled',NOW(),55);
		INSERT INTO publication_deliveries(publication_id,target_key,target_name,destination,status)
		  VALUES(55,'club','Club','@club','publishing')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CancelToday(ctx, today, 42); err != domain.ErrInvalidState {
		t.Fatalf("cancel during delivery: %v", err)
	}
}

func waitForBlockedSession(t *testing.T, ctx context.Context, holder pgx.Tx, holderPID int) {
	t.Helper()
	// pg_stat_activity can retain the holder's initial snapshot and omit a
	// connection opened later. pg_locks reflects current lock requests.
	const query = `SELECT EXISTS (
		SELECT 1 FROM pg_locks WHERE NOT granted AND $1 = ANY(pg_blocking_pids(pid))
	)`
	for {
		var waiting bool
		if err := holder.QueryRow(ctx, query, holderPID).Scan(&waiting); err != nil {
			t.Fatalf("waiting for a session blocked by PID %d: %v", holderPID, err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCancelTodayWaitsForPublicationBeingLinked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := managementSortTestPool(t)
	repo := learningpostgres.NewRepository(pool)
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	id, err := repo.Enqueue(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	held, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(ctx)
	var pid int
	if err := held.QueryRow(ctx, "SELECT pg_backend_pid() FROM ai_topic_jobs WHERE id=$1 FOR UPDATE", id).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- repo.CancelToday(ctx, today, 42) }()
	waitForBlockedSession(t, ctx, held, pid)
	if _, err := held.Exec(ctx, `INSERT INTO publications(id,creator_telegram_id,source,status,scheduled_at)
		VALUES(55,42,'ai_daily_topic','scheduled',NOW())`); err != nil {
		t.Fatal(err)
	}
	if _, err := held.Exec(ctx, "UPDATE ai_topic_jobs SET status='scheduled',publication_id=55 WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if err := held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM publications WHERE id=55").Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("%s %v", status, err)
	}
}
