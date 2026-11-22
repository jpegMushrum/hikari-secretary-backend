package memberapi

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
)

type memberProfile struct {
	FullName         *string `json:"full_name"`
	IsRussianCitizen *bool   `json:"is_russian_citizen"`
	IsITMOStudent    *bool   `json:"is_itmo_student"`
	FeedbackEnabled  *bool   `json:"feedback_enabled"`
	ReminderOffset   *int    `json:"reminder_offset_minutes"`
}

func (h *Handler) profile(w http.ResponseWriter, r *http.Request) {
	var profile memberProfile
	err := h.pool.QueryRow(r.Context(), `SELECT full_name,is_russian_citizen,is_itmo_student,feedback_offers_enabled,preferred_reminder_offset_minutes
		FROM people WHERE id=$1`, personID(r)).Scan(&profile.FullName, &profile.IsRussianCitizen, &profile.IsITMOStudent, &profile.FeedbackEnabled, &profile.ReminderOffset)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, profile)
}

func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) {
	var profile struct {
		registrationProfile
		FeedbackEnabled *bool `json:"feedback_enabled"`
	}
	if !decodeBody(w, r, &profile) {
		return
	}
	if !profile.valid() {
		writeError(w, 400, "invalid_request")
		return
	}
	_, err := h.pool.Exec(r.Context(), `UPDATE people SET full_name=$2,is_russian_citizen=$3,
		is_itmo_student=$4,feedback_offers_enabled=COALESCE($5,feedback_offers_enabled),updated_at=NOW() WHERE id=$1`,
		personID(r), profile.FullName, *profile.Citizen, *profile.Student, profile.FeedbackEnabled)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	w.WriteHeader(204)
}

func (h *Handler) updatePreferences(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool           `json:"feedback_enabled"`
		Offset  json.RawMessage `json:"reminder_offset_minutes"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Enabled == nil && len(body.Offset) == 0 {
		writeError(w, 400, "invalid_request")
		return
	}
	var offset *int
	hasOffset := len(body.Offset) > 0
	if hasOffset && (json.Unmarshal(body.Offset, &offset) != nil || (offset != nil && !slices.Contains(h.reminders, *offset))) {
		writeError(w, 400, "invalid_reminder")
		return
	}
	_, err := h.pool.Exec(r.Context(), `UPDATE people SET feedback_offers_enabled=COALESCE($2,feedback_offers_enabled),
		preferred_reminder_offset_minutes=CASE WHEN $3 THEN $4::int ELSE preferred_reminder_offset_minutes END,
		updated_at=NOW() WHERE id=$1`, personID(r), body.Enabled, hasOffset, offset)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	w.WriteHeader(204)
}

type ownFeedback struct {
	ID         int64     `json:"id"`
	Text       string    `json:"text"`
	EventID    *int64    `json:"event_id"`
	EventTitle *string   `json:"event_title"`
	CreatedAt  time.Time `json:"created_at"`
}

func (h *Handler) feedback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	page, err := pagination.Parse(query)
	field, order := query.Get("sort"), query.Get("order")
	if order == "" {
		order = "desc"
	}
	if err != nil || (field != "" && field != "created_at") || (order != "asc" && order != "desc") {
		writeError(w, 400, "invalid_request")
		return
	}
	id := personID(r)
	var total int64
	if err := h.pool.QueryRow(r.Context(), "SELECT COUNT(*) FROM feedback WHERE person_id=$1", id).Scan(&total); err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	rows, err := h.pool.Query(r.Context(), `SELECT f.id,f.text,f.event_id,e.title,f.created_at FROM feedback f
		LEFT JOIN events e ON e.id=f.event_id WHERE f.person_id=$1 ORDER BY f.created_at `+order+",f.id "+order+" LIMIT $2 OFFSET $3", id, page.Size, page.Offset())
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ownFeedback])
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "page": map[string]any{"number": page.Number, "size": page.Size, "total_items": total, "total_pages": (total + int64(page.Size) - 1) / int64(page.Size)}, "sort": map[string]string{"field": "created_at", "order": order}, "filters": map[string]any{}})
}

func (h *Handler) createFeedback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text    string `json:"text"`
		EventID *int64 `json:"event_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" || utf8.RuneCountInString(body.Text) > 3000 || (body.EventID != nil && *body.EventID <= 0) {
		writeError(w, 400, "invalid_request")
		return
	}
	tag, err := h.pool.Exec(r.Context(), `INSERT INTO feedback(person_id,event_id,text)
		SELECT $1,$2::bigint,$3 WHERE $2::bigint IS NULL OR EXISTS (
			SELECT 1 `+eventFrom+` WHERE `+visibleEvent+` AND e.id=$2 AND `+feedbackEligible+`)`, personID(r), body.EventID, body.Text)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "not_found")
		return
	}
	w.WriteHeader(204)
}

func (h *Handler) deleteFeedback(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	tag, err := h.pool.Exec(r.Context(), "DELETE FROM feedback WHERE id=$1 AND person_id=$2", id, personID(r))
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "not_found")
		return
	}
	w.WriteHeader(204)
}
