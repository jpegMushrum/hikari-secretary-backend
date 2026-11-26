//go:build integration

package memberapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/adapters/adminapi"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/adapters/mascotapi"
	mascotpg "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/adapters/postgres"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/httpapi"
)

type imageMemory map[string][]byte

func (s imageMemory) Put(_ context.Context, key string, data []byte, mime string) error {
	s[key] = data
	return nil
}
func (s imageMemory) Get(_ context.Context, key string) ([]byte, string, error) {
	return s[key], "image/png", nil
}
func (s imageMemory) Delete(_ context.Context, key string) error { delete(s, key); return nil }

func TestMascotImageRoutesAndRoleBoundaries(t *testing.T) {
	ctx := context.Background()
	db := testPool(t)
	repo := mascotpg.NewRepository(db)
	images := imageMemory{}
	api := &mascotapi.Handler{Repo: repo, Objects: images}
	auth := santaTestIdentities{"admin": {TelegramID: 401, FirstName: "Admin"}, "alice": {TelegramID: 402, FirstName: "Alice"}, "bob": {TelegramID: 403, FirstName: "Bob"}}
	router := httpapi.NewRouter(db, auth, map[int64]struct{}{401: {}}, nil, nil, nil, NewSeasonalRouter(db, nil, api), (&adminapi.Management{Pool: db, Mascot: api}).Router(http.NotFoundHandler()))
	const admin = "/api/v1/admin/mascot"
	const member = "/api/v1/member/mascot"
	call := func(token, method, path string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Authorization", "tma "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	check := func(w *httptest.ResponseRecorder, expected int) {
		t.Helper()
		if w.Code != expected {
			t.Fatalf("got %d %s, want %d", w.Code, w.Body.String(), expected)
		}
	}
	check(call("bob", "GET", admin, nil), 403)
	check(call("invalid", "GET", member, nil), 401)
	check(call("bob", "GET", admin+"/results/entries/1", nil), 403)
	check(call("invalid", "GET", member+"/results/entries/1", nil), 401)
	check(call("bob", "GET", member+"/results/entries/0", nil), 400)
	check(call("bob", "GET", member+"/results/entries/1", nil), 404)
	checkVisibility := func(token, base string, visible bool, until time.Time) {
		t.Helper()
		w := call(token, "GET", base+"/visibility", nil)
		check(w, 200)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil || string(fields["visible"]) != strconv.FormatBool(visible) {
			t.Fatalf("unexpected visibility: %s", w.Body.String())
		}
		raw, found := fields["submission_until"]
		if until.IsZero() {
			if found {
				t.Fatalf("unexpected deadline: %s", raw)
			}
			return
		}
		var got time.Time
		if err := json.Unmarshal(raw, &got); err != nil || !got.Equal(until) {
			t.Fatalf("deadline %s, want %s", raw, until)
		}
	}
	checkVisibility("alice", member, false, time.Time{})
	season, err := repo.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check(call("alice", "GET", member, nil), 404)
	checkVisibility("alice", member, false, time.Time{})
	checkVisibility("admin", admin, true, time.Time{})
	check(call("alice", "POST", member+"/images?season_id="+strconv.FormatInt(season.ID, 10), []byte("hidden")), 404)
	deadline := time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02")
	open := []byte(fmt.Sprintf(`{"season_id":%d,"action":"open","deadline":%q}`, season.ID, deadline))
	check(call("admin", "POST", admin, open), 204)
	until, err := time.Parse(time.RFC3339, deadline+"T00:00:00+03:00")
	if err != nil {
		t.Fatal(err)
	}
	checkVisibility("alice", member, true, until)
	checkVisibility("admin", admin, true, until)
	if w := call("admin", "POST", admin+"/test-deadline", open); w.Code != 404 && w.Code != 405 {
		t.Fatalf("local control exposed: %d", w.Code)
	}
	check(call("alice", "GET", member, nil), 200)
	path := member + "/images?season_id=" + strconv.FormatInt(season.ID, 10)
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 3, 3))); err != nil {
		t.Fatal(err)
	}
	check(call("alice", "POST", path, img.Bytes()), 403)
	_, err = db.Exec(ctx, `WITH event AS (INSERT INTO events(title,starts_at,ends_at) VALUES('Eligibility',now()+interval '1 day',now()+interval '2 days') RETURNING id)
	INSERT INTO event_registrations(event_id,person_id,registered_at) SELECT event.id,t.person_id,now() FROM event,telegram_identities t WHERE t.telegram_user_id=402`)
	if err != nil {
		t.Fatal(err)
	}
	check(call("alice", "POST", path, []byte("invalid image")), 400)
	res := call("alice", "POST", path, img.Bytes())
	check(res, 200)
	var uploaded struct{ Key string }
	if err = json.Unmarshal(res.Body.Bytes(), &uploaded); err != nil || uploaded.Key == "" {
		t.Fatalf("invalid upload response %s", res.Body.String())
	}
	check(call("alice", "GET", member+"/images/"+uploaded.Key, nil), 200)
	other := call("bob", "GET", member+"/images/"+uploaded.Key, nil)
	if other.Code != 403 && other.Code != 404 {
		t.Fatalf("private image exposed: %d", other.Code)
	}
	check(call("bob", "GET", admin+"/images/"+uploaded.Key, nil), 403)
	check(call("bob", "DELETE", member+"/images/"+uploaded.Key, nil), 404)
	check(call("alice", "DELETE", member+"/images/"+uploaded.Key, nil), 204)
	check(call("alice", "GET", member+"/images/"+uploaded.Key, nil), 404)
	res = call("alice", "POST", path, img.Bytes())
	check(res, 200)
	if err = json.Unmarshal(res.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	command := func(token, base, action string, props map[string]any) {
		t.Helper()
		props["action"] = action
		props["season_id"] = season.ID
		payload, err := json.Marshal(props)
		if err != nil {
			t.Fatal(err)
		}
		check(call(token, "POST", base, payload), 204)
	}
	command("alice", member, "submit", map[string]any{"name": "Hoshi", "description": "A star",
		"images": []map[string]any{{"key": uploaded.Key, "ai": true}}})
	var entryID int64
	if err := db.QueryRow(ctx, "SELECT id FROM mascot_entries WHERE season_id=$1", season.ID).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	if err := repo.EndTestSubmissions(ctx, season.ID); err != nil {
		t.Fatal(err)
	}
	checkVisibility("alice", member, true, time.Time{})
	checkVisibility("admin", admin, true, time.Time{})
	command("admin", admin, "moderate", map[string]any{"approved": true, "entry_id": entryID})
	command("admin", admin, "collect", map[string]any{})
	detailPath := "/results/entries/" + strconv.FormatInt(entryID, 10)
	adminDetail := call("admin", "GET", admin+detailPath, nil)
	check(adminDetail, 200)
	check(call("bob", "GET", member+detailPath, nil), 404)
	command("admin", admin, "publish", map[string]any{"round": 1})
	memberDetail := call("bob", "GET", member+detailPath, nil)
	check(memberDetail, 200)
	if bytes.Contains(memberDetail.Body.Bytes(), []byte("author")) || !bytes.Equal(adminDetail.Body.Bytes(), memberDetail.Body.Bytes()) {
		t.Fatal("both results endpoints must return the same anonymous card")
	}

	reset := []byte(fmt.Sprintf(`{"season_id":%d}`, season.ID))
	resetPath := admin + "/test-reset"
	if w := call("admin", "POST", resetPath, reset); w.Code != 404 && w.Code != 405 {
		t.Fatalf("production router exposes test reset: %d", w.Code)
	}
	router = httpapi.NewRouter(db, auth, map[int64]struct{}{401: {}}, nil, nil, nil, NewSeasonalRouter(db, nil, api),
		(&adminapi.Management{Pool: db, Mascot: api, LocalSantaReset: true}).Router(http.NotFoundHandler()))
	check(call("bob", "POST", resetPath, reset), 403)
	check(call("admin", "POST", resetPath, []byte("{}")), 400)
	check(call("admin", "POST", resetPath, reset), 204)
	check(call("admin", "POST", resetPath, reset), 409)
	v, err := repo.View(ctx, 401, true)
	if err != nil || v.Season.ID == season.ID || v.Season.Phase != "preparation" || len(v.Entries) != 0 || len(v.Rounds) != 0 {
		t.Fatalf("expected an empty new season after reset: %+v %v", v.Season, err)
	}
	check(call("alice", "GET", member, nil), 404)
	var archived bool
	err = db.QueryRow(ctx, `SELECT EXISTS(SELECT FROM mascot_seasons s JOIN mascot_entries e ON e.season_id=s.id
		JOIN mascot_images i ON i.entry_id=e.id WHERE s.id=$1 AND s.phase='closed' AND i.key=$2)`, season.ID, uploaded.Key).Scan(&archived)
	if err != nil || !archived {
		t.Fatalf("archived submission missing: %v", err)
	}
}
