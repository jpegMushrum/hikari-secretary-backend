//go:build integration

package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	learningpostgres "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent/adapters/postgres"
	learningdomain "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent/domain"
	publishing "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/adapters/postgres"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/requestcontext"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
)

func TestQueueGenerationCoalescesRequestsAndFencesOldWorker(t *testing.T) {
	ctx := context.Background()
	pool := managementSortTestPool(t)
	repo := learningpostgres.NewRepository(pool)
	slot := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Minute)
	id, err := repo.Enqueue(ctx, slot)
	if err != nil {
		t.Fatal(err)
	}
	old, err := repo.Claim(ctx, true, time.Minute)
	if err != nil || old == nil || old.ID != id {
		t.Fatalf("old claim %v %v", old, err)
	}
	type result struct {
		id  int64
		err error
	}
	got := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { id, err := repo.QueueGeneration(ctx, slot, 42); got <- result{id, err} }()
	}
	for i := 0; i < 2; i++ {
		if r := <-got; r.err != nil || r.id != id {
			t.Fatalf("coalesced request %+v", r)
		}
	}
	manual, err := repo.Claim(ctx, false, time.Minute)
	if err != nil || manual == nil || manual.ID != id || manual.RequestedBy == nil {
		t.Fatalf("manual claim %v %v", manual, err)
	}
	if err := repo.Complete(ctx, old, domain.ScheduledDraft{}, learningdomain.Topic{}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale generation retained lease: %v", err)
	}
	if next, err := repo.QueueGeneration(ctx, slot, 42); err != nil || next != id {
		t.Fatalf("replayed active manual: %d %v", next, err)
	}
	if next, err := repo.Claim(ctx, false, time.Minute); err != nil || next != nil {
		t.Fatalf("duplicated in-flight request: %v %v", next, err)
	}
}

func TestQueueGenerationReleasesOriginalWhenWorkerBecomesUnavailable(t *testing.T) {
	ctx := context.Background()
	pool := managementSortTestPool(t)
	repo := learningpostgres.NewRepository(pool)
	_, err := pool.Exec(ctx, `INSERT INTO publications(id,creator_telegram_id,status,text,source,scheduled_at) VALUES (9,42,'scheduled','original','ai_daily_topic',NOW());
	INSERT INTO publication_deliveries(publication_id,target_key,target_name,destination,next_attempt_at) VALUES(9,'club','Club','test',NOW());
	INSERT INTO ai_topic_jobs(scheduled_for,status,publication_id,next_attempt_at,regeneration_requested_by) VALUES
	(NOW(),'generating',9,NOW()+interval '5 minutes',42),
	(NOW()+interval '1 hour','pending',NULL,NOW(),42),
	(NOW()+interval '1 day','pending',NULL,NOW(),NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.FailUnavailableManualGenerations(ctx); err != nil {
		t.Fatal(err)
	}
	var failed, pending int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE status='failed'), COUNT(*) FILTER(WHERE status='pending' AND regeneration_requested_by IS NULL) FROM ai_topic_jobs`).Scan(&failed, &pending); err != nil || failed != 2 || pending != 1 {
		t.Fatalf("disabled worker recovery: failed %d, pending automatic %d: %v", failed, pending, err)
	}
	if d, err := publishing.NewRepository(pool).ClaimDue(ctx); err != nil || d == nil || d.Text != "original" {
		t.Fatalf("original held after restart: %v %v", d, err)
	}
}

