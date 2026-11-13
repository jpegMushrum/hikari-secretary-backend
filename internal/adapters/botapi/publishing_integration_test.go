//go:build integration

package botapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/adapters/botapi"
	interactionspostgres "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/interactions/adapters/postgres"
	interactions "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/interactions/application"
	publishingpostgres "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/adapters/postgres"
	publishing "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return pool
}

func TestPublishingDeliveryMakesEventAvailableToUsers(t *testing.T) {
	ctx := context.Background()
	pool := integrationPool(t)
	defer pool.Close()
	repository := publishingpostgres.NewRepository(pool)
	service := publishing.NewService(repository)
	key := "integration-" + time.Now().UTC().Format("20060102150405.000000000")
	publicationID, err := service.CreateDraft(ctx, domain.Draft{CreatorTelegramID: 991001, Text: "integration", SourceKey: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM publications WHERE id=$1 AND source_key=$2`, publicationID, key)
	})
	if err := service.ToggleTarget(ctx, publicationID, domain.Target{Key: "integration", Name: "Integration", Destination: "-1000001"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := service.Schedule(ctx, publicationID, time.Now().UTC().Add(-time.Second)); err != nil || !ok {
		t.Fatalf("schedule: ok=%v err=%v", ok, err)
	}
	var eventID int64
	if err := pool.QueryRow(ctx, `INSERT INTO events(title,starts_at,ends_at) VALUES('Integration event',NOW()+INTERVAL '1 hour',NOW()+INTERVAL '2 hours') RETURNING id`).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM event_publication_references WHERE event_id=$1`, eventID)
		_, _ = pool.Exec(ctx, `DELETE FROM events WHERE id=$1`, eventID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO event_publication_references(event_id,provider,external_publication_id,published) VALUES($1,'hikari_publishing',$2,FALSE)`, eventID, stringID(publicationID)); err != nil {
		t.Fatal(err)
	}

	delivery, err := service.ClaimDue(ctx)
	if err != nil || delivery == nil {
		t.Fatalf("claim: delivery=%#v err=%v", delivery, err)
	}
	if delivery.PublicationID != publicationID || !delivery.EventEnabled {
		t.Fatalf("wrong delivery: %#v", delivery)
	}
	if err := service.Succeeded(ctx, delivery.ID, "external-message"); err != nil {
		t.Fatal(err)
	}

	handler := botapi.NewRouter(pool, interactions.NewService(interactionspostgres.NewRepository(pool)), service)
	mark := httptest.NewRequest(http.MethodPost, "/internal/bot/v1/events/"+stringID(publicationID)+"/published", nil)
	markRecorder := httptest.NewRecorder()
	handler.ServeHTTP(markRecorder, mark)
	if markRecorder.Code != http.StatusNoContent {
		t.Fatalf("mark published: %d %s", markRecorder.Code, markRecorder.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/bot/v1/events?view=available&telegram_id=991001", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("available events: %d %s", recorder.Code, recorder.Body.String())
	}
	var events []map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&events); err != nil {
		t.Fatalf("decode available events: %v; body=%q", err, recorder.Body.String())
	}
	found := false
	for _, event := range events {
		if postID, ok := event["post_id"].(float64); ok && int64(postID) == publicationID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected publication %d in available events: %#v", publicationID, events)
	}
}

func stringID(id int64) string { return strconv.FormatInt(id, 10) }
