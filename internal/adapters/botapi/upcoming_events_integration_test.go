//go:build integration

package botapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/adapters/botapi"
	interactionspostgres "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/interactions/adapters/postgres"
	interactions "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/interactions/application"
)

func TestUpcomingEventsAndReminderManagement(t *testing.T) {
	ctx := context.Background()
	pool := integrationPool(t)
	_, err := pool.Exec(ctx, `
		INSERT INTO people(full_name,is_russian_citizen,is_itmo_student) VALUES ('Alice',true,false),('Bob',true,false);
		INSERT INTO telegram_identities(person_id,telegram_user_id,display_name,last_seen_at)
			SELECT id,id,full_name,NOW() FROM people;
		INSERT INTO events(title,starts_at,ends_at)
			SELECT 'Event '||i,NOW()+i*interval '1 hour',NOW()+(i+1)*interval '1 hour'
			FROM generate_series(1,6) AS i;
		UPDATE events SET starts_at=NOW()-interval '1 hour' WHERE id=5;
		UPDATE events SET status='cancelled',cancelled_at=NOW() WHERE id=6;
		INSERT INTO event_publication_references(event_id,provider,external_publication_id,published)
			SELECT id,'hikari_publishing',id::text,id NOT IN (3,4) FROM events;
		INSERT INTO event_registrations(event_id,person_id,status,registered_at,cancelled_at,reminders_enabled,reminder_offset_minutes)
			VALUES (1,1,'registered',NOW()-interval '1 day',NULL,TRUE,60),
				(1,2,'registered',NOW()-interval '1 day',NULL,TRUE,60),
				(2,1,'cancelled',NOW()-interval '1 day',NOW(),FALSE,NULL),
				(3,1,'registered',NOW()-interval '1 day',NULL,FALSE,NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	router := botapi.NewRouter(pool, interactions.NewService(interactionspostgres.NewRepository(pool)), nil)
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/internal/bot/v1"+path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s: got %d (%s), want %d", method, path, rec.Code, rec.Body, want)
		}
		return rec
	}
	flowPath := "/flows/1/registration"
	call(http.MethodPut, flowPath, `{"publication_id":2,"stage":"reminder"}`, 200)
	call(http.MethodDelete, flowPath+"?publication_id=2", "", 400)
	call(http.MethodDelete, flowPath+"?publication_id=1&stage=reminder", "", 200)
	call(http.MethodGet, flowPath, "", 200)
	call(http.MethodPut, flowPath, `{"publication_id":2,"stage":"citizenship"}`, 200)
	call(http.MethodDelete, flowPath+"?publication_id=2&stage=reminder", "", 200)
	call(http.MethodGet, flowPath, "", 200)
	call(http.MethodDelete, flowPath+"?publication_id=2&stage=citizenship", "", 200)
	call(http.MethodGet, flowPath, "", 404)
	type event struct {
		ID         int  `json:"post_id"`
		Registered bool `json:"registered"`
		Reminders  bool `json:"reminders_enabled"`
	}
	list := func(user string) []event {
		t.Helper()
		var items []event
		rec := call(http.MethodGet, "/events?view=upcoming&telegram_id="+user, "", 200)
		if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		return items
	}
	if got := list("1"); !slices.Equal(got, []event{{1, true, true}, {2, false, false}, {3, true, false}}) {
		t.Fatalf("participant's upcoming events = %+v", got)
	}
	if got := list("999"); !slices.Equal(got, []event{{1, false, false}, {2, false, false}}) {
		t.Fatalf("new participant's upcoming events = %+v", got)
	}
	const reminder = "/events/1/registrations/1/reminder"
	for _, invalid := range []string{`{}`, `{"reminder_offset_minutes":0}`, `{"reminder_offset_minutes":120}`, `{"reminder_offset_minutes":-5}`} {
		call(http.MethodPatch, reminder, invalid, 400)
	}
	call(http.MethodPatch, "/events/1/registrations/999/reminder", `{"reminder_offset_minutes":30}`, 409)
	call(http.MethodPatch, "/events/2/registrations/1/reminder", `{"reminder_offset_minutes":30}`, 409)
	call(http.MethodPatch, reminder, `{"reminder_offset_minutes":30}`, 200)
	call(http.MethodPost, "/events/1/registrations", `{"telegram_user_id":1,"reminder_offset_minutes":null}`, 200)
	call(http.MethodPost, "/reminders/mark-sent", `{"telegram_user_id":1,"publication_id":1,"reminder_offset_minutes":60}`, 200)
	var unchangedRegistration, pending bool
	err = pool.QueryRow(ctx, `SELECT registered_at<NOW()-interval '23 hours',
		reminder_offset_minutes=30 AND reminder_sent_at IS NULL FROM event_registrations WHERE event_id=1 AND person_id=1`).Scan(&unchangedRegistration, &pending)
	if err != nil || !unchangedRegistration || !pending {
		t.Fatalf("reminder edit or stale delivery changed registration: %t %t %v", unchangedRegistration, pending, err)
	}
	call(http.MethodPost, "/reminders/mark-sent", `{"telegram_user_id":2,"publication_id":1,"reminder_offset_minutes":60}`, 200)
	var marked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_registrations
		WHERE event_id=1 AND person_id=2 AND reminder_sent_at IS NOT NULL`).Scan(&marked); err != nil || marked != 1 {
		t.Fatalf("expected due reminder for the second participant to be marked: %d %v", marked, err)
	}
	call(http.MethodPatch, reminder, `{"reminder_offset_minutes":null}`, 200)
	if got := list("1"); !got[0].Registered || got[0].Reminders {
		t.Fatalf("disabled reminder changed registration: %+v", got)
	}
	if got := list("2"); !got[0].Registered || !got[0].Reminders {
		t.Fatalf("other participant's reminder changed: %+v", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE events SET starts_at=NOW()-interval '1 second' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	call(http.MethodPatch, reminder, `{"reminder_offset_minutes":null}`, 409)
}
