package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) CreateDraft(ctx context.Context, draft domain.Draft) (int64, error) {
	entitiesJSON, err := json.Marshal(draft.Entities)
	if err != nil {
		return 0, err
	}
	var rich any
	if draft.RichMessage != nil {
		richJSON, marshalErr := json.Marshal(draft.RichMessage)
		if marshalErr != nil {
			return 0, marshalErr
		}
		rich = string(richJSON)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	if draft.ID > 0 {
		var status string
		if err = tx.QueryRow(ctx, "SELECT status FROM publications WHERE id=$1 FOR UPDATE", draft.ID).Scan(&status); err != nil {
			return 0, err
		}
		if status != "draft" {
			return 0, domain.ErrInvalidState
		}
		if _, err = tx.Exec(ctx, "DELETE FROM publication_media WHERE publication_id=$1", draft.ID); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM publication_deliveries WHERE publication_id=$1", draft.ID); err != nil {
			return 0, err
		}
		err = tx.QueryRow(ctx, "UPDATE publications SET text=$2,entities=$3::jsonb,rich_message=$4::jsonb,source=$5 WHERE id=$1 RETURNING id", draft.ID, draft.Text, string(entitiesJSON), rich, draft.Source).Scan(&id)
	} else {
		err = tx.QueryRow(ctx, `INSERT INTO publications(creator_telegram_id,text,entities,rich_message,source,source_key) VALUES($1,$2,$3::jsonb,$4::jsonb,$5,NULLIF($6,'')) ON CONFLICT(source_key) DO NOTHING RETURNING id`, draft.CreatorTelegramID, draft.Text, string(entitiesJSON), rich, draft.Source, draft.SourceKey).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			if err = tx.QueryRow(ctx, "SELECT id FROM publications WHERE source_key=$1", draft.SourceKey).Scan(&id); err != nil {
				return 0, err
			}
			return id, tx.Commit(ctx)
		}
	}
	if err != nil {
		return 0, err
	}
	for _, media := range draft.Media {
		if _, err = tx.Exec(ctx, `INSERT INTO publication_media(publication_id,position,path) VALUES($1,$2,$3) ON CONFLICT(publication_id,position) DO NOTHING`, id, media.Position, media.Path); err != nil {
			return 0, err
		}
	}
	for _, target := range draft.Targets {
		if _, err = tx.Exec(ctx, `INSERT INTO publication_deliveries(publication_id,target_key,target_name,destination,message_thread_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(publication_id,target_key) DO NOTHING`, id, target.Key, target.Name, target.Destination, target.MessageThreadID); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

func (r *Repository) CreateScheduled(ctx context.Context, scheduled domain.ScheduledDraft) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	id, err := CreateScheduledTx(ctx, tx, scheduled)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// CreateScheduledTx also lets the AI worker commit its job and publication together.
func CreateScheduledTx(ctx context.Context, tx pgx.Tx, scheduled domain.ScheduledDraft) (int64, error) {
	entitiesJSON, err := json.Marshal(scheduled.Entities)
	if err != nil {
		return 0, err
	}
	var rich any
	if scheduled.RichMessage != nil {
		richJSON, marshalErr := json.Marshal(scheduled.RichMessage)
		if marshalErr != nil {
			return 0, marshalErr
		}
		rich = string(richJSON)
	}

	var id int64
	var status, source string
	err = tx.QueryRow(ctx, `
		INSERT INTO publications(creator_telegram_id,text,entities,rich_message,source,source_key)
		VALUES($1,$2,$3::jsonb,$4::jsonb,$5,NULLIF($6,''))
		ON CONFLICT(source_key) DO UPDATE SET source_key=EXCLUDED.source_key
		RETURNING id,status,source`,
		scheduled.CreatorTelegramID, scheduled.Text, string(entitiesJSON), rich,
		scheduled.Source, scheduled.SourceKey,
	).Scan(&id, &status, &source)
	if err != nil {
		return 0, err
	}
	if status == string(domain.StatusScheduled) {
		return id, nil
	}
	if status != string(domain.StatusDraft) {
		return 0, domain.ErrInvalidState
	}

	for _, media := range scheduled.Media {
		if _, err = tx.Exec(ctx, `
			INSERT INTO publication_media(publication_id,position,path)
			VALUES($1,$2,$3)
			ON CONFLICT(publication_id,position) DO UPDATE SET path=EXCLUDED.path`,
			id, media.Position, media.Path,
		); err != nil {
			return 0, err
		}
	}
	for _, target := range scheduled.Targets {
		if _, err = tx.Exec(ctx, `
			INSERT INTO publication_deliveries(publication_id,target_key,target_name,destination,message_thread_id)
			VALUES($1,$2,$3,$4,$5)
			ON CONFLICT(publication_id,target_key) DO UPDATE
			SET target_name=EXCLUDED.target_name,destination=EXCLUDED.destination,message_thread_id=EXCLUDED.message_thread_id`,
			id, target.Key, target.Name, target.Destination, target.MessageThreadID,
		); err != nil {
			return 0, err
		}
	}
	if err = ReserveQueueSlots(ctx, tx, source, 1); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE publications SET status='scheduled',scheduled_at=$2 WHERE id=$1`, id, scheduled.ScheduledAt); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE publication_deliveries SET status='pending',next_attempt_at=$2 WHERE publication_id=$1`, id, scheduled.ScheduledAt); err != nil {
		return 0, err
	}
	return id, nil
}

