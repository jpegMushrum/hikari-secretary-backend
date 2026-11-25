//go:build integration

package memberapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/adapters/adminapi"
	santapg "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/santa/adapters/postgres"
	santa "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/santa/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/httpapi"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
)

type santaTestIdentities map[string]telegramauth.Identity

func (identities santaTestIdentities) Validate(token string) (telegramauth.Identity, error) {
	id, ok := identities[token]
	if !ok {
		return id, telegramauth.ErrInvalidInitData
	}
	return id, nil
}

func TestSantaRoutesProtectRolesDraftsAndAssignments(t *testing.T) {
	pool := testPool(t)
	service := santapg.NewRepository(pool, []int64{41})
	auth := santaTestIdentities{
		"admin": {TelegramID: 41, FirstName: "Admin"},
		"alice": {TelegramID: 42, FirstName: "Alice", Username: "alice_club"},
		"bob":   {TelegramID: 43, FirstName: "Bob"},
		"chen":  {TelegramID: 44, FirstName: "Chen"},
	}
	handler := httpapi.NewRouter(pool, auth, map[int64]struct{}{41: {}}, nil, nil, nil,
		NewRouterWithSanta(pool, service), (&adminapi.Management{Pool: pool, Santa: service}).Router(http.NotFoundHandler()))

	const admin = "/api/v1/admin/santa"
	const member = "/api/v1/member/santa"
	call := func(token, method, path string, data any, status int, target any) {
		t.Helper()
		body, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "tma "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s as %s: got %d %s, want %d", method, path, token, w.Code, w.Body, status)
		}
		if target != nil {
			if err := json.Unmarshal(w.Body.Bytes(), target); err != nil {
				t.Fatal(err)
			}
		}
	}

	call("", "GET", member, nil, 401, nil)
	call("invalid", "GET", member, nil, 401, nil)
	call("alice", "GET", admin, nil, 403, nil)
	call("alice", "POST", admin, map[string]any{"action": "open"}, 403, nil)
	season, err := service.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var visibility map[string]any
	call("alice", "GET", member+"/visibility", nil, 200, &visibility)
	if visibility["visible"] != false || len(visibility) != 1 {
		t.Fatalf("preparation leaked to member: %v", visibility)
	}
	call("alice", "GET", member, nil, 404, nil)
	adminCmd := func(action string, fields map[string]any) {
		t.Helper()
		var v santa.AdminView
		call("admin", "GET", admin, nil, 200, &v)
		if fields == nil {
			fields = map[string]any{}
		}
		fields["season_id"], fields["revision"], fields["action"] = season.ID, v.Season.Revision, action
		call("admin", "POST", admin, fields, 204, nil)
	}
	adminCmd("settings", map[string]any{"budget": "500", "registration_until": time.Now().Add(24 * time.Hour), "exchange_at": time.Now().Add(72 * time.Hour), "reminder_minutes": 60})
	adminCmd("open", nil)
	call("alice", "POST", member, map[string]any{"season_id": season.ID, "action": "join", "person_id": 84, "wishes": "forged"}, 400, nil)
	for _, who := range []string{"alice", "bob", "chen"} {
		call(who, "POST", member, map[string]any{"season_id": season.ID, "action": "join", "wishes": who + " wish"}, 204, nil)
	}
	var roster santa.AdminView
	call("admin", "GET", admin, nil, 200, &roster)
	contacts := make(map[int64]*string)
	for _, person := range roster.Participants {
		contacts[person.TelegramID] = person.Username
	}
	if username := contacts[42]; username == nil || *username != "alice_club" {
		t.Fatal("admin roster missing participant's Telegram username")
	}
	if username, ok := contacts[43]; !ok || username != nil {
		t.Fatal("admin roster missing Telegram ID fallback")
	}
	adminCmd("shuffle", nil)
	var draft santa.MemberView
	call("alice", "GET", member, nil, 200, &draft)
	if draft.Recipient != nil || draft.Participation == nil || draft.Participation.Wishes != "alice wish" {
		t.Fatalf("unexpected member draft view: %+v", draft)
	}
	var raw map[string]json.RawMessage
	call("alice", "GET", member, nil, 200, &raw)
	if _, ok := raw["pairs"]; ok {
		t.Fatal("member route exposed pair list")
	}
	if _, ok := raw["participants"]; ok {
		t.Fatal("member route exposed participant list")
	}
	adminCmd("confirm", nil)
	call("admin", "GET", admin, nil, 200, &roster)
	byID := make(map[int64]santa.AdminParticipant)
	for _, person := range roster.Participants {
		byID[person.PersonID] = person
	}
	for _, pair := range roster.Pairs {
		giver, want := byID[pair.GiverID], byID[pair.RecipientID]
		for token, identity := range auth {
			if identity.TelegramID != giver.TelegramID {
				continue
			}
			var view santa.MemberView
			call(token, "GET", member, nil, 200, &view)
			got := view.Recipient
			if got == nil || got.PersonID != want.PersonID || got.TelegramID != want.TelegramID ||
				(got.Username == nil) != (want.Username == nil) || (got.Username != nil && *got.Username != *want.Username) {
				t.Fatalf("contact does not match assigned recipient for %s", token)
			}
		}
	}
	var published santa.MemberView
	call("alice", "GET", member, nil, 200, &published)
	if published.Recipient == nil || published.Recipient.PersonID == draft.Participation.PersonID || published.Recipient.Wishes == "" {
		t.Fatal("missing or invalid own recipient after confirmation")
	}
	description := "hand-painted mug"
	call("alice", "POST", member, map[string]any{"action": "gift_description", "season_id": season.ID, "recipient_id": published.Recipient.PersonID, "gift_description": description}, 204, nil)
	call("alice", "GET", member, nil, 200, &published)
	if published.Participation.GiftDescription != description {
		t.Fatal("giver cannot read own saved gift description")
	}
	for _, other := range []string{"bob", "chen"} {
		var privateView map[string]json.RawMessage
		call(other, "GET", member, nil, 200, &privateView)
		for _, key := range []string{"telegram_id", "username"} {
			if bytes.Contains(privateView["participation"], []byte(`"`+key+`"`)) {
				t.Fatalf("member participation unexpectedly includes %s", key)
			}
		}
		if bytes.Contains(privateView["recipient"], []byte("gift_description")) || bytes.Contains(privateView["participation"], []byte(description)) {
			t.Fatal("member view disclosed another participant's prepared gift")
		}
	}
	var before santa.AdminView
	call("admin", "GET", admin, nil, 200, &before)
	call("admin", "POST", admin, map[string]any{"action": "pause", "season_id": season.ID, "revision": before.Season.Revision}, 400, nil)
	call("alice", "GET", member, nil, 200, nil)
	var after santa.AdminView
	call("admin", "GET", admin, nil, 200, &after)
	if after.Season.Status != "open" || after.Season.Revision != before.Season.Revision {
		t.Fatal("rejected pause changed season")
	}
	// Existing databases can still contain seasons paused by an earlier release.
	if _, err := pool.Exec(context.Background(), "UPDATE santa_seasons SET status='paused' WHERE id=$1", season.ID); err != nil {
		t.Fatal(err)
	}
	call("alice", "GET", member, nil, 404, nil)
	call("alice", "POST", member, map[string]any{"action": "wishes", "season_id": season.ID, "wishes": "changed"}, 404, nil)
	adminCmd("open", nil)
	call("alice", "GET", member, nil, 200, nil)
	var resumed santa.AdminView
	call("admin", "GET", admin, nil, 200, &resumed)
	var foundGift bool
	for _, participant := range resumed.Participants {
		if participant.PersonID == published.Participation.PersonID {
			foundGift = participant.GiftDescription == description
		}
	}
	if !foundGift {
		t.Fatal("organizer cannot review giver's prepared gift")
	}
	reset := map[string]any{"season_id": season.ID, "revision": resumed.Season.Revision}
	call("admin", "POST", admin, map[string]any{"action": "close", "season_id": season.ID, "revision": resumed.Season.Revision}, 400, nil)
	call("admin", "POST", admin+"/reset", reset, 404, nil)
	handler = httpapi.NewRouter(pool, auth, map[int64]struct{}{41: {}}, nil, nil, nil,
		NewRouterWithSanta(pool, service), (&adminapi.Management{Pool: pool, Santa: service, LocalSantaReset: true}).Router(http.NotFoundHandler()))
	call("", "POST", admin+"/reset", reset, 401, nil)
	call("alice", "POST", admin+"/reset", reset, 403, nil)
	call("admin", "POST", admin+"/reset", map[string]any{"season_id": season.ID, "revision": resumed.Season.Revision - 1}, 409, nil)
	call("admin", "POST", admin+"/reset", reset, 204, nil)
	call("admin", "POST", admin+"/reset", reset, 404, nil)
	var fresh santa.AdminView
	call("admin", "GET", admin, nil, 200, &fresh)
	if fresh.Season.ID == season.ID || fresh.Season.Status != "preparation" || fresh.Season.Confirmed ||
		len(fresh.Participants) != 0 || len(fresh.Pairs) != 0 || fresh.Season.Budget != resumed.Season.Budget ||
		!fresh.Season.ExchangeAt.Equal(*resumed.Season.ExchangeAt) || !fresh.Season.RegistrationUntil.Equal(*resumed.Season.RegistrationUntil) ||
		fresh.Season.ReminderMinutes != resumed.Season.ReminderMinutes {
		t.Fatalf("unexpected reset state: %+v", fresh)
	}
}
