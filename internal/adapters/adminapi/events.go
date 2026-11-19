package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	events "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/requestcontext"
)

type EventService interface {
	List(context.Context, events.ListEventsQuery) (events.ListEventsResult, error)
	Get(context.Context, int64) (events.Event, error)
	ListParticipants(context.Context, int64, events.ListParticipantsQuery) (events.ListParticipantsResult, error)
	Cancel(context.Context, int64, int64) (events.CancelResult, error)
}

type Handler struct{ events EventService }

func NewRouter(eventsService EventService) http.Handler {
	h := &Handler{eventsService}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", h.listEvents)
	mux.HandleFunc("GET /events/{id}", h.getEvent)
	mux.HandleFunc("GET /events/{id}/participants", h.listParticipants)
	mux.HandleFunc("POST /events/{id}/cancel", h.cancelEvent)
	return http.StripPrefix("/api/v1/admin", mux)
}
func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	query, err := parseEventsQuery(r.URL.Query())
	if err != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	result, err := h.events.List(r.Context(), query)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writePage(w, result.Items, query.Page, result.TotalItems, map[string]any{"status": query.Status, "search": query.Search, "starts_from": query.StartsFrom, "starts_to": query.StartsTo}, string(query.Sort), string(query.Order))
}
func (h *Handler) getEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	event, err := h.events.Get(r.Context(), id)
	if errors.Is(err, events.ErrEventNotFound) {
		writeError(w, 404, "event_not_found")
		return
	}
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, event)
}
func (h *Handler) listParticipants(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	query, err := parseParticipantsQuery(r.URL.Query())
	if err != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	if _, err = h.events.Get(r.Context(), id); errors.Is(err, events.ErrEventNotFound) {
		writeError(w, 404, "event_not_found")
		return
	} else if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	result, err := h.events.ListParticipants(r.Context(), id, query)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writePage(w, result.Items, query.Page, result.TotalItems, map[string]any{"status": query.Status, "is_russian_citizen": query.IsRussianCitizen, "is_itmo_student": query.IsITMOStudent, "needs_pass": query.NeedsPass}, string(query.Sort), string(query.Order))
}
func (h *Handler) cancelEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	identity, ok := requestcontext.Administrator(r.Context())
	if !ok {
		writeError(w, 500, "internal_error")
		return
	}
	result, err := h.events.Cancel(r.Context(), id, identity.TelegramID)
	if errors.Is(err, events.ErrEventNotFound) {
		writeError(w, 404, "event_not_found")
		return
	}
	if errors.Is(err, events.ErrEventNotActive) {
		writeError(w, 409, "event_not_active")
		return
	}
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, result)
}
func parseEventsQuery(values url.Values) (events.ListEventsQuery, error) {
	page, err := pagination.Parse(values)
	if err != nil {
		return events.ListEventsQuery{}, err
	}
	status, err := one(values, "status")
	if err != nil || !allowed(status, "", "active", "cancelled", "completed") {
		return events.ListEventsQuery{}, errors.New("status")
	}
	search, err := one(values, "search")
	if err != nil || utf8.RuneCountInString(search) > 100 {
		return events.ListEventsQuery{}, errors.New("search")
	}
	sortRaw, err := one(values, "sort")
	if err != nil {
		return events.ListEventsQuery{}, err
	}
	if sortRaw == "" {
		sortRaw = "starts_at"
	}
	if !allowed(sortRaw, "starts_at", "title", "created_at", "id", "status", "registrations", "attended") {
		return events.ListEventsQuery{}, errors.New("sort")
	}
	orderRaw, err := one(values, "order")
	if err != nil {
		return events.ListEventsQuery{}, err
	}
	if orderRaw == "" {
		orderRaw = "asc"
	}
	if !allowed(orderRaw, "asc", "desc") {
		return events.ListEventsQuery{}, errors.New("order")
	}
	from, err := optionalTime(values, "starts_from")
	if err != nil {
		return events.ListEventsQuery{}, err
	}
	to, err := optionalTime(values, "starts_to")
	if err != nil || (from != nil && to != nil && !from.Before(*to)) {
		return events.ListEventsQuery{}, errors.New("range")
	}
	return events.ListEventsQuery{Page: page, Status: status, Search: strings.TrimSpace(search), StartsFrom: from, StartsTo: to, Sort: events.SortField(sortRaw), Order: events.SortOrder(orderRaw)}, nil
}
func parseParticipantsQuery(values url.Values) (events.ListParticipantsQuery, error) {
	page, err := pagination.Parse(values)
	if err != nil {
		return events.ListParticipantsQuery{}, err
	}
	status, err := one(values, "status")
	if err != nil || !allowed(status, "", "registered", "cancelled") {
		return events.ListParticipantsQuery{}, errors.New("status")
	}
	sortRaw, err := one(values, "sort")
	if err != nil {
		return events.ListParticipantsQuery{}, err
	}
	if sortRaw == "" {
		sortRaw = "name"
	}
	if !allowed(sortRaw, "name", "registered_at", "needs_pass", "person_id", "telegram", "cancelled_at", "status", "attended", "is_russian_citizen", "is_itmo_student") {
		return events.ListParticipantsQuery{}, errors.New("sort")
	}
	orderRaw, err := one(values, "order")
	if err != nil {
		return events.ListParticipantsQuery{}, err
	}
	if orderRaw == "" {
		orderRaw = "asc"
	}
	if !allowed(orderRaw, "asc", "desc") {
		return events.ListParticipantsQuery{}, errors.New("order")
	}
	citizen, err := optionalBool(values, "is_russian_citizen")
	if err != nil {
		return events.ListParticipantsQuery{}, err
	}
	student, err := optionalBool(values, "is_itmo_student")
	if err != nil {
		return events.ListParticipantsQuery{}, err
	}
	pass, err := optionalBool(values, "needs_pass")
	if err != nil {
		return events.ListParticipantsQuery{}, err
	}
	return events.ListParticipantsQuery{Page: page, Status: status, IsRussianCitizen: citizen, IsITMOStudent: student, NeedsPass: pass, Sort: events.ParticipantSort(sortRaw), Order: events.SortOrder(orderRaw)}, nil
}
func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}
func optionalTime(values url.Values, name string) (*time.Time, error) {
	raw, err := one(values, name)
	if err != nil || raw == "" {
		return nil, err
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	value = value.UTC()
	return &value, nil
}
func optionalBool(values url.Values, name string) (*bool, error) {
	raw, err := one(values, name)
	if err != nil || raw == "" {
		return nil, err
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}
func one(values url.Values, name string) (string, error) {
	items, ok := values[name]
	if !ok {
		return "", nil
	}
	if len(items) != 1 {
		return "", errors.New("repeated")
	}
	return items[0], nil
}
func allowed(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
func writePage(w http.ResponseWriter, items any, page pagination.Page, total int64, filters map[string]any, sort, order string) {
	pages := (total + int64(page.Size) - 1) / int64(page.Size)
	writeJSON(w, 200, map[string]any{"items": items, "page": map[string]any{"number": page.Number, "size": page.Size, "total_items": total, "total_pages": pages}, "sort": map[string]string{"field": sort, "order": order}, "filters": filters})
}
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
