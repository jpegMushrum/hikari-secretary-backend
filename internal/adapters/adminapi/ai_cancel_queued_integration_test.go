//go:build integration

package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

func TestCancelQueuedAIPublicationUsesActualQueue(t *testing.T) {
	ctx := context.Background()
	pool := managementSortTestPool(t)
	h := &Management{Pool: pool, AILocation: time.UTC, AIAutomationConfigured: true}
	handler := h.Router(http.NotFoundHandler())
	call := func(method, path, body string, want int) learningcontent.AutomationState {
		t.Helper()
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(method, "/api/v1/admin/ai/automation"+path, strings.NewReader(body)))
		if res.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, res.Code, res.Body)
		}
		var state learningcontent.AutomationState
		if want == http.StatusOK {
			if err := json.Unmarshal(res.Body.Bytes(), &state); err != nil {
				t.Fatal(err)
			}
		}
		return state
	}
	_, err := pool.Exec(ctx, `INSERT INTO publications(id,creator_telegram_id,source,status,scheduled_at) VALUES
		(1,42,'ai_daily_topic','scheduled',NOW()+interval '2 days'),
		(2,42,'ai_daily_topic','scheduled',NOW()+interval '1 day'),
		(3,42,'admin','scheduled',NOW());
		INSERT INTO publication_deliveries(publication_id,target_key,target_name,destination,status)
		VALUES(1,'a','First','test','retry'),(2,'a','Second','test','pending');
		INSERT INTO ai_topic_jobs(publication_id,scheduled_for,status,next_attempt_at,topic,automatic)
		SELECT id,scheduled_at,CASE WHEN id=2 THEN 'generating' ELSE 'scheduled' END,NOW(),'{"title":"テーマ"}',false
		FROM publications WHERE id IN (1,2);`)
	if err != nil {
		t.Fatal(err)
	}
	state := call("GET", "", "", 200)
	if state.CanCancelToday || !state.CanCancelQueued || state.QueuedPublicationID == nil || *state.QueuedPublicationID != 2 {
		t.Fatalf("future manual publication unavailable: %+v", state)
	}
	call("POST", "/cancel-queued", "{}", 400)
	call("POST", "/cancel-queued", `{"publication_id":3}`, 409)
	call("POST", "/cancel-queued", `{"publication_id":1}`, 409)
	if _, err := pool.Exec(ctx, "UPDATE publication_deliveries SET status='publishing' WHERE publication_id=2"); err != nil {
		t.Fatal(err)
	}
	if state := call("GET", "", "", 200); state.CanCancelQueued || state.QueuedPublicationID != nil {
		t.Fatal("older publication selected during dispatch of latest")
	}
	call("POST", "/cancel-queued", `{"publication_id":2}`, 409)
	if _, err := pool.Exec(ctx, "UPDATE publication_deliveries SET status='retry' WHERE publication_id=2"); err != nil {
		t.Fatal(err)
	}
	state = call("POST", "/cancel-queued", `{"publication_id":2}`, 200)
	if !state.Enabled || !state.CanCancelQueued || state.QueuedPublicationID == nil || *state.QueuedPublicationID != 1 {
		t.Fatalf("wrong next target or toggle state: %+v", state)
	}
	call("POST", "/cancel-queued", `{"publication_id":2}`, 409)
	state = call("POST", "/cancel-queued", `{"publication_id":1}`, 200)
	if state.CanCancelQueued || state.QueuedPublicationID != nil || !state.Enabled {
		t.Fatalf("queue not empty or automation changed: %+v", state)
	}
	var valid bool
	err = pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM publications WHERE id IN (1,2) AND status='cancelled')=2 AND
		(SELECT status FROM publications WHERE id=3)='scheduled' AND
		(SELECT status FROM ai_topic_jobs WHERE publication_id=1)='scheduled' AND
		(SELECT status FROM ai_topic_jobs WHERE publication_id=2)='cancelled' AND
		(SELECT count(*) FROM ai_topic_versions)=2`).Scan(&valid)
	if err != nil || !valid {
		t.Fatalf("queued state, generation history or replacement fence incorrect: %v", err)
	}
}

func TestCancelQueuedRechecksClaimAfterWaitingForPublisher(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := managementSortTestPool(t)
	_, err := pool.Exec(ctx, `INSERT INTO publications(id,creator_telegram_id,source,status,scheduled_at)
		VALUES(7,42,'ai_daily_topic','scheduled',NOW());
		INSERT INTO publication_deliveries(publication_id,target_key,target_name,destination,status)
		VALUES(7,'club','Club','test','pending')`)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Rollback(ctx)
	var pid int
	if err := claim.QueryRow(ctx, "SELECT pg_backend_pid() FROM publications WHERE id=7 FOR UPDATE").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- learningcontent.NewPostgresRepository(pool).CancelQueued(ctx, 7, 42) }()
	for {
		var waiting bool
		if err := claim.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := claim.Exec(ctx, "UPDATE publication_deliveries SET status='publishing' WHERE publication_id=7"); err != nil {
		t.Fatal(err)
	}
	if err := claim.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != domain.ErrInvalidState {
		t.Fatalf("cancelled a claimed delivery: %v", err)
	}
}

func TestQueueGenerationRejectsUnlinkedQueuedAI(t *testing.T) {
	ctx := context.Background()
	pool := managementSortTestPool(t)
	if _, err := pool.Exec(ctx, `INSERT INTO publications(id,creator_telegram_id,source,status,scheduled_at)
		VALUES(12,42,'ai_daily_topic','scheduled',NOW()+interval '1 day')`); err != nil {
		t.Fatal(err)
	}
	repo := learningcontent.NewPostgresRepository(pool)
	slot := time.Now().Add(24 * time.Hour)
	if _, err := repo.QueueGeneration(ctx, slot, 42); err != domain.ErrInvalidState {
		t.Fatalf("created generation alongside unlinked AI: %v", err)
	}
	var jobs int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM ai_topic_jobs").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("created job before cancellation: %d %v", jobs, err)
	}
	if err := repo.CancelQueued(ctx, 12, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QueueGeneration(ctx, slot, 42); err != nil {
		t.Fatal(err)
	}
}
