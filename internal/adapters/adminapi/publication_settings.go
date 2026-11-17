package adminapi

import (
	"errors"
	events "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/adapters/postgres"
	eventapp "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
	"net/http"
	"time"
)

// Only configuration is accepted: original Telegram content is never rewritten.
func (h *Management) configurePublication(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	var body struct {
		TargetKeys []string `json:"target_keys"`
		Event      *struct {
			Title    string    `json:"title"`
			StartsAt time.Time `json:"starts_at"`
			EndsAt   time.Time `json:"ends_at"`
		} `json:"event"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.TargetKeys == nil {
		writeError(w, 400, "invalid_request")
		return
	}
	available, err := configuredTargets()
	if err != nil {
		managementError(w, err)
		return
	}
	targets := []domain.Target{}
	seen := map[string]bool{}
	for _, key := range body.TargetKeys {
		found := false
		for _, target := range available {
			if target.Key == key && !seen[key] {
				targets = append(targets, target)
				seen[key] = true
				found = true
				break
			}
		}
		if !found {
			writeError(w, 400, "invalid_target")
			return
		}
	}
	tx, err := h.Pool.Begin(r.Context())
	if err != nil {
		managementError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Same lock order as the event writer, then publication row.
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(20261105)"); err != nil {
		managementError(w, err)
		return
	}
	var status string
	if err = tx.QueryRow(r.Context(), "SELECT status FROM publications WHERE id=$1 FOR UPDATE", id).Scan(&status); err != nil {
		managementError(w, err)
		return
	}
	if status != "draft" {
		writeError(w, 409, "invalid_state")
		return
	}
	if body.Event != nil {
		_, err = events.SaveDraftEvent(r.Context(), tx, events.CreateEvent{PublicationID: id, Title: body.Event.Title, StartsAt: body.Event.StartsAt, EndsAt: body.Event.EndsAt}, administratorID(r))
		switch {
		case errors.Is(err, events.ErrInvalidEvent):
			writeError(w, 400, "invalid_request")
			return
		case errors.Is(err, events.ErrEventCapacity):
			writeError(w, 409, "capacity_limit")
			return
		case errors.Is(err, eventapp.ErrEventNotActive):
			writeError(w, 409, "event_not_active")
			return
		case err != nil:
			managementError(w, err)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM publication_deliveries WHERE publication_id=$1", id); err != nil {
		managementError(w, err)
		return
	}
	for _, target := range targets {
		if _, err = tx.Exec(r.Context(), `INSERT INTO publication_deliveries(publication_id,target_key,target_name,destination,message_thread_id) VALUES($1,$2,$3,$4,$5)`, id, target.Key, target.Name, target.Destination, target.MessageThreadID); err != nil {
			managementError(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		managementError(w, err)
		return
	}
	h.getPublication(w, r)
}
