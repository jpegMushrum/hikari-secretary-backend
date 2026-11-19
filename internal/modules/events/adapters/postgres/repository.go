package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/database"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool} }

func eventStatusSQL(alias string) string {
	return "CASE WHEN " + alias + ".status='active' AND " + alias + ".ends_at<=NOW() THEN 'completed' ELSE " + alias + ".status END"
}
func (r *Repository) List(ctx context.Context, q application.ListEventsQuery) (application.ListEventsResult, error) {
	where, args := eventWhere(q)
	var total int64
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM events e "+where, args...).Scan(&total); err != nil {
		return application.ListEventsResult{}, fmt.Errorf("count events: %w", err)
	}
	limit, offset := len(args)+1, len(args)+2
	sql := fmt.Sprintf(`SELECT e.id,r.external_publication_id::bigint,e.title,e.starts_at,e.ends_at,%s,COALESCE(r.published,FALSE),COUNT(er.id) FILTER(WHERE er.status='registered'),COUNT(er.id) FILTER(WHERE er.status='cancelled'),COUNT(a.registration_id) FILTER(WHERE a.attended),e.created_at FROM events e LEFT JOIN event_publication_references r ON r.event_id=e.id LEFT JOIN event_registrations er ON er.event_id=e.id LEFT JOIN attendance a ON a.registration_id=er.id %s GROUP BY e.id,r.external_publication_id,r.published ORDER BY %s LIMIT $%d OFFSET $%d`, eventStatusSQL("e"), where, eventOrder(q), limit, offset)
	rows, err := r.pool.Query(ctx, sql, append(args, q.Page.Size, q.Page.Offset())...)
	if err != nil {
		return application.ListEventsResult{}, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	items := make([]application.Event, 0, q.Page.Size)
	for rows.Next() {
		var item application.Event
		if err = rows.Scan(&item.ID, &item.PublicationID, &item.Title, &item.StartsAt, &item.EndsAt, &item.Status, &item.Published, &item.Registrations, &item.CancelledRegistrations, &item.Attended, &item.CreatedAt); err != nil {
			return application.ListEventsResult{}, err
		}
		items = append(items, item)
	}
	return application.ListEventsResult{Items: items, TotalItems: total}, rows.Err()
}
func eventWhere(q application.ListEventsQuery) (string, []any) {
	clauses := []string{}
	args := []any{}
	add := func(format string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(format, len(args)))
	}
	if q.Status != "" {
		add(eventStatusSQL("e")+"=$%d", q.Status)
	}
	if value := strings.TrimSpace(q.Search); value != "" {
		add("(e.title ILIKE $%[1]d OR e.id::text LIKE $%[1]d)", database.ContainsPattern(value))
	}
	if q.StartsFrom != nil {
		add("e.starts_at>=$%d", *q.StartsFrom)
	}
	if q.StartsTo != nil {
		add("e.starts_at<$%d", *q.StartsTo)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}
func eventOrder(q application.ListEventsQuery) string {
	direction := "ASC"
	if q.Order == application.SortDescending {
		direction = "DESC"
	}
	field := "e.starts_at"
	switch q.Sort {
	case application.SortByTitle:
		field = "e.title"
	case application.SortByCreatedAt:
		field = "e.created_at"
	case "id":
		field = "e.id"
	case "status":
		field = eventStatusSQL("e")
	case "registrations":
		field = "COUNT(er.id) FILTER(WHERE er.status='registered')"
	case "attended":
		field = "COUNT(a.registration_id) FILTER(WHERE a.attended)"
	}
	return field + " " + direction + ",e.id " + direction
}
func (r *Repository) Get(ctx context.Context, id int64) (application.Event, error) {
	var item application.Event
	err := r.pool.QueryRow(ctx, fmt.Sprintf(`SELECT e.id,r.external_publication_id::bigint,e.title,e.starts_at,e.ends_at,%s,COALESCE(r.published,FALSE),COUNT(er.id) FILTER(WHERE er.status='registered'),COUNT(er.id) FILTER(WHERE er.status='cancelled'),COUNT(a.registration_id) FILTER(WHERE a.attended),e.created_at FROM events e LEFT JOIN event_publication_references r ON r.event_id=e.id LEFT JOIN event_registrations er ON er.event_id=e.id LEFT JOIN attendance a ON a.registration_id=er.id WHERE e.id=$1 GROUP BY e.id,r.external_publication_id,r.published`, eventStatusSQL("e")), id).Scan(&item.ID, &item.PublicationID, &item.Title, &item.StartsAt, &item.EndsAt, &item.Status, &item.Published, &item.Registrations, &item.CancelledRegistrations, &item.Attended, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Event{}, application.ErrEventNotFound
	}
	return item, err
}
func (r *Repository) ListParticipants(ctx context.Context, eventID int64, q application.ListParticipantsQuery) (application.ListParticipantsResult, error) {
	where, args := participantWhere(eventID, q)
	var total int64
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM event_registrations er JOIN people p ON p.id=er.person_id "+where, args...).Scan(&total); err != nil {
		return application.ListParticipantsResult{}, err
	}
	limit, offset := len(args)+1, len(args)+2
	sql := fmt.Sprintf(`SELECT p.id,ti.telegram_user_id,COALESCE(p.full_name,ti.display_name),ti.username,er.registered_at,er.cancelled_at,er.status,a.attended,p.is_russian_citizen,p.is_itmo_student,(NOT COALESCE(p.is_russian_citizen,FALSE) OR NOT COALESCE(p.is_itmo_student,FALSE)) FROM event_registrations er JOIN people p ON p.id=er.person_id JOIN telegram_identities ti ON ti.person_id=p.id LEFT JOIN attendance a ON a.registration_id=er.id %s ORDER BY %s LIMIT $%d OFFSET $%d`, where, participantOrder(q), limit, offset)
	rows, err := r.pool.Query(ctx, sql, append(args, q.Page.Size, q.Page.Offset())...)
	if err != nil {
		return application.ListParticipantsResult{}, err
	}
	defer rows.Close()
	items := make([]application.Participant, 0, q.Page.Size)
	for rows.Next() {
		var item application.Participant
		if err = rows.Scan(&item.PersonID, &item.TelegramUserID, &item.FullName, &item.Username, &item.RegisteredAt, &item.CancelledAt, &item.Status, &item.Attended, &item.IsRussianCitizen, &item.IsITMOStudent, &item.NeedsPass); err != nil {
			return application.ListParticipantsResult{}, err
		}
		items = append(items, item)
	}
	return application.ListParticipantsResult{Items: items, TotalItems: total}, rows.Err()
}
func participantWhere(eventID int64, q application.ListParticipantsQuery) (string, []any) {
	clauses := []string{"er.event_id=$1"}
	args := []any{eventID}
	add := func(format string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(format, len(args)))
	}
	if q.Status != "" {
		add("er.status=$%d", q.Status)
	}
	if q.IsRussianCitizen != nil {
		add("p.is_russian_citizen=$%d", *q.IsRussianCitizen)
	}
	if q.IsITMOStudent != nil {
		add("p.is_itmo_student=$%d", *q.IsITMOStudent)
	}
	if q.NeedsPass != nil {
		add("(NOT COALESCE(p.is_russian_citizen,FALSE) OR NOT COALESCE(p.is_itmo_student,FALSE))=$%d", *q.NeedsPass)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}
