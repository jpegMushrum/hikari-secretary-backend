//go:build integration

package memberapi

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/migrations"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/requestcontext"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func testRequest(handler http.Handler, personID, telegramID int64, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := requestcontext.WithMember(r.Context(), requestcontext.MemberIdentity{Telegram: telegramauth.Identity{TelegramID: telegramID, FirstName: fmt.Sprintf("Member %d", personID)}})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r.WithContext(ctx))
	return w
}
func testPool(t *testing.T) *pgxpool.Pool {
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
	schema := fmt.Sprintf("member_test_%d", time.Now().UnixNano())
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
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
	t.Cleanup(pool.Close)
	return pool
}
