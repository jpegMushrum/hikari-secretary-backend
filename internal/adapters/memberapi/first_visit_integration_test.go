//go:build integration

package memberapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/adapters/botapi"
	publishingpg "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/adapters/postgres"
	publishing "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/application"
)

func TestFirstVisitRegistrationNotice(t *testing.T) {
	pool := testPool(t)
	const admin = "club_admin"
	member := NewSeasonalRouter(pool, nil, nil, admin)
	bot := botapi.NewRouter(pool, nil, publishing.NewService(publishingpg.NewRepository(pool)), botapi.RouterOptions{FirstVisitAdminUsername: admin})
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO events(title,starts_at,ends_at) SELECT 'Meeting',now()+interval '2 days',now()+interval '3 days' FROM generate_series(1,2);
		INSERT INTO event_publication_references(event_id,provider,external_publication_id,published)
		SELECT id,'legacy_sqlite',id::text,true FROM events`); err != nil {
		t.Fatal(err)
	}
	setProfile := func(user int64, student bool) {
		t.Helper()
		resp := testRequest(member, user, user, "PUT", "/api/v1/member/profile",
			fmt.Sprintf(`{"full_name":"Test Member","is_itmo_student":%t,"is_russian_citizen":true}`, student))
		if resp.Code != 204 {
			t.Fatal(resp.Code, resp.Body)
		}
	}
	registerBot := func(user, event int64) *httptest.ResponseRecorder {
		return testRequest(bot, user, user, "POST", fmt.Sprintf("/internal/bot/v1/events/%d/registrations", event),
			fmt.Sprintf(`{"telegram_user_id":%d}`, user))
	}
	check := func(resp *httptest.ResponseRecorder, status int, contact string) {
		t.Helper()
		if resp.Code != status {
			t.Fatalf("got %d %s, want %d", resp.Code, resp.Body, status)
		}
		if status == 204 {
			if resp.Body.Len() != 0 {
				t.Fatalf("unexpected body: %s", resp.Body)
			}
			return
		}
		var result map[string]any
		if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		got, exists := result["first_visit_admin_username"]
		if exists != (contact != "") || (exists && got != contact) {
			t.Fatalf("unexpected notice: %s", resp.Body)
		}
		if registered, ok := result["registered"]; ok && registered != true {
			t.Fatalf("unexpected registration: %s", resp.Body)
		}
	}
	const path = "/api/v1/member/events/1/registration"
	const firstRegistration = `{"reminder_offset_minutes":null,"profile":{"full_name":"New Member","is_itmo_student":false,"is_russian_citizen":false}}`
	// An invalid submission does not consume the notice. The first registration
	// uses the profile saved in the same transaction.
	check(testRequest(member, 41, 41, "POST", path, `{"reminder_offset_minutes":null}`), 409, "")
	// Make queue insertion fail after writing the registration. The retry
	// must still be a first visit and must still require the original profile.
	if _, err := pool.Exec(context.Background(), "ALTER TABLE outbox ADD CONSTRAINT simulate_enqueue_failure CHECK (topic <> 'registration.first_visit')"); err != nil {
		t.Fatal(err)
	}
	check(testRequest(member, 41, 41, "POST", path, firstRegistration), 500, "")
	if _, err := pool.Exec(context.Background(), "ALTER TABLE outbox DROP CONSTRAINT simulate_enqueue_failure"); err != nil {
		t.Fatal(err)
	}
	check(testRequest(member, 41, 41, "POST", path, `{"reminder_offset_minutes":null}`), 409, "")
	check(testRequest(member, 41, 41, "POST", path, firstRegistration), 200, admin)
	check(testRequest(member, 41, 41, "POST", path, firstRegistration), 204, "")
	check(testRequest(member, 41, 41, "DELETE", path, ""), 204, "")
	// Even a cancelled registration counts as history when registering through the other interface.
	check(registerBot(41, 2), 200, "")
	check(registerBot(41, 1), 200, "")

	setProfile(42, true)
	check(registerBot(42, 1), 200, "")
	setProfile(43, false)
	check(registerBot(43, 1), 200, admin)
	check(registerBot(43, 1), 200, "")
	check(testRequest(NewRouter(pool), 44, 44, "POST", path, firstRegistration), 204, "")
	setProfile(47, true)
	check(testRequest(member, 47, 47, "POST", path, `{"reminder_offset_minutes":null}`), 204, "")

	postBot := func(path string) *httptest.ResponseRecorder {
		return testRequest(bot, 0, 0, "POST", "/internal/bot/v1"+path, "")
	}
	const claim = "/outbox/notifications/claim"
	var first struct {
		ID       int64  `json:"id"`
		To       int64  `json:"telegram_user_id"`
		Username string `json:"first_visit_admin_username"`
		Topic    string `json:"topic"`
		Attempts int    `json:"attempts"`
	}
	res := postBot(claim)
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &first) != nil || first.Topic != "registration.first_visit" ||
		first.ID == 0 || first.To != 41 || first.Username != admin || first.Attempts != 1 {
		t.Fatalf("claim: %d %s", res.Code, res.Body)
	}
	check(postBot(claim), 204, "")
	// Bot startup recovers a job interrupted after claim; successful
	// acknowledgement retires the same job from further delivery.
	check(postBot("/deliveries/recover"), 200, "")
	retry := first
	res = postBot(claim)
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &retry) != nil || retry.ID != first.ID || retry.Attempts != 2 {
		t.Fatalf("recover: %d %s", res.Code, res.Body)
	}
	check(postBot(fmt.Sprintf("/outbox/notifications/%d/succeeded", first.ID)), 200, "")
	check(postBot(claim), 204, "")

	// Two different events in different interfaces share one first visit.
	setProfile(45, false)
	start := make(chan struct{})
	botResponse := make(chan *httptest.ResponseRecorder, 1)
	memberResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		<-start
		botResponse <- registerBot(45, 2)
	}()
	go func() {
		<-start
		memberResponse <- testRequest(member, 45, 45, "POST", path, `{"reminder_offset_minutes":null}`)
	}()
	close(start)
	b, m := <-botResponse, <-memberResponse
	if m.Code == 200 {
		check(m, 200, admin)
		check(b, 200, "")
		check(postBot(claim), 200, admin)
	} else {
		check(m, 204, "")
		check(b, 200, admin)
	}
	check(postBot(claim), 204, "")

	// While an event update prevents registration, the bot must leave the
	// person unlocked for other interfaces. Reversing these locks can deadlock.
	setProfile(46, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	edit, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer edit.Rollback(context.Background())
	var holderPID int
	if err := edit.QueryRow(ctx, "SELECT pg_backend_pid() FROM events WHERE id=1 FOR UPDATE").Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	go func() { botResponse <- registerBot(46, 1) }()
	for {
		var waiting bool
		if err := edit.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_locks WHERE NOT granted AND $1=ANY(pg_blocking_pids(pid)))", holderPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := edit.Exec(ctx, "SELECT p.id FROM people p JOIN telegram_identities ti ON ti.person_id=p.id WHERE ti.telegram_user_id=46 FOR NO KEY UPDATE OF p NOWAIT"); err != nil {
		t.Fatal("bot locked the person before the event", err)
	}
	if err := edit.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	check(<-botResponse, 200, admin)
}