func TestQueueNewGenerationKeepsHistoryAndReplacesQueuedPost(t *testing.T) {
	ctx := context.Background()
	pool := managementSortTestPool(t)
	repo := learningpostgres.NewRepository(pool)
	publications := publishing.NewRepository(pool)
	location := time.FixedZone("club", 3*60*60)
	now := time.Now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	slot := now.Add(time.Hour).Truncate(time.Minute)
	admin := &Management{Pool: pool, AILocation: location, AIPublishTime: slot.Format("15:04")}
	handler := admin.Router(http.NotFoundHandler())
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/ai/generations/queue", nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req.WithContext(requestcontext.WithAdministrator(ctx, telegramauth.Identity{TelegramID: 42})))
		return res
	}
	if res := call(); res.Code != 503 {
		t.Fatalf("disabled backend: %d", res.Code)
	}
	admin.AIEnabled = true
	if err := repo.SetAutomation(ctx, false, today, 42); err != nil {
		t.Fatal(err)
	}
	submit := func() int64 {
		t.Helper()
		res := call()
		var out struct {
			JobID int64 `json:"job_id"`
		}
		if res.Code != 202 || json.Unmarshal(res.Body.Bytes(), &out) != nil || out.JobID == 0 {
			t.Fatalf("queue response: %d %s", res.Code, res.Body)
		}
		return out.JobID
	}
	first := submit()
	if id := submit(); id != first {
		t.Fatalf("duplicate job %d", id)
	}
	complete := func(id int64, title string) int64 {
		t.Helper()
		job, err := repo.Claim(ctx, false, time.Minute)
		if err != nil || job == nil || job.ID != id || !job.ScheduledFor.Equal(slot) {
			t.Fatalf("claim %v %v", job, err)
		}
		topic := learningdomain.Topic{Title: title, VocabularyTheme: title}
		draft := domain.ScheduledDraft{Draft: domain.Draft{Text: title, CreatorTelegramID: 42, Source: "ai_daily_topic", SourceKey: "daily-topic-job:" + strconv.FormatInt(id, 10)}, ScheduledAt: slot, Targets: []domain.Target{{Key: "club", Name: "Club", Destination: "test"}}}
		if err := repo.Complete(ctx, job, draft, topic); err != nil {
			t.Fatal(err)
		}
		var publicationID int64
		if err := pool.QueryRow(ctx, "SELECT publication_id FROM ai_topic_jobs WHERE id=$1", id).Scan(&publicationID); err != nil {
			t.Fatal(err)
		}
		return publicationID
	}
	oldPost := complete(first, "初稿")
	if err := repo.CancelToday(ctx, time.Date(slot.Year(), slot.Month(), slot.Day(), 0, 0, 0, 0, location), 42); err != nil {
		t.Fatal(err)
	}
	second := submit()
	if second == first {
		t.Fatal("cancelled history item overwritten")
	}
	newPost := complete(second, "新稿")
	if oldPost == newPost {
		t.Fatal("reused cancelled publication")
	}
	if _, err := pool.Exec(ctx, "INSERT INTO ai_topic_jobs(scheduled_for,status,next_attempt_at) VALUES($1,'failed',NOW())", slot.AddDate(0, 0, 2)); err != nil {
		t.Fatal(err)
	}
	if id := submit(); id != second {
		t.Fatalf("replacement target %d, want %d", id, second)
	}
	if _, err := pool.Exec(ctx, "UPDATE publications SET scheduled_at=NOW()-interval '1 second' WHERE id=$1", newPost); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE publication_deliveries SET next_attempt_at=NOW() WHERE publication_id=$1", newPost); err != nil {
		t.Fatal(err)
	}
	if delivery, err := publications.ClaimDue(ctx); err != nil || delivery != nil {
		t.Fatalf("claimed while replacing: %v %v", delivery, err)
	}
	if id := complete(second, "最終稿"); id != newPost {
		t.Fatalf("replaced publication id %d", id)
	}
	history, err := repo.History(ctx)
	if err != nil || len(history) != 3 {
		t.Fatalf("history %v %v", history, err)
	}
	if delivery, err := publications.ClaimDue(ctx); err != nil || delivery == nil || delivery.PublicationID != newPost || delivery.Text != "最終稿" {
		t.Fatalf("did not claim new content: %v %v", delivery, err)
	}
	if res := call(); res.Code != 409 {
		t.Fatalf("started delivery accepted: %d %s", res.Code, res.Body)
	}
}