func ReserveQueueSlots(ctx context.Context, tx pgx.Tx, source string, slots int) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(734001)`); err != nil {
		return err
	}
	var count, aiCount int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE source='ai_daily_topic')
		FROM publications WHERE status='scheduled'`).Scan(&count, &aiCount); err != nil {
		return err
	}
	if count+slots > 5 || (source == "ai_daily_topic" && aiCount+slots > 1) {
		return domain.ErrPendingPublicationLimit
	}
	return nil
}

func (r *Repository) RecoverInterruptedDeliveries(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE publication_deliveries
		SET status='retry', next_attempt_at=NOW()
		WHERE status='publishing'`)
	return err
}

func (r *Repository) ReplaceScheduled(ctx context.Context, id int64, text string, rich map[string]any) (bool, error) {
	raw, err := json.Marshal(rich)
	if err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, `UPDATE publications SET text=$2,rich_message=$3::jsonb WHERE id=$1 AND status='scheduled' AND source='ai_daily_topic'`, id, text, string(raw))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) ToggleTarget(ctx context.Context, publicationID int64, target domain.Target) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM publications WHERE id=$1 FOR UPDATE`, publicationID).Scan(&status); err != nil {
		return err
	}
	if status != string(domain.StatusDraft) {
		return domain.ErrInvalidState
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM publication_deliveries WHERE publication_id=$1 AND target_key=$2)`, publicationID, target.Key).Scan(&exists); err != nil {
		return err
	}
	if exists {
		_, err = tx.Exec(ctx, `DELETE FROM publication_deliveries WHERE publication_id=$1 AND target_key=$2`, publicationID, target.Key)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO publication_deliveries(publication_id,target_key,target_name,destination,message_thread_id) VALUES($1,$2,$3,$4,$5)`, publicationID, target.Key, target.Name, target.Destination, target.MessageThreadID)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) Schedule(ctx context.Context, publicationID int64, when time.Time) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var status, source string
	if err = tx.QueryRow(ctx, `SELECT status,source FROM publications WHERE id=$1 FOR UPDATE`, publicationID).Scan(&status, &source); err != nil {
		return false, err
	}
	if status != "draft" {
		return false, nil
	}
	var invalidEvent bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events e JOIN event_publication_references ref ON ref.event_id=e.id WHERE ref.external_publication_id=$1 AND (e.status<>'active' OR e.starts_at<=$2 OR e.starts_at<=NOW()))`, fmt.Sprint(publicationID), when).Scan(&invalidEvent); err != nil {
		return false, err
	}
	if invalidEvent {
		return false, nil
	}
	if err = ReserveQueueSlots(ctx, tx, source, 1); err != nil {
		return false, err
	}
	var targets int
	if err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM publication_deliveries WHERE publication_id=$1`, publicationID).Scan(&targets); err != nil {
		return false, err
	}
	if targets == 0 {
		return false, nil
	}
	command, err := tx.Exec(ctx, `UPDATE publications SET status='scheduled',scheduled_at=$2 WHERE id=$1 AND status='draft'`, publicationID, when)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() != 1 {
		return false, nil
	}
	_, err = tx.Exec(ctx, `UPDATE publication_deliveries SET status='pending',next_attempt_at=$2 WHERE publication_id=$1`, publicationID, when)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (r *Repository) Publication(ctx context.Context, id int64) (map[string]any, bool, error) {
	var p struct {
		id, creator          int64
		text, status, source string
		scheduled            *time.Time
		entities             []byte
		rich                 []byte
	}
	err := r.pool.QueryRow(ctx, `SELECT id,creator_telegram_id,text,status,source,scheduled_at,entities,rich_message FROM publications WHERE id=$1`, id).Scan(&p.id, &p.creator, &p.text, &p.status, &p.source, &p.scheduled, &p.entities, &p.rich)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, false, nil
		}
		return nil, false, err
	}
	entities := []map[string]any{}
	_ = json.Unmarshal(p.entities, &entities)
	var rich map[string]any
	if len(p.rich) > 0 {
		_ = json.Unmarshal(p.rich, &rich)
	}
	rows, err := r.pool.Query(ctx, `SELECT path FROM publication_media WHERE publication_id=$1 ORDER BY position`, id)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	media := []string{}
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			return nil, false, err
		}
		media = append(media, path)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	deliveryRows, err := r.pool.Query(ctx, `SELECT id,target_key,target_name,destination,message_thread_id,status,attempts,next_attempt_at,external_id,last_error,published_at FROM publication_deliveries WHERE publication_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, false, err
	}
	defer deliveryRows.Close()
	deliveries := []map[string]any{}
	for deliveryRows.Next() {
		var did int64
		var key, name, dest, status string
		var thread *int64
		var attempts int
		var next, published *time.Time
		var external, last *string
		if err = deliveryRows.Scan(&did, &key, &name, &dest, &thread, &status, &attempts, &next, &external, &last, &published); err != nil {
			return nil, false, err
		}
		deliveries = append(deliveries, map[string]any{"id": did, "target_key": key, "target_name": name, "destination": dest, "message_thread_id": thread, "status": status, "attempts": attempts, "next_attempt_at": next, "external_id": external, "last_error": last, "published_at": published})
	}
	if err = deliveryRows.Err(); err != nil {
		return nil, false, err
	}
	var event map[string]any
	var title, eventStatus string
	var starts, ends time.Time
	err = r.pool.QueryRow(ctx, `SELECT e.title,e.starts_at,e.ends_at,e.status FROM events e JOIN event_publication_references r ON r.event_id=e.id WHERE r.external_publication_id=$1 ORDER BY CASE r.provider WHEN 'hikari_publishing' THEN 0 ELSE 1 END LIMIT 1`, fmt.Sprint(id)).Scan(&title, &starts, &ends, &eventStatus)
	if err == nil {
		event = map[string]any{"title": title, "starts_at": starts, "ends_at": ends, "status": eventStatus}
	}
	return map[string]any{"id": p.id, "creator_id": p.creator, "text": p.text, "status": p.status, "source": p.source, "scheduled_at": p.scheduled, "entities": entities, "rich_message": rich, "media_paths": media, "deliveries": deliveries, "event": event}, true, nil
}

func (r *Repository) Queue(ctx context.Context, limit int) ([]map[string]any, error) {
	rows, err := r.pool.Query(ctx, `SELECT p.id,p.creator_telegram_id,p.scheduled_at,p.status,p.source,string_agg(d.target_name, ', ' ORDER BY d.id) FROM publications p JOIN publication_deliveries d ON d.publication_id=p.id WHERE p.status='scheduled' GROUP BY p.id ORDER BY p.scheduled_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, creator int64
		var at time.Time
		var status, source, targets string
		if err = rows.Scan(&id, &creator, &at, &status, &source, &targets); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"id": id, "creator_id": creator, "scheduled_at": at, "status": status, "source": source, "targets": targets})
	}
	return result, rows.Err()
}
func (r *Repository) Cancel(ctx context.Context, id int64) (bool, error) {
	command, err := r.pool.Exec(ctx, `UPDATE publications SET status='cancelled',completed_at=NOW() WHERE id=$1 AND status IN ('draft','scheduled')`, id)
	return command.RowsAffected() == 1, err
}
func (r *Repository) ClaimDue(ctx context.Context) (*domain.Delivery, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var d domain.Delivery
	var entities, rich []byte
	err = tx.QueryRow(ctx, `SELECT d.id,d.publication_id,p.creator_telegram_id,d.target_key,d.target_name,d.destination,d.message_thread_id,d.attempts,p.text,p.entities,p.rich_message,EXISTS(SELECT 1 FROM event_publication_references e WHERE e.external_publication_id=p.id::text AND e.published=FALSE) FROM publication_deliveries d JOIN publications p ON p.id=d.publication_id WHERE p.status='scheduled' AND d.status IN ('pending','retry') AND d.platform='telegram' AND p.scheduled_at<=NOW() AND d.next_attempt_at<=NOW() AND NOT EXISTS (SELECT 1 FROM ai_topic_jobs j WHERE j.publication_id=p.id AND j.regeneration_requested_by IS NOT NULL AND j.status IN ('pending','generating','retry')) ORDER BY d.next_attempt_at,d.id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&d.ID, &d.PublicationID, &d.CreatorTelegramID, &d.TargetKey, &d.TargetName, &d.Destination, &d.MessageThreadID, &d.Attempts, &d.Text, &entities, &rich, &d.EventEnabled)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, tx.Commit(ctx)
		}
		return nil, err
	}
	// The candidate scan may have seen a pre-request snapshot immediately
	// before obtaining the publication lock. Check again under that lock.
	tag, err := tx.Exec(ctx, `UPDATE publication_deliveries SET status='publishing' WHERE id=$1
		AND NOT EXISTS (SELECT 1 FROM ai_topic_jobs WHERE publication_id=$2
			AND regeneration_requested_by IS NOT NULL AND status IN ('pending','generating','retry'))`, d.ID, d.PublicationID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, tx.Commit(ctx)
	}
	_ = json.Unmarshal(entities, &d.Entities)
	if len(rich) > 0 {
		_ = json.Unmarshal(rich, &d.RichMessage)
	}
	rows, err := tx.Query(ctx, `SELECT path FROM publication_media WHERE publication_id=$1 ORDER BY position`, d.PublicationID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			rows.Close()
			return nil, err
		}
		d.MediaPaths = append(d.MediaPaths, path)
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &d, nil
}
func (r *Repository) Succeeded(ctx context.Context, id int64, external string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var publicationID int64
	if err = tx.QueryRow(ctx, `UPDATE publication_deliveries SET status='sent',external_id=$2,published_at=NOW(),last_error=NULL WHERE id=$1 RETURNING publication_id`, id, external).Scan(&publicationID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE publications SET status=CASE WHEN EXISTS(SELECT 1 FROM publication_deliveries WHERE publication_id=$1 AND status NOT IN ('sent','failed')) THEN status WHEN EXISTS(SELECT 1 FROM publication_deliveries WHERE publication_id=$1 AND status='failed') THEN 'failed' ELSE 'sent' END,completed_at=CASE WHEN NOT EXISTS(SELECT 1 FROM publication_deliveries WHERE publication_id=$1 AND status NOT IN ('sent','failed')) THEN NOW() ELSE completed_at END WHERE id=$1`, publicationID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) Failed(ctx context.Context, id int64, reason string, max int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var pub int64
	var attempts int
	if err = tx.QueryRow(ctx, `UPDATE publication_deliveries SET attempts=attempts+1,last_error=$2,status=CASE WHEN attempts+1 >= $3 THEN 'failed' ELSE 'retry' END,next_attempt_at=CASE WHEN attempts+1 >= $3 THEN NULL ELSE NOW() + INTERVAL '1 minute' * LEAST(30, attempts+1) END WHERE id=$1 RETURNING publication_id,attempts`, id, reason, max).Scan(&pub, &attempts); err != nil {
		return err
	}
	if attempts >= max {
		_, err = tx.Exec(ctx, `UPDATE publications SET status=CASE WHEN NOT EXISTS(SELECT 1 FROM publication_deliveries WHERE publication_id=$1 AND status NOT IN ('sent','failed')) THEN 'failed' ELSE status END,completed_at=CASE WHEN NOT EXISTS(SELECT 1 FROM publication_deliveries WHERE publication_id=$1 AND status NOT IN ('sent','failed')) THEN NOW() ELSE completed_at END WHERE id=$1`, pub)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (r *Repository) Notices(ctx context.Context) ([]domain.Notice, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,creator_telegram_id,status FROM publications WHERE status IN ('sent','failed') AND notified=FALSE ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Notice{}
	for rows.Next() {
		var n domain.Notice
		if err = rows.Scan(&n.ID, &n.CreatorTelegramID, &n.Status); err != nil {
			return nil, err
		}
		ds, err := r.pool.Query(ctx, `SELECT target_name,status,COALESCE(last_error,'') FROM publication_deliveries WHERE publication_id=$1 ORDER BY id`, n.ID)
		if err != nil {
			return nil, err
		}
		for ds.Next() {
			var d domain.DeliveryResult
			if err = ds.Scan(&d.TargetName, &d.Status, &d.LastError); err != nil {
				ds.Close()
				return nil, err
			}
			n.Deliveries = append(n.Deliveries, d)
		}
		ds.Close()
		result = append(result, n)
	}
	return result, rows.Err()
}
func (r *Repository) MarkNotified(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, `UPDATE publications SET notified=TRUE WHERE id=$1`, id)
	return err
}
