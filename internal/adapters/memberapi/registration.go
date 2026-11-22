package memberapi

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	eventpg "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/adapters/postgres"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type registrationProfile struct {
	FullName string `json:"full_name"`
	Citizen  *bool  `json:"is_russian_citizen"`
	Student  *bool  `json:"is_itmo_student"`
}

func (p *registrationProfile) valid() bool {
	p.FullName = strings.TrimSpace(p.FullName)
	n := utf8.RuneCountInString(p.FullName)
	return n >= 2 && n <= 160 && p.Citizen != nil && p.Student != nil
}
func (h *Handler) reminder(raw json.RawMessage, starts, now time.Time) (*int, bool) {
	var n *int
	if len(raw) == 0 || json.Unmarshal(raw, &n) != nil {
		return nil, false
	}
	return n, n == nil || slices.Contains(h.reminders, *n) && starts.Sub(now).Minutes() > float64(*n)
}

type registrationEvent struct {
	start, timeNow  time.Time
	published, open bool
}

// Serialize concurrent event closure, first registration and changes from this
// interface. Returning through the transaction rolls back profile and reminder together.
func (h *Handler) lockOpen(w http.ResponseWriter, r *http.Request) (pgx.Tx, int64, registrationEvent, bool) {
	var e registrationEvent
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return nil, 0, e, false
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		dbError(w, err)
		return nil, 0, e, false
	}
	err = tx.QueryRow(r.Context(), `SELECT e.starts_at,NOW(),ref.published,e.status='active' AND e.starts_at>NOW()`+eventFrom+` WHERE `+visibleEvent+` AND e.id=$2 FOR SHARE OF e`, personID(r), id).Scan(&e.start, &e.timeNow, &e.published, &e.open)
	if err != nil || !e.open {
		tx.Rollback(r.Context())
		if err != nil {
			dbError(w, err)
		} else {
			writeError(w, 409, "registration_unavailable")
		}
		return nil, 0, e, false
	}
	return tx, id, e, true
}
func commitMutation(w http.ResponseWriter, r *http.Request, tx pgx.Tx) {
	if err := tx.Commit(r.Context()); err != nil {
		dbError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Offset  json.RawMessage      `json:"reminder_offset_minutes"`
		Profile *registrationProfile `json:"profile"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	tx, id, e, ok := h.lockOpen(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())

	if !e.published {
		writeError(w, 409, "registration_unavailable")
		return
	}
	pid := personID(r)
	var already, complete bool
	err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM event_registrations WHERE event_id=$2 AND person_id=$1 AND status='registered'),
 NULLIF(btrim(full_name),'') IS NOT NULL AND is_russian_citizen IS NOT NULL AND is_itmo_student IS NOT NULL
 FROM people WHERE id=$1 FOR NO KEY UPDATE`, pid, id).Scan(&already, &complete)
	if err != nil {
		dbError(w, err)
		return
	}
	if already {
		w.WriteHeader(204)
		return
	}
	offset, valid := h.reminder(body.Offset, e.start, e.timeNow)
	if !valid {
		writeError(w, 400, "invalid_reminder")
		return
	}
	if !complete {
		if body.Profile == nil {
			writeError(w, 409, "profile_incomplete")
			return
		}
		if !body.Profile.valid() {
			writeError(w, 400, "invalid_profile")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE people SET full_name=$2,is_russian_citizen=$3,is_itmo_student=$4,updated_at=NOW() WHERE id=$1`, pid, body.Profile.FullName, body.Profile.Citizen, body.Profile.Student)
		if err != nil {
			dbError(w, err)
			return
		}
	}
	err = eventpg.RegisterTx(r.Context(), tx, id, pid, offset)
	if err != nil {
		dbError(w, err)
		return
	}
	commitMutation(w, r, tx)
}
func (h *Handler) updateReminder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Offset json.RawMessage `json:"reminder_offset_minutes"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	tx, id, e, ok := h.lockOpen(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	n, valid := h.reminder(body.Offset, e.start, e.timeNow)
	if !valid {
		writeError(w, 400, "invalid_reminder")
		return
	}
	// Match the person-before-registration lock order used by registration.
	// A stale request rolls back the preference along with the event change.
	if _, err := tx.Exec(r.Context(), `UPDATE people SET preferred_reminder_offset_minutes=$2,updated_at=NOW() WHERE id=$1`, personID(r), n); err != nil {
		dbError(w, err)
		return
	}
	tag, err := tx.Exec(r.Context(), `UPDATE event_registrations SET reminders_enabled=$3,
 reminder_sent_at=CASE WHEN reminder_offset_minutes IS DISTINCT FROM $4 OR reminders_enabled<>$3 THEN NULL ELSE reminder_sent_at END,
 reminder_offset_minutes=$4,updated_at=NOW() WHERE event_id=$1 AND person_id=$2 AND status='registered'`, id, personID(r), n != nil, n)
	if err != nil {
		dbError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 409, "registration_unavailable")
		return
	}
	commitMutation(w, r, tx)
}
func (h *Handler) cancelRegistration(w http.ResponseWriter, r *http.Request) {
	tx, id, _, ok := h.lockOpen(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), "UPDATE event_registrations SET status='cancelled',cancelled_at=NOW(),updated_at=NOW() WHERE event_id=$1 AND person_id=$2 AND status='registered'", id, personID(r))
	if err != nil {
		dbError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 409, "registration_unavailable")
		return
	}
	commitMutation(w, r, tx)
}
func (h *Handler) attendance(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	var body struct {
		Attended *bool `json:"attended"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Attended == nil {
		writeError(w, 400, "invalid_request")
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		dbError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var regID int64
	err = tx.QueryRow(r.Context(), `SELECT er.id FROM events e JOIN event_registrations er ON er.event_id=e.id
 WHERE e.id=$1 AND er.person_id=$2 AND er.status='registered' AND e.status<>'cancelled' AND e.ends_at<=NOW()
 FOR SHARE OF e FOR UPDATE OF er`, id, personID(r)).Scan(&regID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, "attendance_unavailable")
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	tag, err := tx.Exec(r.Context(), "INSERT INTO attendance(registration_id,attended,recorded_at) VALUES($1,$2,NOW()) ON CONFLICT(registration_id) DO NOTHING", regID, body.Attended)
	if err != nil {
		dbError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 409, "attendance_unavailable")
		return
	}
	commitMutation(w, r, tx)
}
