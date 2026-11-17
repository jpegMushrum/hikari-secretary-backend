package adminapi

import (
	"errors"
	eventspostgres "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/adapters/postgres"
	eventsapp "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/application"
	"net/http"
)

func (h *Management) createEvent(w http.ResponseWriter, r *http.Request) {
	var body eventspostgres.CreateEvent
	if !decodeBody(w, r, &body) {
		return
	}
	event, err := eventspostgres.NewRepository(h.Pool).Create(r.Context(), body, administratorID(r))
	if errors.Is(err, eventspostgres.ErrInvalidEvent) {
		writeError(w, 400, "invalid_request")
		return
	}
	if errors.Is(err, eventspostgres.ErrEventCapacity) {
		writeError(w, 409, "capacity_limit")
		return
	}
	if errors.Is(err, eventspostgres.ErrPublicationState) {
		writeError(w, 409, "invalid_publication_state")
		return
	}
	if errors.Is(err, eventsapp.ErrEventNotActive) {
		writeError(w, 409, "event_not_active")
		return
	}
	if err != nil {
		managementError(w, err)
		return
	}
	writeJSON(w, 201, event)
}
