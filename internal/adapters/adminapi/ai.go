package adminapi

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
)

func (h *Management) aiSettings(w http.ResponseWriter, r *http.Request) {
	s, err := learningcontent.NewPostgresRepository(h.Pool).Settings(r.Context())
	if err != nil {
		managementError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		learningcontent.Settings
		Enabled bool `json:"enabled"`
	}{s, h.AIEnabled})
}

func (h *Management) saveAISettings(w http.ResponseWriter, r *http.Request) {
	var s learningcontent.Settings
	if !decodeBody(w, r, &s) {
		return
	}
	if s.Validate() != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	if err := learningcontent.NewPostgresRepository(h.Pool).SaveSettings(r.Context(), s, administratorID(r)); err != nil {
		managementError(w, err)
		return
	}
	h.aiSettings(w, r)
}

var aiHistorySortFields = map[string]string{
	"id": "id", "scheduled_for": "scheduled_for", "status": "status", "attempts": "attempts",
	"title": `COALESCE(NULLIF(topic->>'title_ru',''), NULLIF(topic->>'title',''),
		NULLIF(topic->>'theme',''), NULLIF(topic->>'topic',''), 'Материал #' || id::text)`,
}

func (h *Management) aiHistory(w http.ResponseWriter, r *http.Request) {
	v, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	page, err := pagination.Parse(v)
	status, e1 := one(v, "status")
	sort, e2 := one(v, "sort")
	order, e3 := one(v, "order")
	if sort == "" {
		sort = "scheduled_for"
	}
	sortExpr, ok := aiHistorySortFields[sort]
	if err != nil || e1 != nil || e2 != nil || e3 != nil || !allowed(status, "", "pending", "retry", "generating", "scheduled", "failed", "cancelled") || !ok || !allowed(order, "", "asc", "desc") {
		writeError(w, 400, "invalid_request")
		return
	}
	if order == "" {
		order = "desc"
	}
	var total int64
	if err := h.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM ai_topic_jobs WHERE ($1='' OR status=$1)`, status).Scan(&total); err != nil {
		managementError(w, err)
		return
	}
	items, err := h.queryRows(r, fmt.Sprintf(`SELECT id,scheduled_for,status,attempts,topic,publication_id,last_error FROM ai_topic_jobs WHERE ($1='' OR status=$1) ORDER BY %s %s,id %s LIMIT $2 OFFSET $3`, sortExpr, order, order), status, page.Size, page.Offset())
	if err != nil {
		managementError(w, err)
		return
	}
	writePage(w, items, page, total, map[string]any{"status": status}, sort, order)
}
