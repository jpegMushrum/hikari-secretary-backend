//go:build integration

package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/migrations"
)

func managementSortTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not configured")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(ctx) })
	schema := fmt.Sprintf("list_sort_test_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	if _, err := conn.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(ctx, conn, "../../../migrations"); err != nil {
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
	t.Cleanup(pool.Close)
	return pool
}

func TestAdminListColumnSortPostgres(t *testing.T) {
	pool := managementSortTestPool(t)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO publications (id,creator_telegram_id,text,status,scheduled_at)
		VALUES (1,123,'Zulu','draft',NULL), (2,123,'Alpha','sent',NOW());
		INSERT INTO people(full_name) VALUES (NULL), ('Anna');
		INSERT INTO telegram_identities(person_id, telegram_user_id, display_name,last_seen_at)
		VALUES (1, 100, 'Zoe', NOW());
		INSERT INTO events(title,starts_at,ends_at) VALUES ('Апрель',NOW(),NOW()+interval '1 day');
		INSERT INTO feedback(person_id,event_id,text) VALUES (1,NULL,'Zulu'), (2,1,'Alpha');
		INSERT INTO ai_topic_jobs(scheduled_for,status,attempts,next_attempt_at,topic) VALUES
			(NOW()+interval '1 day','failed',2,NOW(),'{"title_ru":"Zulu","title":"Aaron"}'),
			(NOW(),'retry',1,NOW(),'{"title_ru":"","title":"Alpha"}')
	`)
	if err != nil {
		t.Fatal(err)
	}
	router := (&Management{Pool: pool}).Router(http.NotFoundHandler())
	for _, tc := range []struct {
		path, field string
		firstAsc    int
	}{
		{"publications", "id", 1}, {"publications", "text", 2}, {"publications", "status", 1},
		{"publications", "created_at", 1}, {"publications", "scheduled_at", 2},
		{"feedback", "id", 1}, {"feedback", "text", 2}, {"feedback", "event_title", 2},
		{"feedback", "author_name", 2}, {"feedback", "created_at", 1},
		{"ai/history", "id", 1}, {"ai/history", "title", 2}, {"ai/history", "scheduled_for", 2},
		{"ai/history", "status", 1}, {"ai/history", "attempts", 2},
	} {
		for _, direction := range []string{"asc", "desc"} {
			t.Run(tc.path+"/"+tc.field+"/"+direction, func(t *testing.T) {
				url := "/api/v1/admin/" + tc.path + "?sort=" + tc.field + "&order=" + direction
				res := httptest.NewRecorder()
				router.ServeHTTP(res, httptest.NewRequest("GET", url, nil))
				if res.Code != 200 {
					t.Fatalf("HTTP %d: %s", res.Code, res.Body)
				}
				var data struct {
					Items []struct{ ID int }
					Sort  struct{ Field, Order string }
				}
				if err := json.Unmarshal(res.Body.Bytes(), &data); err != nil {
					t.Fatal(err)
				}
				first := tc.firstAsc
				if direction == "desc" {
					first = 3 - first
				}
				if len(data.Items) != 2 || data.Items[0].ID != first || data.Items[1].ID != 3-first ||
					data.Sort.Field != tc.field || data.Sort.Order != direction {
					t.Fatal(res.Body.String())
				}
			})
		}
	}
}
