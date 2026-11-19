//go:build integration

package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/adapters/botapi"
	ep "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/adapters/postgres"
	ea "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/application"
	peoplepg "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/adapters/postgres"
	people "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/application"
	pp "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/adapters/postgres"
	pa "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/migrations"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/requestcontext"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdminLifecyclePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	schema := fmt.Sprintf("admin_test_%d", time.Now().UnixNano())
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	if _, err = conn.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Run(ctx, conn, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	t.Setenv("ADMIN_MEDIA_DIR", t.TempDir())
	targets := filepath.Join(t.TempDir(), "targets.json")
	if err = os.WriteFile(targets, []byte(`{"telegram":[{"key":"test","name":"Test","chat_id":-100123}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TARGETS_PATH", targets)
	pubs := pa.NewService(pp.NewRepository(pool))
	events := ea.NewService(ep.NewRepository(pool))
	router := (&Management{Pool: pool, Publications: pubs}).Router(NewRouter(events))
	call := func(method, path string, body any, status int) map[string]any {
		t.Helper()
		var raw []byte
		if b, ok := body.([]byte); ok {
			raw = b
		} else if body != nil {
			raw, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, "/api/v1/admin"+path, bytes.NewReader(raw))
		req = req.WithContext(requestcontext.WithAdministrator(req.Context(), telegramauth.Identity{TelegramID: 123}))
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, path, res.Code, res.Body.String())
		}
		result := map[string]any{}
		_ = json.Unmarshal(res.Body.Bytes(), &result)
		return result
	}
	var img bytes.Buffer
	settings := call("GET", "/ai/settings", nil, 200)
	if settings["enabled"] != false || settings["jlpt_level"] != "N3" {
		t.Fatal(settings)
	}
	call("PUT", "/ai/settings", map[string]any{"jlpt_level": "N2", "admin_comment": "Темы для клуба"}, 200)
	var actor int64
	if err := pool.QueryRow(ctx, "SELECT updated_by FROM ai_topic_settings WHERE id=1").Scan(&actor); err != nil || actor != 123 {
		t.Fatalf("actor=%d err=%v", actor, err)
	}
	call("PUT", "/ai/settings", map[string]any{"jlpt_level": "N9"}, 400)
	call("PUT", "/ai/settings", map[string]any{"jlpt_level": "N3", "updated_by": 999}, 400)
	if _, err := pool.Exec(ctx, `INSERT INTO ai_topic_jobs(scheduled_for,status,next_attempt_at,topic) VALUES(NOW(),'scheduled',NOW(),'{"title":"Example"}'),(NOW()+INTERVAL '1 day','failed',NOW(),NULL)`); err != nil {
		t.Fatal(err)
	}
	history := call("GET", "/ai/history?status=scheduled&page_size=10", nil, 200)
	if len(history["items"].([]any)) != 1 || history["page"].(map[string]any)["total_items"] != float64(1) {
		t.Fatal(history)
	}
	if history["items"].([]any)[0].(map[string]any)["topic"].(map[string]any)["title"] != "Example" {
		t.Fatal(history)
	}
	call("GET", "/ai/history?sort=sql", nil, 400)
	call("GET", "/ai/history?status=failed&status=scheduled", nil, 400)
	call("GET", "/ai/history?order=desc;DROP", nil, 400)
	call("GET", "/ai/history?order=desc%3BDROP", nil, 400)
	if len(call("GET", "/ai/history?page=2&page_size=10", nil, 200)["items"].([]any)) != 0 {
		t.Fatal("expected empty page")
	}
	botPreferences := botapi.NewRouter(pool, nil, pubs)
	botResponse := httptest.NewRecorder()
	botPreferences.ServeHTTP(botResponse, httptest.NewRequest("GET", "/internal/bot/v1/ai-topics/preferences", nil))
	if botResponse.Code != 200 || !bytes.Contains(botResponse.Body.Bytes(), []byte(`"N2"`)) {
		t.Fatalf("shared bot preferences: %s", botResponse.Body.String())
	}
	_ = png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	media := call("POST", "/media", img.Bytes(), 201)["id"].(string)
	call("POST", "/media", []byte("not an image"), 400)
	draft := call("POST", "/publications", map[string]any{"text": "Test", "media_ids": []string{media}, "target_keys": []string{"test"}}, 201)
	pub := int64(draft["id"].(float64))
	workspace := call("GET", "/event-workspace?status=draft&sort=title&order=asc", nil, 200)
	items := workspace["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["kind"] != "draft" || items[0].(map[string]any)["starts_at"] != nil {
		t.Fatal(workspace)
	}
	if len(call("GET", "/event-workspace?starts_from="+time.Now().UTC().Format(time.RFC3339), nil, 200)["items"].([]any)) != 0 {
		t.Fatal("undated draft included in date range")
	}
	call("GET", "/event-workspace?status=draft&status=active", nil, 400)
	call("GET", "/event-workspace?sort=invalid", nil, 400)

	call("PUT", fmt.Sprintf("/publications/%d", pub), map[string]any{"text": "Updated", "media_ids": []string{media}, "target_keys": []string{"test"}}, 200)
	card := call("GET", fmt.Sprintf("/publications/%d", pub), nil, 200)
	if card["text"] != "Updated" || len(card["media_ids"].([]any)) != 1 {
		t.Fatal(card)
	}
	call("POST", "/publications", map[string]any{"text": "Test", "media_ids": []string{"../../secret"}}, 400)
	call("POST", "/publications", map[string]any{"text": "Test", "target_keys": []string{"unknown"}}, 400)
	// Give the event and its draft visibly different IDs in this isolated schema.
	if _, err := pool.Exec(ctx, "ALTER TABLE events ALTER COLUMN id RESTART WITH 987"); err != nil {
		t.Fatal(err)
	}
	event := call("POST", "/events", map[string]any{"publication_id": pub, "title": "Meeting", "starts_at": time.Now().Add(time.Hour), "ends_at": time.Now().Add(2 * time.Hour)}, 201)
	eventID := int64(event["id"].(float64))
	for _, searchID := range []int64{eventID, pub} {
		page := call("GET", fmt.Sprintf("/event-workspace?status=draft&search=%d", searchID), nil, 200)
		if rows := page["items"].([]any); len(rows) != 1 || rows[0].(map[string]any)["id"] != float64(pub) {
			t.Fatalf("draft search by %d: %v", searchID, page)
		}
	}
	workspace = call("GET", "/event-workspace?search=Meeting", nil, 200)
	if workspace["page"].(map[string]any)["total_items"] != float64(1) || workspace["items"].([]any)[0].(map[string]any)["kind"] != "draft" {
		t.Fatal("duplicate draft/event", workspace)
	}
	if len(call("GET", "/event-workspace?status=active", nil, 200)["items"].([]any)) != 0 {
		t.Fatal("draft shown as active")
	}
	if len(call("GET", "/event-workspace?page=2&page_size=10", nil, 200)["items"].([]any)) != 0 {
		t.Fatal("workspace pagination")
	}

	call("GET", fmt.Sprintf("/events/%d", eventID), nil, 200)
	call("GET", "/publications?status=draft&page_size=10", nil, 200)
	call("GET", "/publications?sort=sql", nil, 400)
	call("POST", fmt.Sprintf("/publications/%d/schedule", pub), map[string]any{}, 200)
	workspace = call("GET", "/event-workspace?search=Meeting&status=active", nil, 200)
	items = workspace["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["kind"] != "event" || items[0].(map[string]any)["id"] != float64(eventID) {
		t.Fatal("scheduled draft did not become event", workspace)
	}
	if len(call("GET", "/event-workspace?status=draft", nil, 200)["items"].([]any)) != 0 {
		t.Fatal("scheduled publication still shown as draft")
	}
	for _, path := range []string{"/events", "/event-workspace"} {
		page := call("GET", fmt.Sprintf("%s?search=%d", path, eventID), nil, 200)
		if rows := page["items"].([]any); len(rows) != 1 || rows[0].(map[string]any)["id"] != float64(eventID) {
			t.Fatalf("search by ID on %s: %v", path, page)
		}
	}

	call("PUT", fmt.Sprintf("/publications/%d", pub), map[string]any{"text": "Too late"}, 409)
	delivery, err := pubs.ClaimDue(ctx)
	if err != nil || delivery == nil || len(delivery.MediaPaths) != 1 {
		t.Fatalf("delivery=%+v error=%v", delivery, err)
	}
	var person int64
	if err = pool.QueryRow(ctx, "INSERT INTO people(full_name) VALUES('Participant') RETURNING id").Scan(&person); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "INSERT INTO telegram_identities(person_id,telegram_user_id,username,display_name,last_seen_at) VALUES($1,456,'club_member','Screen Name',NOW())", person); err != nil {
		t.Fatal(err)
	}
	repo := peoplepg.NewRepository(pool)
	for _, term := range []string{"ticip", "screen", "CLUB_MEMBER", "@club_member", fmt.Sprint(person), "456", "club%member"} {
		result, err := repo.ListPeople(ctx, people.ListPeopleQuery{Search: term, Page: pagination.Page{Number: 1, Size: 10}})
		if err != nil {
			t.Fatal(err)
		}
		if term == "club%member" {
			if result.TotalItems != 0 {
				t.Fatal("search treated literal % as wildcard")
			}
		} else if result.TotalItems != 1 || len(result.Items) != 1 || result.Items[0].ID != person {
			t.Errorf("search %q: %+v", term, result)
		}
	}
	if _, err = pool.Exec(ctx, "INSERT INTO event_registrations(event_id,person_id,registered_at) VALUES($1,$2,NOW())", eventID, person); err != nil {
		t.Fatal(err)
	}
	var feedback int64
	if err = pool.QueryRow(ctx, "INSERT INTO feedback(person_id,text) VALUES($1,'General feedback') RETURNING id", person).Scan(&feedback); err != nil {
		t.Fatal(err)
	}
	feedbackPage := call("GET", "/feedback?kind=general", nil, 200)
	feedbackItem := feedbackPage["items"].([]any)[0].(map[string]any)
	if feedbackItem["author_name"] != "Participant" || feedbackItem["event_title"] != nil {
		t.Fatal(feedbackItem)
	}
	call("GET", fmt.Sprintf("/feedback/%d", feedback), nil, 200)
	var eventFeedback int64
	if err := pool.QueryRow(ctx, "INSERT INTO feedback(person_id,event_id,text) VALUES($1,$2,'Meeting feedback') RETURNING id", person, eventID).Scan(&eventFeedback); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		total int
	}{
		{"search=mEEting", 1},
		{fmt.Sprintf("search=%d", eventID), 1},
		{fmt.Sprintf("search=%d", eventFeedback), 1},
		{"search=@CLUB_MEMBER", 2},
		{"search=club_member", 2},
		{"search=456", 2},
		{"search=ticip", 2},
		{"search=screen", 2},
		{fmt.Sprintf("search=%d", person), 2},
		{fmt.Sprintf("search=456&event_id=%d&author_id=%d", eventID, person), 1},
		{"search=meeting&kind=general", 0},
		{"search=0", 0},
		{"search=%25", 0},
	} {
		page := call("GET", "/feedback?"+tc.query, nil, 200)
		if len(page["items"].([]any)) != tc.total || page["page"].(map[string]any)["total_items"] != float64(tc.total) {
			t.Errorf("feedback filter %q: %v", tc.query, page)
		}
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_notification() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test failure'; END $$; CREATE TRIGGER reject_notification BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION reject_notification()`); err != nil {
		t.Fatal(err)
	}
	call("POST", fmt.Sprintf("/events/%d/cancel", eventID), nil, 500)
	var state string
	if err = pool.QueryRow(ctx, "SELECT status FROM events WHERE id=$1", eventID).Scan(&state); err != nil || state != "active" {
		t.Fatalf("rollback state=%s err=%v", state, err)
	}
	if _, err = pool.Exec(ctx, "DROP TRIGGER reject_notification ON outbox"); err != nil {
		t.Fatal(err)
	}
	cancelled := call("POST", fmt.Sprintf("/events/%d/cancel", eventID), nil, 200)
	if cancelled["notifications_queued"] != float64(1) {
		t.Fatal(cancelled)
	}
	call("POST", fmt.Sprintf("/events/%d/cancel", eventID), nil, 409)
	var count int
	if err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM outbox WHERE topic='event.cancelled'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("outbox=%d error=%v", count, err)
	}
	call("GET", fmt.Sprintf("/events/%d/participants", eventID), nil, 200)
	// Both adapters must use the same event write rules and outbox cancellation.
	bot := botapi.NewRouter(pool, nil, pubs)
	botPub, err := pubs.CreateDraft(ctx, domain.Draft{CreatorTelegramID: 123, Text: "Bot event"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"publication_id": botPub, "title": "Bot", "starts_at": time.Now().Add(time.Hour), "ends_at": time.Now().Add(2 * time.Hour), "creator_telegram_id": 123})
	res := httptest.NewRecorder()
	bot.ServeHTTP(res, httptest.NewRequest("POST", "/internal/bot/v1/events", bytes.NewReader(raw)))
	if res.Code != 200 {
		t.Fatalf("bot create: %d %s", res.Code, res.Body.String())
	}
	res = httptest.NewRecorder()
	bot.ServeHTTP(res, httptest.NewRequest("POST", fmt.Sprintf("/internal/bot/v1/events/%d/cancel", botPub), nil))
	if res.Code != 200 || res.Body.String() != "[]\n" {
		t.Fatalf("bot cancel: %d %s", res.Code, res.Body.String())
	}
	// Idempotent creation cannot mutate an existing draft on retry.
	first, err := pubs.CreateDraft(ctx, domain.Draft{CreatorTelegramID: 123, Text: "Original", SourceKey: "retry-key"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := pubs.CreateDraft(ctx, domain.Draft{CreatorTelegramID: 123, Text: "Changed", SourceKey: "retry-key"})
	if err != nil || first != second {
		t.Fatalf("retry: %d %d %v", first, second, err)
	}
	p, _, err := pubs.Publication(ctx, first)
	if err != nil || p["text"] != "Original" {
		t.Fatalf("retry mutated: %v %v", p, err)
	}
	// Bot draft to miniapp configuration preserves content.
	{
		original := map[string]any{"creator_id": 123, "text": "Original 😀 content", "entities": []map[string]any{{"type": "bold", "offset": 0, "length": 8}}, "rich_message": map[string]any{"blocks": []any{map[string]any{"type": "heading", "text": "Original heading"}}}, "media_paths": []string{filepath.Join(os.Getenv("ADMIN_MEDIA_DIR"), media)}}
		encoded, _ := json.Marshal(original)
		res := httptest.NewRecorder()
		bot.ServeHTTP(res, httptest.NewRequest("POST", "/internal/bot/v1/publications", bytes.NewReader(encoded)))
		if res.Code != 201 {
			t.Fatal(res.Body.String())
		}
		var result map[string]any
		_ = json.Unmarshal(res.Body.Bytes(), &result)
		id := int64(result["id"].(float64))
		path := fmt.Sprintf("/publications/%d", id)
		before := call("GET", path, nil, 200)
		if _, leaked := before["media_paths"]; leaked {
			t.Fatal("filesystem paths leaked")
		}
		imageResponse := httptest.NewRecorder()
		router.ServeHTTP(imageResponse, httptest.NewRequest("GET", "/api/v1/admin"+path+"/media/0", nil))
		if imageResponse.Code != 200 || !bytes.Equal(imageResponse.Body.Bytes(), img.Bytes()) {
			t.Fatal("preview image missing")
		}
		call("GET", path+"/media/-1", nil, 400)
		call("GET", path+"/media/99", nil, 404)
		start := time.Now().Add(2 * time.Hour)
		end := start.Add(time.Hour)
		settings := map[string]any{"target_keys": []string{"test"}, "event": map[string]any{"title": "From bot", "starts_at": start, "ends_at": end}}
		after := call("PUT", path+"/settings", settings, 200)
		call("PUT", path+"/settings", settings, 200) // retry updates same event
		for _, key := range []string{"text", "entities", "rich_message", "media", "source"} {
			a, _ := json.Marshal(before[key])
			b, _ := json.Marshal(after[key])
			if !bytes.Equal(a, b) {
				t.Fatalf("changed %s", key)
			}
		}
		call("PUT", path+"/settings", map[string]any{"text": "overwrite", "target_keys": []string{"test"}}, 400)
		call("PUT", path+"/settings", map[string]any{"target_keys": []string{"unknown"}}, 400)
		call("PUT", path+"/settings", map[string]any{"target_keys": []string{"test", "test"}}, 400)
		var linked int
		var configuredID int64
		if err := pool.QueryRow(ctx, "SELECT count(*),min(event_id) FROM event_publication_references WHERE external_publication_id=$1", fmt.Sprint(id)).Scan(&linked, &configuredID); err != nil || linked != 1 {
			t.Fatalf("links=%d err=%v", linked, err)
		}
		// Failure after event changes must roll back both event and destinations.
		if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_target_change() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rollback'; END $$; CREATE TRIGGER reject_target_change BEFORE INSERT ON publication_deliveries FOR EACH ROW EXECUTE FUNCTION reject_target_change()`); err != nil {
			t.Fatal(err)
		}
		settings["event"].(map[string]any)["title"] = "Must roll back"
		call("PUT", path+"/settings", settings, 500)
		if _, err := pool.Exec(ctx, "DROP TRIGGER reject_target_change ON publication_deliveries; DROP FUNCTION reject_target_change()"); err != nil {
			t.Fatal(err)
		}
		restored := call("GET", path, nil, 200)
		if restored["event"].(map[string]any)["title"] != "From bot" || len(restored["deliveries"].([]any)) != 1 {
			t.Fatal(restored)
		}
		call("POST", path+"/schedule", map[string]any{"scheduled_at": start}, 409)
		if ok, err := pubs.Schedule(ctx, id, end); err != nil || ok {
			t.Fatalf("bot accepted late schedule: %v %v", ok, err)
		}
		call("POST", path+"/schedule", map[string]any{}, 200)
		call("PUT", path+"/settings", settings, 409)
		call("POST", fmt.Sprintf("/events/%d/cancel", configuredID), nil, 200)
		// A stored path outside the approved media roots must never be served.
		outside := filepath.Join(t.TempDir(), "private.png")
		if err := os.WriteFile(outside, img.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "UPDATE publication_media SET path=$2 WHERE publication_id=$1", id, outside); err != nil {
			t.Fatal(err)
		}
		call("GET", path+"/media/0", nil, 404)
	}
	for i := 0; i < 6; i++ {
		pid, err := pubs.CreateDraft(ctx, domain.Draft{CreatorTelegramID: 123, Text: "Capacity"})
		if err != nil {
			t.Fatal(err)
		}
		want := 201
		if i == 5 {
			want = 409
		}
		call("POST", "/events", map[string]any{"publication_id": pid, "title": "Capacity", "starts_at": time.Now().Add(time.Hour), "ends_at": time.Now().Add(2 * time.Hour)}, want)
	}
	_ = http.MethodGet
}
