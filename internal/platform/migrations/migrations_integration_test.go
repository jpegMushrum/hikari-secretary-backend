//go:build integration

package migrations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRunAppliesAllMigrationsToAnEmptySchema(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

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
	if err = Run(ctx, conn, directory); err != nil {
		t.Fatalf("first migration run: %v", err)
	}
	if err = Run(ctx, conn, directory); err != nil {
		t.Fatalf("idempotent migration run: %v", err)
	}

	var count int
	if err = conn.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 13 {
		t.Fatalf("applied migrations = %d, want 13", count)
	}

	for _, table := range []string{
		"publications", "publication_media", "publication_deliveries",
		"ai_topic_settings", "ai_topic_jobs", "ai_topic_versions",
	} {
		var exists bool
		if err = conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("migration did not create table %q", table)
		}
	}

}
