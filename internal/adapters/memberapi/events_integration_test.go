//go:build integration

package memberapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/adapters/botapi"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemberEventLifecycle(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	router := NewRouter(pool)
	call := func(user int64, method, path, body string, status int) string {
		t.Helper()
		r := testRequest(router, user, user, method, "/api/v1/member"+path, body)
		if r.Code != status {
			t.Fatalf("%s %s: %d, want %d (%s)", method, path, r.Code, status, r.Body.String())
		}
		return r.Body.String()
	}
	// Both new Telegram users can open the miniapp. Each receives one persistent
	// person identity for registration, feedback and bot reminders.
	call(42, "GET", "/me", "", 200)
	call(43, "GET", "/me", "", 200)
	var alice int64
	if err := pool.QueryRow(ctx, "SELECT person_id FROM telegram_identities WHERE telegram_user_id=42").Scan(&alice); err != nil {
		t.Fatal(err)
	}
	mediaDir := t.TempDir()
	t.Setenv("BOT_MEDIA_DIR", mediaDir)
	img := filepath.Join(mediaDir, "photo.jpg")
	if err := os.WriteFile(img, []byte("image body"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `
 INSERT INTO events(title,starts_at,ends_at) VALUES
 ('First',now()+interval '2 days',now()+interval '3 days'),
 ('Draft',now()+interval '2 days',now()+interval '3 days'),
 ('Archive',now()-interval '3 days',now()-interval '2 days'),
 ('Legacy own',now()+interval '4 days',now()+interval '5 days');
 INSERT INTO publications(id,creator_telegram_id,text,status) VALUES (101,99,'Club invitation','sent'),(102,99,'Hidden draft','draft');
 UPDATE publications SET entities='[{"type":"text_mention","offset":0,"length":4,"user":{"id":912345,"first_name":"PRIVATE"}},{"type":"bold","offset":0,"length":4,"internal":"PRIVATE"}]',
 rich_message='{"file_unique_id":"PRIVATE","blocks":[{"type":"photo","file_id":"PRIVATE","caption":{"type":"italic","text":"Public caption","user":"PRIVATE"}},{"type":"details","summary":"Details","blocks":[{"type":"buttons","buttons":[{"text":"Visit","url":"https://example.org","callback_data":"PRIVATE"}]}]}]}' WHERE id=101;
 INSERT INTO event_publication_references(event_id,provider,external_publication_id,published) VALUES (1,'hikari_publishing','101',true),(2,'hikari_publishing','102',false),(3,'legacy_sqlite','103',true),(4,'legacy_sqlite','104',false);
 INSERT INTO publication_deliveries(publication_id,target_key,target_name,destination,status,external_id) VALUES (101,'club','Club','@hikari','sent','8')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "INSERT INTO publication_media(publication_id,position,path) VALUES(101,0,$1),(102,0,$1)", img); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "INSERT INTO event_registrations(event_id,person_id,registered_at) VALUES(4,$1,NOW()),(3,$1,NOW())", alice); err != nil {
		t.Fatal(err)
	}
	event := func(user, id int64) eventDetail {
		t.Helper()
		raw := call(user, "GET", fmt.Sprintf("/events/%d", id), "", 200)
		var detail eventDetail
		if err := json.Unmarshal([]byte(raw), &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "creator") || strings.Contains(raw, "deliveries") || strings.Contains(raw, "telegram_user_id") {
			t.Fatalf("internal fields in detail")
		}
		return detail
	}
	list := func(user int64, query string) []eventItem {
		t.Helper()
		raw := call(user, "GET", "/events"+query, "", 200)
		var result struct{ Items []eventItem }
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		return result.Items
	}
	if got := list(43, ""); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("default public listing: %+v", got)
	}
	if got := list(42, "?view=all"); len(got) != 3 {
		t.Fatalf("archive/own listing: %+v", got)
	}
	if got := list(42, "?view=registered"); len(got) != 1 || got[0].ID != 4 {
		t.Fatalf("own legacy listing: %+v", got)
	}
	for _, path := range []string{"/events/2", "/events/2/media/0", "/events/4", "/events/4/media/0"} {
		call(43, "GET", path, "", 404)
	}
	d := event(42, 1)
	if !d.CanRegister || d.ProfileComplete || len(d.ReminderOptions) == 0 || d.AnnouncementURL == nil || *d.AnnouncementURL != "https://t.me/hikari/8" || d.Announcement == nil || d.Announcement.Text != "Club invitation" {
		t.Fatalf("initial detail: %+v", d)
	}
	rawPreview := call(42, "GET", "/events/1", "", 200)
	for _, private := range []string{"PRIVATE", "file_id", "file_unique_id", "text_mention", "callback_data"} {
		if strings.Contains(rawPreview, private) {
			t.Fatalf("private Telegram field %s in member preview", private)
		}
	}
	for _, presentation := range []string{"Club invitation", "bold", "Public caption", "https://example.org", "details", "buttons"} {
		if !strings.Contains(rawPreview, presentation) {
			t.Fatalf("missing public preview data %s", presentation)
		}
	}
	if raw := call(42, "GET", "/events/1/media/0", "", 200); raw != "image body" {
		t.Fatalf("unexpected media")
	}
	call(42, "POST", "/events/1/registration", `{"reminder_offset_minutes":null}`, 409)
	call(42, "POST", "/events/2/registration", `{"reminder_offset_minutes":null}`, 404)
	call(42, "POST", "/events/1/registration", `{"reminder_offset_minutes":5,"profile":{"full_name":"Test Name","is_itmo_student":false,"is_russian_citizen":true}}`, 400)
	if event(42, 1).ProfileComplete {
		t.Fatal("invalid reminder changed profile")
	}
	call(42, "POST", "/events/1/registration", `{"reminder_offset_minutes":60,"profile":{"full_name":"Test Name","is_itmo_student":false,"is_russian_citizen":true}}`, 204)
	call(42, "POST", "/events/1/registration", `{"reminder_offset_minutes":null}`, 204)
	bot := botapi.NewRouter(pool, nil, nil)
	botReq := func(method, path, body string) (int, string) {
		w := testRequest(bot, 42, 42, method, "/internal/bot/v1"+path, body)
		return w.Code, w.Body.String()
	}
	if _, err := pool.Exec(ctx, "UPDATE events SET starts_at=NOW()+interval '59 minutes' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	call(42, "POST", "/events/1/registration", `{"reminder_offset_minutes":60}`, 204)
	if status, body := botReq("POST", "/events/101/registrations", `{"telegram_user_id":42,"reminder_offset_minutes":60}`); status != 200 {
		t.Fatal(body)
	}
	d = event(42, 1)
	if !d.ProfileComplete || d.PersonalStatus != "registered" || !d.CanManage || d.Offset == nil || *d.Offset != 60 {
		t.Fatalf("registration: %+v", d)
	}
	if event(43, 1).PersonalStatus != "not_registered" {
		t.Fatal("leaked another registration")
	}
	call(43, "PATCH", "/events/1/registration", `{"reminder_offset_minutes":null}`, 409)
	if pref := event(43, 1).PreferredOffset; pref == nil || *pref != 60 {
		t.Fatal("failed registration update changed preference")
	}
	call(42, "PATCH", "/events/1/registration", `{"reminder_offset_minutes":30}`, 204)
	if pref := event(42, 3).PreferredOffset; pref == nil || *pref != 30 {
		t.Fatal("explicit reminder not used on another event")
	}
	call(42, "PATCH", "/events/1/registration", `{"reminder_offset_minutes":null}`, 204)
	if detail := event(42, 1); detail.Offset != nil || detail.PreferredOffset != nil {
		t.Fatal("reminder is still enabled")
	}
	call(42, "PUT", "/events/1/attendance", `{"attended":true}`, 409)
	call(42, "DELETE", "/events/1/registration", "", 204)
	if event(42, 1).PersonalStatus != "not_registered" {
		t.Fatal("cancellation not reflected")
	}
	if !event(42, 3).CanMark || !event(42, 3).CanFeedback {
		t.Fatal("missing archive actions")
	}
	if event(43, 3).CanFeedback {
		t.Fatal("archive feedback available without registration")
	}
	call(42, "PATCH", "/profile", `{"reminder_offset_minutes":360,"feedback_enabled":false}`, 204)
	call(42, "PATCH", "/profile", `{"reminder_offset_minutes":5}`, 400)
	call(42, "PATCH", "/profile", `{"reminder_offset_minutes":"60"}`, 400)
	call(42, "POST", "/events/1/registration", `{"reminder_offset_minutes":null}`, 204)
	if detail := event(42, 1); detail.PreferredOffset == nil || *detail.PreferredOffset != 360 || detail.Offset != nil {
		t.Fatal("temporary fallback overwrote preference")
	}
	call(42, "DELETE", "/events/1/registration", "", 204)
	call(42, "PATCH", "/profile", `{"reminder_offset_minutes":null}`, 204)
	if raw := call(42, "GET", "/profile", "", 200); !strings.Contains(raw, `"reminder_offset_minutes":null`) || !strings.Contains(raw, `"feedback_enabled":false`) {
		t.Fatalf("independent profile preferences: %s", raw)
	}
	call(42, "PUT", "/events/3/attendance", `{"attended":false}`, 204)
	if status, body := botReq("GET", "/attendance-prompts/due", ""); status != 200 || strings.Contains(body, "103") {
		t.Fatalf("repeated attendance prompt %s", body)
	}
	if status, body := botReq("POST", "/attendance", `{"telegram_user_id":42,"publication_id":103,"attended":true}`); status != 200 || !strings.Contains(body, `"recorded":false`) {
		t.Fatalf("stale bot answer %s", body)
	}
	call(42, "PUT", "/events/3/attendance", `{"attended":true}`, 409)
	call(43, "PUT", "/events/3/attendance", `{"attended":true}`, 409)
	if event(42, 3).PersonalStatus != "missed" || event(42, 3).CanMark {
		t.Fatal("attendance not saved")
	}
	// The bot and miniapp can act on the same registration at the same time.
	// Both first submissions succeed and leave a single shared registration.
	if _, err := pool.Exec(ctx, "DELETE FROM event_registrations WHERE event_id=1"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	go func() {
		<-start
		code, _ := botReq("POST", "/events/101/registrations", `{"telegram_user_id":42,"reminder_offset_minutes":30}`)
		results <- code
	}()
	go func() {
		<-start
		results <- testRequest(router, 42, 42, "POST", "/api/v1/member/events/1/registration", `{"reminder_offset_minutes":null}`).Code
	}()
	close(start)
	for range 2 {
		code := <-results
		if code != 200 && code != 204 {
			t.Fatalf("concurrent registration: %d", code)
		}
	}
	if d := event(42, 1); !d.CanManage {
		t.Fatal("concurrent registration unavailable")
	}
	start = make(chan struct{})
	go func() {
		<-start
		code, _ := botReq("PATCH", "/events/101/registrations/42/reminder", `{"reminder_offset_minutes":30}`)
		results <- code
	}()
	go func() {
		<-start
		results <- testRequest(router, 42, 42, "DELETE", "/api/v1/member/events/1/registration", "").Code
	}()
	close(start)
	for range 2 {
		code := <-results
		if code != 200 && code != 204 && code != 409 {
			t.Fatalf("concurrent cancellation: %d", code)
		}
	}
	if d := event(42, 1); d.PersonalStatus != "not_registered" {
		t.Fatal("cancelled registration remains active")
	}
	if resp := testRequest(router, 42, 42, http.MethodGet, "/api/v1/member/events?sort=registrations", ""); resp.Code != 400 {
		t.Fatal(resp.Code)
	}
}
