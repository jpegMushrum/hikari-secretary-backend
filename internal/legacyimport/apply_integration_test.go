//go:build integration

package legacyimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/migrations"
)

func TestApplyPreservesSourceAndPostCutoverChanges(t *testing.T) {
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
	schema := fmt.Sprintf("import_test_%d", time.Now().UnixNano())
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	if _, err = conn.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Run(ctx, conn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	target := dsn
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		target = parsed.String()
	} else {
		target += " search_path=" + schema
	}
	source := createLegacySQLite(t, false)
	legacy, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`INSERT INTO posts(id,creator_id,text,entities_json,rich_message_json,source,status,scheduled_at,created_at) VALUES(77,100,'Pending post','[]','','admin','scheduled','2099-01-01T10:00:00Z','2026-10-01T10:00:00Z');
	INSERT INTO deliveries(id,post_id,platform,target_key,target_name,destination,status,attempts,next_attempt_at) VALUES(77,77,'telegram','test','Test','-100','pending',0,'2099-01-01T10:00:00Z');`)
	legacy.Close()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Apply(ctx, source, target)
	if err != nil || !report.Valid || !report.Applied {
		t.Fatalf("apply: %+v %v", report, err)
	}
	for _, table := range []string{"people", "telegram_identities", "events", "event_registrations", "attendance", "feedback"} {
		var count int
		if err = conn.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var pending int
	if err = conn.QueryRow(ctx, "SELECT COUNT(*) FROM publications p JOIN publication_deliveries d ON d.publication_id=p.id WHERE p.id=77 AND p.status='scheduled' AND d.status='pending'").Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending delivery not imported: %d %v", pending, err)
	}
	if _, err = conn.Exec(ctx, "UPDATE people SET full_name='Changed in PostgreSQL'"); err != nil {
		t.Fatal(err)
	}
	if _, err = Apply(ctx, source, target); err != nil {
		t.Fatal(err)
	}
	var name string
	if err = conn.QueryRow(ctx, "SELECT full_name FROM people").Scan(&name); err != nil || name != "Changed in PostgreSQL" {
		t.Fatalf("rerun overwrote live data: %q %v", name, err)
	}
	var markers int
	if err = conn.QueryRow(ctx, "SELECT COUNT(*) FROM service_metadata WHERE key='legacy_sqlite_import_complete'").Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("marker=%d %v", markers, err)
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("source SQLite modified")
	}
}
