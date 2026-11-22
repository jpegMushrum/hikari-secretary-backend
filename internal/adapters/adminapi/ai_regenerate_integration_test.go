//go:build integration

package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/migrations"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/requestcontext"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
)

type generateFunc func() (learningcontent.Topic, error)

func (f generateFunc) Generate(context.Context, []learningcontent.Topic, learningcontent.Options) (learningcontent.Topic, error) {
	return f()
}

func TestLatestGenerationSelectsActualJobAndSerializesRequests(t *testing.T) {
	ctx := context.Background()
	pool := managementSortTestPool(t)
	admin := &Management{Pool: pool}
	handler := admin.Router(http.NotFoundHandler())
	serve := func(method, suffix string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/admin/ai/history/latest"+suffix, nil)
		req = req.WithContext(requestcontext.WithAdministrator(ctx, telegramauth.Identity{TelegramID: 42}))
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	assertLatest := func(id int64, eligible bool) {
		t.Helper()
		res := serve("GET", "")
		var got struct {
			ID            int64
			CanRegenerate bool `json:"can_regenerate"`
		}
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &got) != nil || got.ID != id || got.CanRegenerate != eligible {
			t.Fatalf("unexpected latest job: %d %s", res.Code, res.Body)
		}
	}
	if res := serve("POST", "/regenerate"); res.Code != 503 {
		t.Fatalf("disabled generator: %d", res.Code)
	}
	admin.AIEnabled = true
	exec(`INSERT INTO ai_topic_jobs(id,scheduled_for,status,next_attempt_at)
		VALUES(100,NOW()+interval '1 day','pending',NOW())`)
	if res := serve("GET", ""); res.Code != 204 || res.Body.Len() != 0 {
		t.Fatalf("expected no generation: %d %s", res.Code, res.Body)
	}
	if res := serve("POST", "/regenerate"); res.Code != 409 {
		t.Fatalf("expected missing generation conflict: %d", res.Code)
	}
	exec(`INSERT INTO ai_topic_jobs(id,scheduled_for,status,next_attempt_at)
		VALUES(7,NOW(),'failed',NOW()),(9,NOW()-interval '1 day','failed',NOW())`)
	assertLatest(7, true)
	results := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- serve("POST", "/regenerate") }()
	}
	accepted, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		res := <-results
		switch res.Code {
		case 202:
			var got struct {
				ID       int64
				Accepted bool
			}
			if json.Unmarshal(res.Body.Bytes(), &got) != nil || got.ID != 7 || !got.Accepted {
				t.Fatalf("unexpected accepted job: %s", res.Body)
			}
			accepted++
		case 409:
			conflicts++
		default:
			t.Fatalf("unexpected response: %d %s", res.Code, res.Body)
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatalf("accepted %d, conflicts %d", accepted, conflicts)
	}
	assertLatest(7, false)
	exec(`INSERT INTO publications(id,creator_telegram_id,status,source) VALUES(11,42,'sent','ai_daily_topic');
		UPDATE ai_topic_jobs SET status='scheduled',publication_id=11 WHERE id=7`)
	assertLatest(7, false)
	if res := serve("POST", "/regenerate"); res.Code != 409 {
		t.Fatalf("replaced an older job after delivery: %d", res.Code)
	}
}

