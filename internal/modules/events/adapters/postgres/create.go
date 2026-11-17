package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/application"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalidEvent = errors.New("invalid_request")
var ErrEventCapacity = errors.New("capacity_limit")
var ErrPublicationState = errors.New("invalid_publication_state")

type CreateEvent struct {
	PublicationID int64     `json:"publication_id"`
	Title         string    `json:"title"`
	StartsAt      time.Time `json:"starts_at"`
	EndsAt        time.Time `json:"ends_at"`
}

// Create serializes capacity checks and linking to a draft publication for both adapters.
func (r *Repository) Create(ctx context.Context, c CreateEvent, actor int64) (application.Event, error) {
	c.Title = strings.TrimSpace(c.Title)
	if c.PublicationID <= 0 || c.Title == "" || utf8.RuneCountInString(c.Title) > 160 || !c.StartsAt.Before(c.EndsAt) || !c.StartsAt.After(time.Now()) {
		return application.Event{}, ErrInvalidEvent
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return application.Event{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(20261105)"); err != nil {
		return application.Event{}, err
	}
	var status string
	if err = tx.QueryRow(ctx, "SELECT status FROM publications WHERE id=$1 FOR UPDATE", c.PublicationID).Scan(&status); err != nil {
		return application.Event{}, err
	}
	if status != "draft" {
		return application.Event{}, ErrPublicationState
	}
	var id int64
	err = tx.QueryRow(ctx, "SELECT event_id FROM event_publication_references WHERE external_publication_id=$1", fmt.Sprint(c.PublicationID)).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return application.Event{}, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		var count int
		if err = tx.QueryRow(ctx, "SELECT COUNT(*) FROM events WHERE status='active' AND ends_at>NOW()").Scan(&count); err != nil {
			return application.Event{}, err
		}
		if count >= 5 {
			return application.Event{}, ErrEventCapacity
		}
		if err = tx.QueryRow(ctx, "INSERT INTO events(title,starts_at,ends_at) VALUES($1,$2,$3) RETURNING id", c.Title, c.StartsAt, c.EndsAt).Scan(&id); err != nil {
			return application.Event{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO event_publication_references(event_id,provider,external_publication_id) VALUES($1,'hikari_publishing',$2)", id, fmt.Sprint(c.PublicationID)); err != nil {
			return application.Event{}, err
		}
	} else {
		tag, e := tx.Exec(ctx, "UPDATE events SET title=$2,starts_at=$3,ends_at=$4,updated_at=NOW() WHERE id=$1 AND status='active' AND ends_at>NOW()", id, c.Title, c.StartsAt, c.EndsAt)
		if e != nil {
			return application.Event{}, e
		}
		if tag.RowsAffected() != 1 {
			return application.Event{}, application.ErrEventNotActive
		}
	}
	actorType := "telegram_admin"
	if actor == 0 {
		actorType = "bot_service"
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_log(actor_type,actor_id,action,object_type,object_id) VALUES($3,$1,'event.saved','event',$2)", fmt.Sprint(actor), id, actorType); err != nil {
		return application.Event{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return application.Event{}, err
	}
	return r.Get(ctx, id)
}
