package botapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	publishing "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/adapters/postgres"
)

func (h *Handler) upcomingEvents(w http.ResponseWriter, r *http.Request) {
	telegramID, err := strconv.ParseInt(r.URL.Query().Get("telegram_id"), 10, 64)
	if err != nil || telegramID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT ref.external_publication_id::bigint,e.title,e.starts_at,e.ends_at,
			er.id IS NOT NULL,COALESCE(er.reminders_enabled,FALSE),er.reminder_offset_minutes,
			`+publishing.AnnouncementLinkSQL+`
		FROM events e
		JOIN event_publication_references ref ON ref.event_id=e.id
		LEFT JOIN event_registrations er ON er.event_id=e.id AND er.status='registered'
			AND er.person_id=(SELECT person_id FROM telegram_identities WHERE telegram_user_id=$1)
		WHERE e.status='active' AND e.starts_at>NOW() AND (ref.published OR er.id IS NOT NULL)
		ORDER BY e.starts_at,ref.external_publication_id::bigint`, telegramID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var postID int64
		var title string
		var start, end time.Time
		var registered, reminders bool
		var offset *int
		var announcementURL *string
		if err := rows.Scan(&postID, &title, &start, &end, &registered, &reminders, &offset, &announcementURL); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		items = append(items, map[string]any{
			"post_id": postID, "title": title, "text": title,
			"starts_at": start.UTC(), "ends_at": end.UTC(), "registered": registered,
			"reminders_enabled": reminders, "reminder_offset_minutes": offset,
			"announcement_url": announcementURL,
		})
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) changeReminder(w http.ResponseWriter, r *http.Request) {
	publicationID, validPost := pathInt64(r, "publication_id")
	telegramID, validUser := pathInt64(r, "telegram_id")
	if !validPost || !validUser {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var body struct {
		Offset json.RawMessage `json:"reminder_offset_minutes"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// Explicit null disables reminders. An omitted field must not do so.
	var offset *int32
	if len(body.Offset) == 0 || json.Unmarshal(body.Offset, &offset) != nil || (offset != nil && *offset <= 0) {
		writeError(w, http.StatusBadRequest, "invalid_reminder")
		return
	}
	var valid bool
	err := h.pool.QueryRow(r.Context(), `
		WITH current AS (
			SELECT er.id, ($3::int IS NULL OR e.starts_at > NOW() + $3 * interval '1 minute') AS eligible
			FROM event_registrations er
			JOIN telegram_identities ti ON ti.person_id=er.person_id
			JOIN events e ON e.id=er.event_id
			JOIN event_publication_references ref ON ref.event_id=e.id
			WHERE ti.telegram_user_id=$1 AND ref.external_publication_id=$2
				AND er.status='registered' AND e.status='active' AND e.starts_at>NOW()
			FOR UPDATE OF er FOR SHARE OF e
		), changed AS (
			UPDATE event_registrations er SET reminders_enabled=($3::int IS NOT NULL),
				reminder_offset_minutes=$3,reminder_sent_at=NULL,updated_at=NOW()
			FROM current WHERE er.id=current.id AND current.eligible
				AND (er.reminder_offset_minutes IS DISTINCT FROM $3 OR er.reminders_enabled<>($3 IS NOT NULL))
		)
		SELECT eligible FROM current`, telegramID, strconv.FormatInt(publicationID, 10), offset).Scan(&valid)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusConflict, "registration_unavailable")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal_error")
	case !valid:
		writeError(w, http.StatusBadRequest, "invalid_reminder")
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"updated": true})
	}
}