func TestRegenerateFailedAndQueuedAIJob(t *testing.T) {
	ctx := context.Background()
	pool := managementSortTestPool(t)
	repo := learningcontent.NewPostgresRepository(pool)
	_, err := pool.Exec(ctx, `INSERT INTO ai_topic_jobs(id,scheduled_for,status,next_attempt_at,last_error,failure_notified)
	VALUES(1,NOW()-interval '1 day','failed',NOW(),'balance',TRUE),
	      (2,NOW()+interval '1 day','pending',NOW()-interval '1 hour',NULL,FALSE)`)
	if err != nil {
		t.Fatal(err)
	}
	admin := &Management{Pool: pool}
	handler := admin.Router(http.NotFoundHandler())
	request := func(method, path string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/admin/ai/history/"+path, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req.WithContext(requestcontext.WithAdministrator(ctx, telegramauth.Identity{TelegramID: 42})))
		if res.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	assertEligible := func(want bool) {
		t.Helper()
		if detail := request("GET", "1", 200); detail["can_regenerate"] != want {
			t.Fatalf("unexpected eligibility: %v", detail)
		}
	}
	assertEligible(false)
	request("POST", "1/regenerate", 503)
	admin.AIEnabled = true
	assertEligible(true)
	request("POST", "0/regenerate", 400)
	request("POST", "999/regenerate", 404)
	request("POST", "latest/regenerate", 202)
	request("POST", "1/regenerate", 409)
	assertEligible(false)
	topic := learningcontent.Topic{JLPTLevel: "N3", Title: "初稿", VocabularyTheme: "first", Introduction: "An introduction", Questions: []string{"q"}, Vocabulary: []learningcontent.VocabularyItem{{Japanese: "猫", Reading: "ねこ", Translation: "cat"}}, Grammar: []learningcontent.GrammarPoint{{Pattern: "p", Explanation: "e", Example: "x", Translation: "t"}}}
	target := domain.Target{Key: "club", Name: "Club", Destination: "test"}
	runWorker := func(want string, provider generateFunc) {
		t.Helper()
		service := learningcontent.NewService(provider, repo, target, 1, 1, 1)
		worker := learningcontent.NewWorker(repo, service, time.UTC, "", 0, 5, time.Second, slog.Default())
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); worker.Run(runCtx) }()
		defer func() { cancel(); <-done }()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			var state string
			if err := pool.QueryRow(ctx, "SELECT status FROM ai_topic_jobs WHERE id=1").Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("job did not reach %s", want)
	}
	runWorker("scheduled", func() (learningcontent.Topic, error) { return topic, nil })
	assertEligible(true)
	var id, creator, delivery int64
	var scheduled time.Time
	if err := pool.QueryRow(ctx, `SELECT p.id,creator_telegram_id,p.scheduled_at,d.id FROM publications p JOIN publication_deliveries d ON d.publication_id=p.id`).Scan(&id, &creator, &scheduled, &delivery); err != nil {
		t.Fatal(err)
	}
	if creator != 42 || time.Since(scheduled) > time.Minute {
		t.Fatalf("unexpected recovered schedule: %d %v", creator, scheduled)
	}
	request("POST", "1/regenerate", 202)
	runWorker("failed", func() (learningcontent.Topic, error) {
		return learningcontent.Topic{}, errors.New("provider unavailable")
	})
	assertEligible(true)
	var original string
	if err := pool.QueryRow(ctx, `SELECT topic->>'title' FROM ai_topic_jobs WHERE id=1`).Scan(&original); err != nil || original != topic.Title {
		t.Fatalf("lost original: %q %v", original, err)
	}
	request("POST", "1/regenerate", 202)
	runWorker("failed", func() (learningcontent.Topic, error) { return topic, nil })
	if detail := request("GET", "1", 200); detail["last_error"] != "model repeated a conversation topic" {
		t.Fatalf("expected old topic to remain in history: %v", detail)
	}
	notices, err := repo.FailureNotices(ctx)
	if err != nil || len(notices) != 0 {
		t.Fatalf("manual failure appeared in automatic notifications: %v %v", notices, err)
	}
	if err := repo.MarkFailureNotified(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var notified bool
	if err := pool.QueryRow(ctx, "SELECT failure_notified FROM ai_topic_jobs WHERE id=1").Scan(&notified); err != nil || notified {
		t.Fatalf("stale notification acknowledgement applied: %v", err)
	}
	topic.Title = "第二稿"
	topic.VocabularyTheme = "second"
	request("POST", "1/regenerate", 202)
	runWorker("scheduled", func() (learningcontent.Topic, error) { return topic, nil })
	var matched bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM publications p JOIN publication_deliveries d ON d.publication_id=p.id
		JOIN ai_topic_jobs j ON j.publication_id=p.id WHERE j.id=1 AND p.id=$1 AND d.id=$2 AND p.scheduled_at=$3
		AND d.target_key='club' AND p.text LIKE '%'||$4||'%' AND j.topic->>'title'=$4)
		AND (SELECT count(*) FROM publications)=1 AND (SELECT status FROM ai_topic_jobs WHERE id=2)='pending'`, id, delivery, scheduled, topic.Title).Scan(&matched); err != nil || !matched {
		t.Fatalf("replacement did not preserve queue: %v", err)
	}
	// A subsequent replacement from the bot uses the same publication and archive.
	topic.Title = "修正稿"
	topic.VocabularyTheme = "new theme"
	for i := 0; i < 2; i++ {
		if ok, err := repo.ReplaceQueuedTopic(ctx, id, topic); err != nil || !ok {
			t.Fatalf("bot replacement: %v %v", ok, err)
		}
	}
	request("GET", "999/versions", 404)
	request("GET", "1/versions?page_size=1", 400)
	for _, path := range []string{"1/versions?page=2", "2/versions"} {
		if items := request("GET", path, 200)["items"].([]any); len(items) != 0 {
			t.Fatalf("unexpected versions in %s: %v", path, items)
		}
	}
	versions := request("GET", "1/versions", 200)["items"].([]any)
	if len(versions) != 3 || request("GET", "1", 200)["version_count"] != float64(3) {
		t.Fatalf("expected three saved revisions: %v", versions)
	}
	history, err := repo.History(ctx)
	if err != nil || len(history) != 3 {
		t.Fatalf("generator history: %v %v", history, err)
	}
	for i, title := range []string{"修正稿", "第二稿", "初稿"} {
		v := versions[i].(map[string]any)
		if v["version"] != float64(3-i) || v["topic"].(map[string]any)["title"] != title || history[i].Title != title {
			t.Fatalf("unexpected version order: %v %v", versions, history)
		}
	}
	request("POST", "1/regenerate", 202)
	runWorker("failed", func() (learningcontent.Topic, error) {
		_, err := pool.Exec(ctx, "UPDATE publication_deliveries SET status='publishing' WHERE id=$1", delivery)
		next := topic
		next.Title = "第三稿"
		next.VocabularyTheme = "third"
		return next, err
	})
	assertEligible(false)
	request("POST", "1/regenerate", 409)
	var unchanged bool
	if err := pool.QueryRow(ctx, `SELECT p.text LIKE '%'||$1||'%' AND j.topic->>'title'=$1 FROM ai_topic_jobs j JOIN publications p ON p.id=j.publication_id WHERE j.id=1`, topic.Title).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("claimed delivery modified: %v", err)
	}
}

func TestManualSchedulingReplayPreservesReplacement(t *testing.T) {
	ctx := context.Background()
	pool := managementSortTestPool(t)
	repo := learningcontent.NewPostgresRepository(pool)
	draft := domain.ScheduledDraft{
		Draft:   domain.Draft{Source: "ai_daily_topic", SourceKey: "manual-77", Text: "original", CreatorTelegramID: 42},
		Targets: []domain.Target{{Key: "test", Name: "Test", Destination: "test"}}, ScheduledAt: time.Now().UTC(),
	}
	first := learningcontent.Topic{Title: "original"}
	type scheduled struct {
		id  int64
		err error
	}
	results := make(chan scheduled, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		input := draft
		input.ScheduledAt = input.ScheduledAt.Add(time.Duration(i) * time.Second)
		go func() { <-start; id, err := repo.ScheduleManual(ctx, input, first); results <- scheduled{id, err} }()
	}
	close(start)
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.id != b.id {
		t.Fatalf("simultaneous manual retries: %v %v", a, b)
	}
	id := a.id
	if err := pool.QueryRow(ctx, "SELECT scheduled_at FROM publications WHERE id=$1", id).Scan(&draft.ScheduledAt); err != nil {
		t.Fatal(err)
	}
	second := learningcontent.Topic{Title: "replacement"}
	if ok, err := repo.ReplaceQueuedTopic(ctx, id, second); err != nil || !ok {
		t.Fatalf("replacement: %v %v", ok, err)
	}
	if replay, err := repo.ScheduleManual(ctx, draft, first); err != nil || replay != id {
		t.Fatalf("manual replay: %v %v", replay, err)
	}
	// While a replacement owns the job, a replay must wait without locking
	// the publication that the replacement needs next.
	held, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(ctx)
	var pid int
	if err = held.QueryRow(ctx, "SELECT pg_backend_pid() FROM ai_topic_jobs WHERE publication_id=$1 FOR UPDATE", id).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	go func() { replay, err := repo.ScheduleManual(waitCtx, draft, first); results <- scheduled{replay, err} }()
	for {
		var waiting bool
		if err = held.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = held.Exec(ctx, "SELECT id FROM publications WHERE id=$1 FOR UPDATE NOWAIT", id); err != nil {
		t.Fatal(err)
	}
	if err = held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if replay := <-results; replay.err != nil || replay.id != id {
		t.Fatalf("blocked replay: %v", replay)
	}
	draft.SourceKey = "another-publication-at-the-same-time"
	if _, err := repo.ScheduleManual(ctx, draft, first); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("slot collision: %v", err)
	}
	history, err := repo.History(ctx)
	if err != nil || len(history) != 2 || history[0].Title != second.Title || history[1].Title != first.Title {
		t.Fatalf("unexpected manual history: %v %v", history, err)
	}
	var queued int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM publications WHERE status='scheduled'").Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("unexpected number of posts: %d %v", queued, err)
	}
}

func TestConcurrentTopicSavesRejectDuplicate(t *testing.T) {
	for _, duplicate := range []string{"title", "vocabulary theme"} {
		t.Run(duplicate, func(t *testing.T) {
			ctx := context.Background()
			pool := managementSortTestPool(t)
			repo := learningcontent.NewPostgresRepository(pool)
			posts := make([]int64, 2)
			for i, title := range []string{"first original", "second original"} {
				// Two legacy queued publications still allow independent replacements.
				var id int64
				err := pool.QueryRow(ctx, `INSERT INTO publications(creator_telegram_id,text,source,status,scheduled_at)
					VALUES(42,$1,'ai_daily_topic','scheduled',$2) RETURNING id`, title, time.Now().UTC().Add(time.Duration(i)*time.Hour)).Scan(&id)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO ai_topic_jobs(scheduled_for,status,next_attempt_at,publication_id,topic)
					SELECT scheduled_at,'scheduled',NOW(),id,jsonb_build_object('title',text,'vocabulary_theme',text) FROM publications WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO publication_deliveries(publication_id,target_key,target_name,destination)
					VALUES($1,'club','Club','test')`, id); err != nil {
					t.Fatal(err)
				}
				posts[i] = id
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for i, suffix := range []string{"A", "B"} {
				topic := learningcontent.Topic{Title: "new " + suffix, VocabularyTheme: "theme " + suffix}
				if duplicate == "title" {
					topic.Title = "shared title"
				} else {
					topic.VocabularyTheme = "shared theme"
				}
				go func() { <-start; _, err := repo.ReplaceQueuedTopic(ctx, posts[i], topic); results <- err }()
			}
			close(start)
			a, b := <-results, <-results
			if a == nil {
				a, b = b, a
			}
			want := "model repeated a vocabulary theme"
			if duplicate == "title" {
				want = "model repeated a conversation topic"
			}
			if a == nil || a.Error() != want || b != nil {
				t.Fatalf("concurrent save errors: %v, %v", a, b)
			}
			var consistent bool
			err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM ai_topic_versions)=3 AND 
				(SELECT COUNT(*) FROM publications WHERE status='scheduled')=2 AND
				(SELECT COUNT(*) FROM publications p JOIN ai_topic_jobs j ON p.id=j.publication_id WHERE p.text LIKE '%'|| (j.topic->>'title') ||'%')=2`).Scan(&consistent)
			if err != nil || !consistent {
				t.Fatalf("inconsistent archive/queue: %v", err)
			}
		})
	}
}

func TestRegenerationReclaimsInterruptedAttempt(t *testing.T) {
	ctx := context.Background()
	pool := managementSortTestPool(t)
	repo := learningcontent.NewPostgresRepository(pool)
	_, err := pool.Exec(ctx, `INSERT INTO ai_topic_jobs(id,scheduled_for,status,next_attempt_at,regeneration_requested_by)
		VALUES(1,NOW(),'pending',NOW(),42)`)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := repo.Claim(ctx, false, time.Minute)
	if err != nil || previous == nil {
		t.Fatalf("first claim: %v %v", previous, err)
	}
	if another, err := repo.Claim(ctx, false, time.Minute); err != nil || another != nil {
		t.Fatalf("concurrent claim: %v %v", another, err)
	}
	if _, err = pool.Exec(ctx, "UPDATE ai_topic_jobs SET next_attempt_at=NOW() WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	current, err := repo.Claim(ctx, false, time.Minute)
	if err != nil || current == nil || current.Attempts != 2 {
		t.Fatalf("reclaimed job: %v %v", current, err)
	}
	if err := repo.MarkFailed(ctx, previous, "stale", nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, previous, domain.ScheduledDraft{}, learningcontent.Topic{}); err == nil {
		t.Fatal("stale attempt completed")
	}
	var state string
	if err := pool.QueryRow(ctx, "SELECT status FROM ai_topic_jobs WHERE id=1").Scan(&state); err != nil || state != "generating" {
		t.Fatalf("stale attempt changed state: %s %v", state, err)
	}
}

func TestRegenerationMigrationRecoversDeliveredLegacyPublication(t *testing.T) {
	pool := managementSortTestPool(t)
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for _, version := range []string{"000010_ai_topic_versions", "000009_ai_regeneration"} {
		down, err := os.ReadFile("../../../migrations/" + version + ".down.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(ctx, string(down)); err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(ctx, "DELETE FROM schema_migrations WHERE version=$1", version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = conn.Exec(ctx, `
		INSERT INTO ai_topic_jobs(id,scheduled_for,status,next_attempt_at) VALUES(1,NOW(),'failed',NOW());
		INSERT INTO ai_topic_jobs(id,scheduled_for,status,next_attempt_at,topic) VALUES
		  (2,NOW()+interval '1 day','scheduled',NOW(),'{"title_ru":"Сакура","vocabulary":["花"],"grammar":[{"pattern":"ように"}]}');
		INSERT INTO publications(id,creator_telegram_id,status,text,source,source_key)
		VALUES(22,42,'sent','original','ai_daily_topic','daily-topic-job:1');`); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Run(ctx, conn.Conn(), "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	history, err := learningcontent.NewPostgresRepository(pool).History(ctx)
	if err != nil || len(history) != 1 || history[0].Title != "Сакура" || len(history[0].Vocabulary) != 1 ||
		history[0].Vocabulary[0].Japanese != "花" || len(history[0].Grammar) != 1 || history[0].Grammar[0].Pattern != "ように" {
		t.Fatalf("legacy version omitted from generation history: %v %v", history, err)
	}
	handler := (&Management{Pool: pool, AIEnabled: true}).Router(http.NotFoundHandler())
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/admin/ai/history/1", nil))
	var job map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &job); err != nil || res.Code != 200 || job["publication_id"] != float64(22) || job["publication_status"] != "sent" || job["status"] != "scheduled" || job["can_regenerate"] != false {
		t.Fatalf("legacy publication missing from history: %v %s", err, res.Body)
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest("POST", "/api/v1/admin/ai/history/1/regenerate", nil))
	if res.Code != 409 {
		t.Fatalf("sent publication accepted for regeneration: %d %s", res.Code, res.Body)
	}
}
