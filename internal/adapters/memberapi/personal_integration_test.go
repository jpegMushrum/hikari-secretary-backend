//go:build integration

package memberapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestOwnProfileAndFeedback(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const alice, bob int64 = 1, 2
	var past, upcoming, hidden int64
	for i, id := range []*int64{&past, &upcoming, &hidden} {
		days := -2
		if i == 1 {
			days = 2
		}
		if err := pool.QueryRow(ctx, `INSERT INTO events(title,starts_at,ends_at) VALUES('Встреча',NOW()+$1::int*interval '1 day',NOW()+$1::int*interval '1 day'+interval '1 hour') RETURNING id`, days).Scan(id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO event_publication_references(event_id,provider,external_publication_id,published) VALUES($1,'legacy_sqlite',$2,$3)", *id, fmt.Sprint(*id), i != 2); err != nil {
			t.Fatal(err)
		}
	}
	router := NewRouter(pool)
	call := func(person int64, method, path, body string, status int) string {
		t.Helper()
		resp := testRequest(router, person, person+1000, method, "/api/v1/member"+path, body)
		if resp.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, resp.Code, resp.Body.String())
		}
		return resp.Body.String()
	}
	call(alice, "PATCH", "/profile", `{"feedback_enabled":false}`, 204)
	call(alice, "PUT", "/profile", `{"full_name":"А","is_russian_citizen":true,"is_itmo_student":false,"feedback_enabled":false}`, 400)
	call(alice, "PUT", "/profile", `{"full_name":"Анна Тест","is_russian_citizen":true,"is_itmo_student":false}`, 204)
	var profile memberProfile
	if err := json.Unmarshal([]byte(call(alice, "GET", "/profile", "", 200)), &profile); err != nil || profile.FullName == nil || *profile.FullName != "Анна Тест" || profile.FeedbackEnabled == nil || *profile.FeedbackEnabled {
		t.Fatalf("profile: %+v %v", profile, err)
	}
	call(bob, "POST", "/feedback", `{"text":"личный отзыв другого человека"}`, 204)
	call(alice, "POST", "/feedback", `{"text":"общий отзыв"}`, 204)
	for _, id := range []int64{hidden, upcoming} {
		call(alice, "POST", "/feedback", fmt.Sprintf(`{"text":"тест","event_id":%d}`, id), 404)
	}
	pastFeedback := fmt.Sprintf(`{"text":"отзыв о встрече","event_id":%d}`, past)
	call(alice, "POST", "/feedback", pastFeedback, 404)
	if _, err := pool.Exec(ctx, `INSERT INTO event_registrations(event_id,person_id,registered_at)
		SELECT $1,person_id,NOW() FROM telegram_identities WHERE telegram_user_id=$2`, past, alice+1000); err != nil {
		t.Fatal(err)
	}
	call(alice, "POST", "/feedback", pastFeedback, 204)
	call(bob, "POST", "/feedback", pastFeedback, 404)
	if _, err := pool.Exec(ctx, `UPDATE event_registrations SET status='cancelled',cancelled_at=NOW() WHERE event_id=$1`, past); err != nil {
		t.Fatal(err)
	}
	call(alice, "POST", "/feedback", pastFeedback, 404)
	raw := call(alice, "GET", "/feedback", "", 200)
	var result struct {
		Items []ownFeedback `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil || len(result.Items) != 2 || strings.Contains(raw, "другого человека") {
		t.Fatalf("feedback: %s %v", raw, err)
	}
	item := result.Items[0]
	if item.EventID == nil || *item.EventID != past {
		t.Fatalf("event feedback: %+v", item)
	}
	path := fmt.Sprintf("/feedback/%d", item.ID)
	call(bob, "DELETE", path, "", 404)
	call(alice, "DELETE", path, "", 204)
	call(alice, "DELETE", path, "", 404)
}
