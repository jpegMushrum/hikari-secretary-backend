package memberapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	publishing "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/adapters/postgres"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/database"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/publicationmedia"
)

// Each event has a single publication reference. Imported events need not
// have a stored publication; a participant can still manage a previous registration.
const eventFrom = `
 FROM events e
 JOIN event_publication_references ref ON ref.event_id=e.id
 LEFT JOIN publications p ON p.id::text=ref.external_publication_id
 LEFT JOIN event_registrations er ON er.event_id=e.id AND er.person_id=$1
 LEFT JOIN attendance a ON a.registration_id=er.id `
const visibleEvent = `(ref.published OR er.status='registered') AND (p.id IS NULL OR p.status<>'draft')`
const feedbackEligible = `(er.status IS NOT DISTINCT FROM 'registered' AND e.ends_at<=NOW() AND e.status<>'cancelled')`
const eventStatus = `CASE WHEN e.status='active' AND e.ends_at<=NOW() THEN 'completed' ELSE e.status END`
const personalStatus = `CASE WHEN er.status IS DISTINCT FROM 'registered' THEN 'not_registered'
 WHEN a.attended THEN 'attended' WHEN a.attended=FALSE THEN 'missed'
 WHEN e.ends_at<=NOW() THEN 'unmarked' ELSE 'registered' END`
const eventColumns = "e.id,e.title,e.starts_at,e.ends_at," + eventStatus + "," + personalStatus

type eventItem struct {
	ID             int64     `json:"id"`
	Title          string    `json:"title"`
	StartsAt       time.Time `json:"starts_at"`
	EndsAt         time.Time `json:"ends_at"`
	Status         string    `json:"status"`
	PersonalStatus string    `json:"personal_status"`
}
type eventDetail struct {
	eventItem
	Announcement    *announcementPreview `json:"announcement"`
	AnnouncementURL *string              `json:"announcement_url"`
	CanRegister     bool                 `json:"can_register"`
	CanManage       bool                 `json:"can_manage_registration"`
	CanMark         bool                 `json:"can_mark_attendance"`
	CanFeedback     bool                 `json:"can_leave_feedback"`
	ProfileComplete bool                 `json:"profile_complete"`
	ReminderOptions []int                `json:"reminder_options"`
	Offset          *int                 `json:"reminder_offset_minutes"`
	PreferredOffset *int                 `json:"preferred_reminder_offset_minutes"`
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := pagination.Parse(q)
	if err != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	view := q.Get("view")
	if view == "" {
		view = "upcoming"
	}
	where := " WHERE " + visibleEvent
	switch view {
	case "registered":
		where += " AND er.status='registered' AND e.status='active' AND e.starts_at>NOW()"
	case "upcoming":
		where += " AND e.status='active' AND e.starts_at>NOW()"
	case "all":
	default:
		writeError(w, 400, "invalid_request")
		return
	}
	search := strings.TrimSpace(q.Get("search"))
	if utf8.RuneCountInString(search) > 100 {
		writeError(w, 400, "invalid_request")
		return
	}
	where += " AND (e.title ILIKE $2 OR e.id::text LIKE $2)"
	sortField := q.Get("sort")
	if sortField == "" {
		sortField = "starts_at"
	}
	cols := map[string]string{"id": "e.id", "title": "e.title", "starts_at": "e.starts_at", "ends_at": "e.ends_at", "status": eventStatus, "personal_status": personalStatus}
	order := q.Get("order")
	if order == "" {
		order = "asc"
		if view == "all" {
			order = "desc"
		}
	}
	if cols[sortField] == "" || (order != "asc" && order != "desc") {
		writeError(w, 400, "invalid_request")
		return
	}
	args := []any{personID(r), database.ContainsPattern(search)}
	var total int64
	if err := h.pool.QueryRow(r.Context(), "SELECT count(*)"+eventFrom+where, args...).Scan(&total); err != nil {
		dbError(w, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), "SELECT "+eventColumns+eventFrom+where+" ORDER BY "+cols[sortField]+" "+order+",e.id LIMIT $3 OFFSET $4", append(args, page.Size, page.Offset())...)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []eventItem{}
	for rows.Next() {
		var it eventItem
		if err := rows.Scan(&it.ID, &it.Title, &it.StartsAt, &it.EndsAt, &it.Status, &it.PersonalStatus); err != nil {
			dbError(w, err)
			return
		}
		items = append(items, it)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "page": map[string]any{"number": page.Number, "size": page.Size, "total_items": total, "total_pages": (total + int64(page.Size) - 1) / int64(page.Size)}, "filters": map[string]string{"view": view, "search": search}, "sort": map[string]string{"field": sortField, "order": order}})
}

func (h *Handler) event(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	var e eventDetail
	var startsInFuture bool
	var published bool
	var now time.Time
	err := h.pool.QueryRow(r.Context(), `SELECT `+eventColumns+`,
 CASE WHEN ref.published AND p.id IS NOT NULL THEN jsonb_build_object('id',p.id,'text',p.text,'entities',p.entities,'rich_message',p.rich_message,
 'media', COALESCE((SELECT jsonb_agg(jsonb_build_object('position',ordinal) ORDER BY ordinal) FROM
 (SELECT row_number() OVER(ORDER BY position)-1 ordinal FROM publication_media WHERE publication_id=p.id) img),'[]'::jsonb)) END,
 CASE WHEN ref.published THEN `+publishing.AnnouncementLinkSQL+` END,
 NULLIF(btrim(member.full_name),'') IS NOT NULL AND member.is_russian_citizen IS NOT NULL AND member.is_itmo_student IS NOT NULL,
 CASE WHEN er.status='registered' AND er.reminders_enabled THEN er.reminder_offset_minutes END,
 member.preferred_reminder_offset_minutes,`+feedbackEligible+`,
 e.starts_at>NOW(),ref.published,NOW() `+eventFrom+" JOIN people member ON member.id=$1 WHERE "+visibleEvent+" AND e.id=$2", personID(r), id).Scan(
		&e.ID, &e.Title, &e.StartsAt, &e.EndsAt, &e.Status, &e.PersonalStatus, &e.Announcement, &e.AnnouncementURL, &e.ProfileComplete, &e.Offset, &e.PreferredOffset, &e.CanFeedback, &startsInFuture, &published, &now)
	if err != nil {
		dbError(w, err)
		return
	}
	if e.Announcement != nil {
		e.Announcement.project()
	}
	active := e.Status == "active" && startsInFuture
	e.CanRegister = active && published && e.PersonalStatus == "not_registered"
	e.CanManage = active && e.PersonalStatus == "registered"
	e.CanMark = e.CanFeedback && e.PersonalStatus == "unmarked"
	e.ReminderOptions = []int{}
	for _, n := range h.reminders {
		if e.StartsAt.Sub(now).Minutes() > float64(n) && active {
			e.ReminderOptions = append(e.ReminderOptions, n)
		}
	}
	writeJSON(w, 200, e)
}

func (h *Handler) media(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	position, err := strconv.Atoi(r.PathValue("position"))
	if !ok || err != nil || position < 0 {
		writeError(w, 400, "invalid_request")
		return
	}
	var path string
	err = h.pool.QueryRow(r.Context(), "SELECT m.path"+eventFrom+" JOIN publication_media m ON m.publication_id=p.id WHERE "+visibleEvent+" AND ref.published AND e.id=$2 ORDER BY m.position OFFSET $3 LIMIT 1", personID(r), id, position).Scan(&path)
	if err != nil {
		dbError(w, err)
		return
	}
	publicationmedia.ServeFile(w, r, path)
}
