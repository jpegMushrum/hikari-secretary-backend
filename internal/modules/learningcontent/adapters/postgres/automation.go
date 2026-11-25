package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	publishing "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/adapters/postgres"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type AutomationState struct {
	Enabled             bool   `json:"enabled"`
	Configured          bool   `json:"configured"`
	CanCancelToday      bool   `json:"can_cancel_today"`
	CanCancelQueued     bool   `json:"can_cancel_queued"`
	QueuedPublicationID *int64 `json:"queued_publication_id"`
}

const dailyWindow = `
	FROM ai_topic_jobs j LEFT JOIN publications p ON p.id=j.publication_id
	WHERE j.automatic AND COALESCE(p.scheduled_at,j.scheduled_for)>=$1
	  AND ($2::timestamptz IS NULL OR COALESCE(p.scheduled_at,j.scheduled_for)<$2)`

const dailyCandidates = dailyWindow + `
	  AND (j.publication_id IS NULL AND j.status IN ('pending','retry','generating') OR
	       p.source='ai_daily_topic' AND p.status='scheduled' AND NOT EXISTS (
		 SELECT 1 FROM publication_deliveries d WHERE d.publication_id=p.id AND d.status NOT IN ('pending','retry')))`

// Limit before checking deliveries: an already claimed latest publication
// must not silently switch the button to an older legacy publication.
const cancellableQueuedPublication = `SELECT p.id FROM (
	SELECT id FROM publications WHERE source='ai_daily_topic' AND status='scheduled'
	ORDER BY id DESC LIMIT 1
) p WHERE NOT EXISTS (SELECT 1 FROM publication_deliveries d
	WHERE d.publication_id=p.id AND d.status NOT IN ('pending','retry'))`

func (r *Repository) Automation(ctx context.Context, configured bool, today time.Time) (AutomationState, error) {
	state := AutomationState{Configured: configured}
	err := r.pool.QueryRow(ctx, `SELECT automation_enabled AND $3, EXISTS(SELECT 1`+dailyCandidates+`),
		(`+cancellableQueuedPublication+`) FROM ai_topic_settings WHERE id=1`,
		today, today.AddDate(0, 0, 1), configured).Scan(&state.Enabled, &state.CanCancelToday, &state.QueuedPublicationID)
	state.CanCancelQueued = state.QueuedPublicationID != nil
	return state, err
}

