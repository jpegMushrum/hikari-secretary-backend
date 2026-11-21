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

func (h *Management) aiHistorySelect() string {
	return fmt.Sprintf(`SELECT j.id, j.scheduled_for, j.status, j.attempts, j.topic,
	j.publication_id, p.status AS publication_status, j.last_error,
	(SELECT COUNT(*) FROM ai_topic_versions WHERE job_id=j.id) AS version_count,
 (%t AND EXISTS (SELECT 1 FROM regenerable_ai_jobs eligible WHERE eligible.id=j.id)) AS can_regenerate
	FROM ai_topic_jobs j LEFT JOIN publications p ON p.id = j.publication_id`, h.AIEnabled)
}

var aiHistorySortFields = map[string]string{
	"id": "j.id", "scheduled_for": "j.scheduled_for", "status": "j.status", "attempts": "j.attempts",
	"publication_status": "p.status",
	"title": `COALESCE(NULLIF(topic->>'title',''), NULLIF(topic->>'title_ru',''),
		NULLIF(topic->>'theme',''), NULLIF(topic->>'topic',''), 'Материал #' || j.id::text)`,
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
	items, err := h.queryRows(r, h.aiHistorySelect()+fmt.Sprintf(` WHERE ($1='' OR j.status=$1)
		ORDER BY %s %s NULLS LAST, j.id %s LIMIT $2 OFFSET $3`, sortExpr, order, order), status, page.Size, page.Offset())
	if err != nil {
		managementError(w, err)
		return
	}
	writePage(w, items, page, total, map[string]any{"status": status}, sort, order)
}

func (h *Management) aiHistoryItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	items, err := h.queryRows(r, h.aiHistorySelect()+" WHERE j.id=$1", id)
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

func (h *Management) aiHistoryVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	values, queryErr := url.ParseQuery(r.URL.RawQuery)
	page, err := pagination.Parse(values)
	if !ok || queryErr != nil || err != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	var total int64
	err = h.Pool.QueryRow(r.Context(), `SELECT (SELECT COUNT(*) FROM ai_topic_versions WHERE job_id=j.id)
		FROM ai_topic_jobs j WHERE id=$1`, id).Scan(&total)
	if err != nil {
		managementError(w, err)
		return
	}
	items, err := h.queryRows(r, `SELECT version,topic FROM ai_topic_versions
		WHERE job_id=$1 AND version<=$2 ORDER BY version DESC LIMIT $3 OFFSET $4`, id, total, page.Size, page.Offset())
	if err != nil {
		managementError(w, err)
		return
	}
	writePage(w, items, page, total, map[string]any{}, "version", "desc")
}

func (h *Management) latestAIGeneration(w http.ResponseWriter, r *http.Request) {
	items, err := h.queryRows(r, h.aiHistorySelect()+" WHERE j.id=("+learningcontent.LatestGenerationID+")")
	if err != nil {
		managementError(w, err)
		return
	}
	if len(items) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, 200, items[0])
}

func (h *Management) regenerateAIJob(w http.ResponseWriter, r *http.Request) {
	var jobID *int64
	if r.PathValue("id") != "" {
		id, ok := pathID(r)
		if !ok {
			writeError(w, 400, "invalid_request")
			return
		}
		jobID = &id
	}
	if !h.AIEnabled {
		writeError(w, 503, "ai_disabled")
		return
	}
	id, err := learningcontent.NewPostgresRepository(h.Pool).RequestRegeneration(r.Context(), jobID, administratorID(r))
	if err != nil {
		managementError(w, err)
		return
	}
	response := map[string]any{"accepted": true}
	if jobID == nil {
		response["id"] = id
	}
	writeJSON(w, 202, response)
}