func participantOrder(q application.ListParticipantsQuery) string {
	direction := "ASC"
	if q.Order == application.SortDescending {
		direction = "DESC"
	}
	field := "COALESCE(p.full_name,ti.display_name)"
	switch q.Sort {
	case application.ParticipantByRegisteredAt:
		field = "er.registered_at"
	case "person_id":
		field = "er.person_id"
	case "telegram":
		field = "LOWER(ti.username)"
	case "cancelled_at":
		field = "er.cancelled_at"
	case "status":
		field = "er.status"
	case "attended":
		field = "a.attended"
	case "is_russian_citizen":
		field = "p.is_russian_citizen"
	case "is_itmo_student":
		field = "p.is_itmo_student"
	case application.ParticipantByNeedsPass:
		field = "NOT (p.is_russian_citizen AND p.is_itmo_student)"
	}
	return field + " " + direction + " NULLS LAST,er.person_id " + direction
}
func (r *Repository) Cancel(ctx context.Context, id, actor int64) (application.CancelResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return application.CancelResult{}, err
	}
	defer tx.Rollback(ctx)
	var title string
	err = tx.QueryRow(ctx, `UPDATE events SET status='cancelled',cancelled_at=NOW(),updated_at=NOW() WHERE id=$1 AND status='active' AND ends_at>NOW() RETURNING title`, id).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		_ = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE id=$1)`, id).Scan(&exists)
		if !exists {
			return application.CancelResult{}, application.ErrEventNotFound
		}
		return application.CancelResult{}, application.ErrEventNotActive
	}
	if err != nil {
		return application.CancelResult{}, err
	}
	rows, err := tx.Query(ctx, `SELECT er.id,ti.telegram_user_id FROM event_registrations er JOIN telegram_identities ti ON ti.person_id=er.person_id WHERE er.event_id=$1 AND er.status='registered'`, id)
	if err != nil {
		return application.CancelResult{}, err
	}
	type target struct{ id, user int64 }
	targets := []target{}
	for rows.Next() {
		var value target
		if err = rows.Scan(&value.id, &value.user); err != nil {
			rows.Close()
			return application.CancelResult{}, err
		}
		targets = append(targets, value)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return application.CancelResult{}, err
	}
	for _, target := range targets {
		payload, _ := json.Marshal(map[string]any{"telegram_user_id": target.user, "event_id": id, "title": title})
		if _, err = tx.Exec(ctx, `INSERT INTO outbox(topic,aggregate_type,aggregate_id,payload,idempotency_key) VALUES('event.cancelled','event_registration',$1,$2::jsonb,$3) ON CONFLICT(idempotency_key) DO NOTHING`, target.id, string(payload), fmt.Sprintf("event-cancelled:%d:%d", id, target.id)); err != nil {
			return application.CancelResult{}, err
		}
	}
	data, _ := json.Marshal(map[string]any{"notifications_queued": len(targets)})
	actorType := "telegram_admin"
	if actor == 0 {
		actorType = "bot_service"
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log(actor_type,actor_id,action,object_type,object_id,idempotency_key,data) VALUES($5,$1,'event.cancelled','event',$2,$3,$4::jsonb) ON CONFLICT(actor_type,idempotency_key) DO NOTHING`, fmt.Sprint(actor), id, fmt.Sprintf("cancel-event:%d", id), string(data), actorType)
	if err != nil {
		return application.CancelResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return application.CancelResult{}, err
	}
	event, err := r.Get(ctx, id)
	return application.CancelResult{Event: event, NotificationsQueued: int64(len(targets))}, err
}
