package adminapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	publishing "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/requestcontext"
)

// Management is mounted behind the same administrator middleware as event reads.
type Management struct {
	Pool         *pgxpool.Pool
	Publications *publishing.Service
}

func (h *Management) Router(events http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/admin/events", h.createEvent)
	mux.HandleFunc("POST /api/v1/admin/publications", h.createPublication)
	mux.HandleFunc("PUT /api/v1/admin/publications/{id}", h.createPublication)
	mux.HandleFunc("GET /api/v1/admin/targets", h.targets)
	mux.HandleFunc("POST /api/v1/admin/media", h.uploadMedia)
	mux.HandleFunc("GET /api/v1/admin/media/{id}", h.getMedia)
	mux.HandleFunc("GET /api/v1/admin/publications", h.listPublications)
	mux.HandleFunc("GET /api/v1/admin/publications/{id}", h.getPublication)
	mux.HandleFunc("POST /api/v1/admin/publications/{id}/cancel", h.cancelPublication)
	mux.HandleFunc("POST /api/v1/admin/publications/{id}/schedule", h.schedulePublication)
	mux.HandleFunc("GET /api/v1/admin/feedback", h.listFeedback)
	mux.HandleFunc("GET /api/v1/admin/feedback/{id}", h.getFeedback)
	mux.Handle("/", events)
	return mux
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "invalid_request")
		return false
	}
	return true
}
func managementError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "not_found")
	} else if errors.Is(err, domain.ErrInvalidState) {
		writeError(w, 409, "invalid_state")
	} else if errors.Is(err, domain.ErrPendingPublicationLimit) {
		writeError(w, 409, "capacity_limit")
	} else {
		writeError(w, 500, "internal_error")
	}
}
func (h *Management) getPublication(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	p, found, err := h.Publications.Publication(r.Context(), id)
	if err != nil {
		managementError(w, err)
		return
	}
	if !found {
		writeError(w, 404, "not_found")
		return
	}
	ids := []string{}
	if paths, ok := p["media_paths"].([]string); ok {
		for _, path := range paths {
			base := filepath.Base(path)
			if mediaIDPattern.MatchString(base) {
				ids = append(ids, base)
			}
		}
	}
	p["media_ids"] = ids
	delete(p, "media_paths")
	writeJSON(w, 200, p)
}
func (h *Management) cancelPublication(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	changed, err := h.Publications.Cancel(r.Context(), id)
	if err != nil {
		managementError(w, err)
		return
	}
	if !changed {
		writeError(w, 409, "invalid_state")
		return
	}
	writeJSON(w, 200, map[string]bool{"cancelled": true})
}
func (h *Management) schedulePublication(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	var body struct {
		ScheduledAt *time.Time `json:"scheduled_at"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	when := time.Now().UTC()
	if body.ScheduledAt != nil {
		when = body.ScheduledAt.UTC()
		if when.Before(time.Now()) {
			writeError(w, 400, "invalid_request")
			return
		}
	}
	changed, err := h.Publications.Schedule(r.Context(), id, when)
	if err != nil {
		managementError(w, err)
		return
	}
	if !changed {
		writeError(w, 409, "invalid_state")
		return
	}
	writeJSON(w, 200, map[string]bool{"scheduled": true})
}

// queryRows serializes database read models without exposing storage paths.
func (h *Management) queryRows(r *http.Request, query string, args ...any) ([]map[string]any, error) {
	rows, err := h.Pool.Query(r.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		item := map[string]any{}
		for i, f := range rows.FieldDescriptions() {
			item[f.Name] = values[i]
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (h *Management) listPublications(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, false)
}
func (h *Management) listFeedback(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, true)
}
func (h *Management) list(w http.ResponseWriter, r *http.Request, feedback bool) {
	values := r.URL.Query()
	from, e1 := optionalTime(values, "from")
	to, e2 := optionalTime(values, "to")
	if e1 != nil || e2 != nil || (from != nil && to != nil && !from.Before(*to)) {
		writeError(w, 400, "invalid_request")
		return
	}
	page, err := pagination.Parse(values)
	if err != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	order, err := one(values, "order")
	if err != nil || !allowed(order, "", "asc", "desc") {
		writeError(w, 400, "invalid_request")
		return
	}
	if order == "" {
		order = "desc"
	}
	sort, err := one(values, "sort")
	if err != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	if sort == "" {
		sort = "created_at"
	}
	if (!feedback && !allowed(sort, "created_at", "scheduled_at")) || (feedback && sort != "created_at") {
		writeError(w, 400, "invalid_request")
		return
	}
	args := []any{}
	clauses := []string{"TRUE"}
	filters := map[string]any{}
	add := func(expr string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(expr, len(args)))
	}
	table, fields := "publications p", "p.id,p.creator_telegram_id,p.text,p.status,p.source,p.scheduled_at,p.created_at"
	if feedback {
		table = "feedback p"
		fields = "p.id,p.person_id,p.event_id,p.text,p.created_at"
	}
	for _, key := range []string{"status", "source", "target", "event_id", "author_id", "kind", "from", "to"} {
		raw, e := one(values, key)
		if e != nil {
			writeError(w, 400, "invalid_request")
			return
		}
		if raw == "" {
			continue
		}
		filters[key] = raw
		switch key {
		case "status":
			if feedback || !allowed(raw, "draft", "scheduled", "sent", "failed", "cancelled") {
				writeError(w, 400, "invalid_request")
				return
			}
			add("p.status=$%d", raw)
		case "source":
			if feedback {
				writeError(w, 400, "invalid_request")
				return
			}
			add("p.source=$%d", raw)
		case "target":
			if feedback {
				writeError(w, 400, "invalid_request")
				return
			}
			add("EXISTS(SELECT 1 FROM publication_deliveries d WHERE d.publication_id=p.id AND d.target_key=$%d)", raw)
		case "event_id", "author_id":
			id, e := strconv.ParseInt(raw, 10, 64)
			if !feedback || e != nil || id <= 0 {
				writeError(w, 400, "invalid_request")
				return
			}
			column := "event_id"
			if key == "author_id" {
				column = "person_id"
			}
			add("p."+column+"=$%d", id)
		case "kind":
			if !feedback || !allowed(raw, "general", "event") {
				writeError(w, 400, "invalid_request")
				return
			}
			if raw == "general" {
				clauses = append(clauses, "p.event_id IS NULL")
			} else {
				clauses = append(clauses, "p.event_id IS NOT NULL")
			}
		case "from", "to":
			at, e := time.Parse(time.RFC3339, raw)
			if e != nil {
				writeError(w, 400, "invalid_request")
				return
			}
			op := ">="
			if key == "to" {
				op = "<"
			}
			column := "created_at"
			if !feedback {
				column = "scheduled_at"
			}
			add("p."+column+op+"$%d", at)
		}
	}
	where := " WHERE " + strings.Join(clauses, " AND ")
	var total int64
	if err = h.Pool.QueryRow(r.Context(), "SELECT COUNT(*) FROM "+table+where, args...).Scan(&total); err != nil {
		managementError(w, err)
		return
	}
	query := fmt.Sprintf("SELECT %s FROM %s%s ORDER BY p.%s %s,p.id %s LIMIT $%d OFFSET $%d", fields, table, where, sort, order, order, len(args)+1, len(args)+2)
	items, err := h.queryRows(r, query, append(args, page.Size, page.Offset())...)
	if err != nil {
		managementError(w, err)
		return
	}
	writePage(w, items, page, total, filters, sort, order)
}
func (h *Management) getFeedback(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	items, err := h.queryRows(r, "SELECT f.id,f.person_id,f.event_id,f.text,f.created_at,p.full_name,ti.username,ti.telegram_user_id,e.title AS event_title FROM feedback f JOIN people p ON p.id=f.person_id JOIN telegram_identities ti ON ti.person_id=p.id LEFT JOIN events e ON e.id=f.event_id WHERE f.id=$1", id)
	if err != nil {
		managementError(w, err)
		return
	}
	if len(items) == 0 {
		writeError(w, 404, "not_found")
		return
	}
	writeJSON(w, 200, items[0])
}

func administratorID(r *http.Request) int64 {
	identity, _ := requestcontext.Administrator(r.Context())
	return identity.TelegramID
}
