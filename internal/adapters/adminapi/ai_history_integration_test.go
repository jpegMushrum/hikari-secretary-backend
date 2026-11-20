//go:build integration

package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestAIHistoryPublicationStatePostgres(t *testing.T) {
	pool := managementSortTestPool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO publications (id, creator_telegram_id, status)
		VALUES (10, 1, 'scheduled'), (11, 1, 'sent');
		INSERT INTO ai_topic_jobs (id, scheduled_for, status, next_attempt_at, publication_id)
		VALUES (1, NOW(), 'scheduled', NOW(), NULL),
		       (2, NOW()+interval '1 day', 'scheduled', NOW(), 11),
		       (3, NOW()+interval '2 days', 'scheduled', NOW(), 10)`)
	if err != nil {
		t.Fatal(err)
	}
	router := (&Management{Pool: pool}).Router(http.NotFoundHandler())
	get := func(path string, status int) map[string]any {
		t.Helper()
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/admin/ai/history"+path, nil))
		if res.Code != status {
			t.Fatalf("GET %s: HTTP %d, %s", path, res.Code, res.Body.String())
		}
		var data map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, test := range []struct {
		order string
		want  []any
	}{{"asc", []any{"scheduled", "sent", nil}}, {"desc", []any{"sent", "scheduled", nil}}} {
		items := get("?sort=publication_status&status=scheduled&order="+test.order, 200)["items"].([]any)
		var got []any
		for _, item := range items {
			status, ok := item.(map[string]any)["publication_status"]
			if !ok {
				t.Fatal("missing publication_status")
			}
			got = append(got, status)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("order %s: got %v, want %v", test.order, got, test.want)
		}
		if !reflect.DeepEqual(items[len(items)-1], get("/1", 200)) {
			t.Fatal("list and detail differ for a job without a linked publication")
		}
	}
	if _, err := pool.Exec(ctx, "UPDATE publications SET status='failed' WHERE id=10"); err != nil {
		t.Fatal(err)
	}
	detail := get("/3", 200)
	if detail["status"] != "scheduled" || detail["publication_status"] != "failed" {
		t.Fatalf("unexpected job and publication states: %v", detail)
	}
	get("/42", 404)
	get("/0", 400)
}
