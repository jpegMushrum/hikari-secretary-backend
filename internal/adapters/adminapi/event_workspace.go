package adminapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The workspace is a read model: drafts are publications, not synthetic events.
// Filtering and pagination apply to the entire union, never to separate client lists.
const eventWorkspaceSQL = `WITH workspace AS (
 SELECT 'event'::text kind,e.id,r.external_publication_id::bigint publication_id,e.title,e.starts_at,e.ends_at,
 CASE WHEN e.status='active' AND e.ends_at<=NOW() THEN 'completed' ELSE e.status END status,
 e.created_at,
 (SELECT COUNT(*) FROM event_registrations er WHERE er.event_id=e.id AND er.status='registered') registrations,
 (SELECT COUNT(*) FROM event_registrations er JOIN attendance a ON a.registration_id=er.id WHERE er.event_id=e.id AND a.attended) attended
 FROM events e LEFT JOIN event_publication_references r ON r.event_id=e.id
 WHERE NOT EXISTS(SELECT 1 FROM publications p WHERE p.id::text=r.external_publication_id AND r.provider='hikari_publishing' AND p.status='draft')
 UNION ALL
 SELECT 'draft',p.id,p.id,COALESCE(e.title,NULLIF(split_part(p.text,E'\n',1),''),'Черновик #'||p.id),e.starts_at,e.ends_at,'draft',p.created_at,0,0
 FROM publications p LEFT JOIN event_publication_references r ON r.provider='hikari_publishing' AND r.external_publication_id=p.id::text
 LEFT JOIN events e ON e.id=r.event_id WHERE p.status='draft'
 ) `

type workspaceItem struct {
	Kind          string     `json:"kind"`
	ID            int64      `json:"id"`
	PublicationID *int64     `json:"publication_id"`
	Title         string     `json:"title"`
	StartsAt      *time.Time `json:"starts_at"`
	EndsAt        *time.Time `json:"ends_at"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	Registrations int64      `json:"registrations"`
	Attended      int64      `json:"attended"`
}

func (h *Management) eventWorkspace(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	status, err := one(values, "status")
	if err != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	if status == "draft" {
		values.Del("status")
	}
	q, err := parseEventsQuery(values)
	if err != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	clauses := []string{"TRUE"}
	args := []any{}
	add := func(expr string, v any) {
		args = append(args, v)
		clauses = append(clauses, fmt.Sprintf(expr, len(args)))
	}
	if status != "" {
		add("status=$%d", status)
	}
	if q.Search != "" {
		add("title ILIKE $%d", "%"+q.Search+"%")
	}
	if q.StartsFrom != nil {
		add("starts_at >= $%d", *q.StartsFrom)
	}
	if q.StartsTo != nil {
		add("starts_at < $%d", *q.StartsTo)
	}
	where := " WHERE " + strings.Join(clauses, " AND ")
	var total int64
	if err = h.Pool.QueryRow(r.Context(), eventWorkspaceSQL+"SELECT COUNT(*) FROM workspace"+where, args...).Scan(&total); err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	sql := eventWorkspaceSQL + fmt.Sprintf("SELECT kind,id,publication_id,title,starts_at,ends_at,status,created_at,registrations,attended FROM workspace%s ORDER BY %s %s NULLS LAST,kind,id LIMIT $%d OFFSET $%d", where, q.Sort, q.Order, len(args)+1, len(args)+2)
	rows, err := h.Pool.Query(r.Context(), sql, append(args, q.Page.Size, q.Page.Offset())...)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	defer rows.Close()
	items := make([]workspaceItem, 0)
	for rows.Next() {
		var item workspaceItem
		if err = rows.Scan(&item.Kind, &item.ID, &item.PublicationID, &item.Title, &item.StartsAt, &item.EndsAt, &item.Status, &item.CreatedAt, &item.Registrations, &item.Attended); err != nil {
			writeError(w, 500, "internal_error")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writePage(w, items, q.Page, total, map[string]any{"status": status, "search": q.Search, "starts_from": q.StartsFrom, "starts_to": q.StartsTo}, string(q.Sort), string(q.Order))
}
