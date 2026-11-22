package learningcontent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

// FailUnavailableManualGenerations is used at startup when the AI worker is
// disabled. An accepted request must reach a terminal state and release its previous edition
// for delivery. Automatic jobs resume normally when their worker returns.
func (r *PostgresRepository) FailUnavailableManualGenerations(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `UPDATE ai_topic_jobs j SET status='failed',last_error='Генератор отключён',updated_at=NOW()
		WHERE j.status IN ('pending','generating','retry') AND j.regeneration_requested_by IS NOT NULL`)
	return err
}

// QueueGeneration returns the job the UI should track. New editions retain
// the scheduled time of a queued post even when newer jobs exist in history.
func (r *PostgresRepository) QueueGeneration(ctx context.Context, slot time.Time, actor int64) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// Serializes concurrent submissions with slot creation and automation changes.
	if _, err = tx.Exec(ctx, "SELECT id FROM ai_topic_settings WHERE id=1 FOR UPDATE"); err != nil {
		return 0, err
	}
	var id int64
	var publicationID *int64
	err = tx.QueryRow(ctx, `SELECT p.id FROM publications p
		WHERE p.status='scheduled' AND p.source='ai_daily_topic'
		ORDER BY EXISTS(SELECT 1 FROM publication_deliveries d WHERE d.publication_id=p.id AND d.status<>'pending'),
			p.scheduled_at,p.id LIMIT 1`).Scan(&publicationID)
	if err == nil {
		// A legacy publication can be queued without an associated generation.
		// It can be cancelled, but has no safe generation history to replace.
		err = tx.QueryRow(ctx, "SELECT id FROM ai_topic_jobs WHERE publication_id=$1 FOR UPDATE", *publicationID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, domain.ErrInvalidState
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// A second click can arrive before the first generated any content,
		// including when the deployment has no configured daily time.
		err = tx.QueryRow(ctx, `SELECT id,publication_id FROM ai_topic_jobs WHERE publication_id IS NULL
			AND regeneration_requested_by IS NOT NULL AND status IN ('pending','generating','retry')
			ORDER BY scheduled_for,id LIMIT 1 FOR UPDATE`).Scan(&id, &publicationID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Lock the scheduler's job before reading its publication: the worker
		// might have just finished and linked it while we waited for the lock.
		err = tx.QueryRow(ctx, `SELECT id,publication_id FROM ai_topic_jobs
			WHERE scheduled_for=$1 AND NOT slot_released FOR UPDATE`, slot).Scan(&id, &publicationID)
		if err == nil && publicationID != nil {
			var status string
			err = tx.QueryRow(ctx, "SELECT status FROM publications WHERE id=$1 FOR UPDATE", *publicationID).Scan(&status)
			if err == nil && status != "scheduled" {
				// Keep the completed edition and its publication in history.
				// Only its reservation of this scheduling slot is released.
				if _, err = tx.Exec(ctx, `UPDATE ai_topic_jobs SET slot_released=true,paused_by_automation=false WHERE id=$1`, id); err != nil {
					return 0, err
				}
				err = pgx.ErrNoRows
			}
		}
		if errors.Is(err, pgx.ErrNoRows) {
			publicationID = nil
			err = tx.QueryRow(ctx, `INSERT INTO ai_topic_jobs(scheduled_for,status,next_attempt_at,prompt_version)
				VALUES($1,'pending',NOW(),'backend-v1') RETURNING id`, slot).Scan(&id)
		}
	}
	if err != nil {
		return 0, err
	}
	if publicationID != nil {
		// Delivery claims also lock the publication. After obtaining the lock,
		// use a fresh snapshot to see any already-claimed delivery.
		var status string
		if err := tx.QueryRow(ctx, "SELECT status FROM publications WHERE id=$1 FOR UPDATE", *publicationID).Scan(&status); err != nil {
			return 0, err
		}
		var safe bool
		if err := tx.QueryRow(ctx, `SELECT COALESCE(bool_and(status='pending'),false)
			FROM publication_deliveries WHERE publication_id=$1`, *publicationID).Scan(&safe); err != nil {
			return 0, err
		}
		if status != "scheduled" || !safe {
			return 0, domain.ErrInvalidState
		}
	}
	// Only replay a manual request already in flight. Fence an older automatic
	// generation so the new request picks up the administrator's current settings.
	_, err = tx.Exec(ctx, `UPDATE ai_topic_jobs SET
		next_attempt_at=CASE WHEN status='generating' AND regeneration_requested_by IS NOT NULL THEN next_attempt_at ELSE NOW() END,
		attempts=CASE WHEN status='generating' AND regeneration_requested_by IS NOT NULL THEN attempts ELSE 0 END,
		status=CASE WHEN status='generating' AND regeneration_requested_by IS NOT NULL THEN status ELSE 'pending' END,
		regeneration_requested_by=$2,automatic=true,paused_by_automation=false,failure_notified=false,
		last_error=NULL,updated_at=NOW() WHERE id=$1`, id, actor)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(actor_type,actor_id,action,object_type,object_id)
		VALUES('admin',$1,'ai.queue_generation','ai_topic_job',$2)`, fmt.Sprint(actor), id); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
