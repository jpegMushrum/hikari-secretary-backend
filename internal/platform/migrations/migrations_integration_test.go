//go:build integration

package migrations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func emptySchema(t *testing.T) (context.Context, *pgx.Conn, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })

	schema := fmt.Sprintf("migration_test_%d", time.Now().UnixNano())
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	if _, err = conn.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Clean(filepath.Join(workingDirectory, "..", "..", "..", "migrations"))
	return ctx, conn, directory
}

func TestRunAppliesAllMigrationsToAnEmptySchema(t *testing.T) {
	ctx, conn, directory := emptySchema(t)
	if err := Run(ctx, conn, directory); err != nil {
		t.Fatalf("first migration run: %v", err)
	}
	if err := Run(ctx, conn, directory); err != nil {
		t.Fatalf("idempotent migration run: %v", err)
	}

	var count int
	if err := conn.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 17 {
		t.Fatalf("applied migrations = %d, want 17", count)
	}

	for _, table := range []string{
		"publications", "publication_media", "publication_deliveries",
		"ai_topic_settings", "ai_topic_jobs", "ai_topic_versions",
		"santa_seasons", "santa_participants", "santa_pairs", "santa_wish_drafts",
	} {
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("migration did not create table %q", table)
		}
	}

}

func TestOrderedGiftUpgradePreservesKnownAnswersAndDescriptions(t *testing.T) {
	ctx, conn, directory := emptySchema(t)
	previous := t.TempDir()
	files, err := filepath.Glob(filepath.Join(directory, "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		name := filepath.Base(file)
		if strings.SplitN(name, "_", 2)[0] > "000016" {
			continue
		}
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(previous, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Run(ctx, conn, previous); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO santa_seasons DEFAULT VALUES"); err != nil {
		t.Fatal(err)
	}
	for _, gift := range []string{"ready", "will_be_ready", "not_ready", "unknown"} {
		if _, err := conn.Exec(ctx, `WITH person AS (INSERT INTO people DEFAULT VALUES RETURNING id)
			INSERT INTO santa_participants(season_id, person_id, wishes, gift, gift_description)
			SELECT 1, id, 'Tea', $1, 'Tea set' FROM person`, gift); err != nil {
			t.Fatal(err)
		}
	}
	if err := Run(ctx, conn, directory); err != nil {
		t.Fatal(err)
	}
	var gifts []string
	var descriptionsKept bool
	if err := conn.QueryRow(ctx, `SELECT array_agg(gift ORDER BY person_id), bool_and(gift_description='Tea set')
		FROM santa_participants`).Scan(&gifts, &descriptionsKept); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gifts, []string{"ready", "unknown", "not_ready", "unknown"}) || !descriptionsKept {
		t.Fatalf("unexpected migrated gift data: %v, descriptions retained: %v", gifts, descriptionsKept)
	}
}