func (r *Repository) CancelQueued(ctx context.Context, publicationID, actor int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT id FROM ai_topic_settings WHERE id=1 FOR UPDATE"); err != nil {
		return err
	}
	// Use the same job -> publication order as replacement and completion.
	// ClaimDue locks the publication before updating delivery status. The
	// statement after that lock sees any claim that won the race.
	if _, err = tx.Exec(ctx, "SELECT id FROM ai_topic_jobs WHERE publication_id=$1 ORDER BY id FOR UPDATE", publicationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "SELECT id FROM publications WHERE id=$1 FOR UPDATE", publicationID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE publications SET status='cancelled',completed_at=NOW()
		WHERE id=$1 AND id=(`+cancellableQueuedPublication+`)`, publicationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrInvalidState
	}
	if _, err = tx.Exec(ctx, `UPDATE ai_topic_jobs SET status='cancelled',paused_by_automation=false,
		updated_at=NOW() WHERE publication_id=$1 AND status IN ('pending','retry','generating')`, publicationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_log(actor_type,actor_id,action,object_type,object_id)
		VALUES('admin',$1,'ai.cancel_queued','publication',$2)`, fmt.Sprint(actor), publicationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Lock jobs before publications (same order as generation completion), then
// check delivery state again after obtaining the publication lock. ClaimDue
// locks the publication while it moves a delivery into publishing.
func cancelDailyJobs(ctx context.Context, tx pgx.Tx, since time.Time, until *time.Time, pause bool) (int, error) {
	rows, err := tx.Query(ctx, "SELECT j.id"+dailyWindow+
		" ORDER BY j.id FOR UPDATE OF j", since, until)
	if err != nil {
		return 0, err
	}
	jobs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, err
	}
	count := 0
	for _, jobID := range jobs {
		// A provider may have linked a new publication while the job lock was
		// contested. Re-read eligibility and linkage from the committed row.
		var publicationID *int64
		err := tx.QueryRow(ctx, "SELECT j.publication_id"+dailyCandidates+" AND j.id=$3", since, until, jobID).Scan(&publicationID)
		if err == pgx.ErrNoRows {
			continue
		}
		if err != nil {
			return 0, err
		}
		if publicationID != nil {
			var status string
			if err := tx.QueryRow(ctx, "SELECT status FROM publications WHERE id=$1 FOR UPDATE", *publicationID).Scan(&status); err != nil {
				return 0, err
			}
			if status != "scheduled" {
				continue
			}
			tag, err := tx.Exec(ctx, `UPDATE publications SET status='cancelled',completed_at=NOW() WHERE id=$1
				AND NOT EXISTS (SELECT 1 FROM publication_deliveries WHERE publication_id=$1 AND status NOT IN ('pending','retry'))`, *publicationID)
			if err != nil {
				return 0, err
			}
			if tag.RowsAffected() == 0 {
				continue
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE ai_topic_jobs SET status='cancelled',paused_by_automation=$2,updated_at=NOW() WHERE id=$1`, jobID, pause); err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}

func (r *Repository) SetAutomation(ctx context.Context, enabled bool, today time.Time, actor int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var previous bool
	if err := tx.QueryRow(ctx, "SELECT automation_enabled FROM ai_topic_settings WHERE id=1 FOR UPDATE").Scan(&previous); err != nil {
		return err
	}
	if previous == enabled {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE ai_topic_settings SET automation_enabled=$1,
		automation_resumed_at=CASE WHEN $1 THEN NOW() ELSE automation_resumed_at END,updated_at=NOW(),updated_by=$2 WHERE id=1`, enabled, actor); err != nil {
		return err
	}
	if enabled {
		// Reuse already prepared material when its slot is still ahead.
		// Skipped days and individually cancelled posts stay cancelled.
		// Old databases can have several prepared editions paused, or a manual
		// publication may have filled the slot in the meantime. Keep those
		// versions in history and free their dates for the regular scheduler.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(734001)"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `WITH prepared AS (
			SELECT id,ROW_NUMBER() OVER (ORDER BY scheduled_for,id) AS priority FROM ai_topic_jobs
			WHERE paused_by_automation AND scheduled_for>NOW() AND publication_id IS NOT NULL
		) UPDATE ai_topic_jobs j SET paused_by_automation=false,slot_released=true,updated_at=NOW()
		FROM prepared WHERE j.id=prepared.id AND (prepared.priority>1 OR EXISTS (
			SELECT 1 FROM publications WHERE status='scheduled' AND source='ai_daily_topic'))`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `WITH resumed AS (
			UPDATE ai_topic_jobs SET status=CASE WHEN publication_id IS NULL THEN 'pending' ELSE 'scheduled' END,
				paused_by_automation=false,attempts=CASE WHEN publication_id IS NULL THEN 0 ELSE attempts END,
				next_attempt_at=NOW(),updated_at=NOW()
			WHERE paused_by_automation AND scheduled_for>NOW() RETURNING publication_id
		) SELECT publication_id FROM resumed WHERE publication_id IS NOT NULL`)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := publishing.ReserveQueueSlots(ctx, tx, "ai_daily_topic", len(ids)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE publications SET status='scheduled',completed_at=NULL WHERE status='cancelled' AND id=ANY($1)", ids); err != nil {
				return err
			}
		}
	} else {
		_, err = cancelDailyJobs(ctx, tx, today.AddDate(0, 0, 1), nil, true)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(actor_type,actor_id,action,object_type,object_id,data)
		VALUES('admin',$1,'ai.automation','ai_topic_settings',1,jsonb_build_object('enabled',$2::boolean))`, fmt.Sprint(actor), enabled); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) CancelToday(ctx context.Context, today time.Time, actor int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT id FROM ai_topic_settings WHERE id=1 FOR UPDATE"); err != nil {
		return err
	}
	tomorrow := today.AddDate(0, 0, 1)
	count, err := cancelDailyJobs(ctx, tx, today, &tomorrow, false)
	if err != nil {
		return err
	}
	if count == 0 {
		return domain.ErrInvalidState
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_log(actor_type,actor_id,action,object_type,object_id)
		VALUES('admin',$1,'ai.cancel_today','ai_topic_settings',1)`, fmt.Sprint(actor)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
