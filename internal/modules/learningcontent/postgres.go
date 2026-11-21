package learningcontent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	publishing "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/adapters/postgres"
	publishingapp "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository { return &PostgresRepository{pool} }

func (r *PostgresRepository) Preferences(ctx context.Context) (Options, error) {
	return r.Settings(ctx)
}
func (r *PostgresRepository) History(ctx context.Context) ([]Topic, error) {
	rows, err := r.pool.Query(ctx, `SELECT v.topic FROM ai_topic_versions v JOIN ai_topic_jobs j ON j.id=v.job_id
		ORDER BY j.scheduled_for DESC, v.version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Topic{}
	for rows.Next() {
		var raw map[string]any
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		topic := Topic{}
		for _, key := range []string{"title", "title_ru", "theme", "topic"} {
			if title, ok := raw[key].(string); ok && title != "" {
				topic.Title = title
				break
			}
		}
		topic.VocabularyTheme, _ = raw["vocabulary_theme"].(string)
		for _, word := range historyElements(raw["vocabulary"], "japanese") {
			topic.Vocabulary = append(topic.Vocabulary, VocabularyItem{Japanese: word})
		}
		for _, pattern := range historyElements(raw["grammar"], "pattern") {
			topic.Grammar = append(topic.Grammar, GrammarPoint{Pattern: pattern})
		}
		items = append(items, topic)
	}
	return items, rows.Err()
}

// Imported versions can contain plain strings instead of structured entries.
func historyElements(raw any, field string) []string {
	entries, _ := raw.([]any)
	var elements []string
	for _, entry := range entries {
		if obj, ok := entry.(map[string]any); ok {
			entry = obj[field]
		}
		if value, ok := entry.(string); ok && value != "" {
			elements = append(elements, value)
		}
	}
	return elements
}
func (r *PostgresRepository) ScheduleManual(ctx context.Context, draft domain.ScheduledDraft, topic Topic) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// Reserve the job before locking its publication, as ReplaceQueuedTopic
	// and Complete do. An existing source key retains its original time slot.
	var jobID int64
	var linkedID *int64
	err = tx.QueryRow(ctx, `INSERT INTO ai_topic_jobs(scheduled_for,status,next_attempt_at,prompt_version)
		VALUES(COALESCE((SELECT scheduled_at FROM publications WHERE source_key=NULLIF($1,'')), $2),
			'pending',NOW(),'backend-v1')
		ON CONFLICT(scheduled_for) DO UPDATE SET scheduled_for=EXCLUDED.scheduled_for
		RETURNING id,publication_id`, draft.SourceKey, draft.ScheduledAt).Scan(&jobID, &linkedID)
	if err != nil {
		return 0, err
	}
	id, err := publishing.CreateScheduledTx(ctx, tx, draft)
	if err != nil {
		return 0, err
	}
	// Concurrent first submissions with the same key may initially reserve
	// different times. After CreateScheduledTx resolves the key, the winning
	// job is visible. Roll back the unused reservation on replay.
	var replay bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM ai_topic_jobs WHERE publication_id=$1)", id).Scan(&replay)
	if err != nil {
		return 0, err
	}
	if replay {
		return id, nil
	}
	if linkedID != nil {
		return 0, domain.ErrInvalidState
	}
	if err = saveTopic(ctx, tx, jobID, id, topic); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
func (r *PostgresRepository) ReplaceQueuedTopic(ctx context.Context, publicationID int64, topic Topic) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var jobID int64
	err = tx.QueryRow(ctx, `SELECT id FROM ai_topic_jobs WHERE publication_id=$1 AND status IN ('scheduled','failed') FOR UPDATE`, publicationID).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	text, rich := Render(topic)
	if err = replaceQueuedMaterial(ctx, tx, publicationID, text, rich); errors.Is(err, domain.ErrInvalidState) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err = saveTopic(ctx, tx, jobID, publicationID, topic); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

type Job struct {
	ID            int64
	ScheduledFor  time.Time
	Attempts      int
	PublicationID *int64
	RequestedBy   *int64
	LeaseUntil    time.Time
}

func (r *PostgresRepository) Enqueue(ctx context.Context, scheduled time.Time) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `INSERT INTO ai_topic_jobs(scheduled_for,status,next_attempt_at,prompt_version) VALUES($1,'pending',NOW(),'backend-v1') ON CONFLICT(scheduled_for) DO UPDATE SET updated_at=NOW() RETURNING id`, scheduled).Scan(&id)
	return id, err
}

// LatestGenerationID excludes untouched scheduler placeholders. A requested
// replacement remains the latest generation while it waits for a worker.
const LatestGenerationID = `SELECT id FROM ai_topic_jobs
	WHERE status <> 'pending' OR attempts > 0 OR regeneration_requested_by IS NOT NULL
		OR topic IS NOT NULL OR publication_id IS NOT NULL
	ORDER BY scheduled_for DESC, id DESC LIMIT 1`

func (r *PostgresRepository) RequestRegeneration(ctx context.Context, jobID *int64, actor int64) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	if jobID == nil {
		err = tx.QueryRow(ctx, LatestGenerationID+" FOR UPDATE").Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, domain.ErrInvalidState
		}
	} else {
		err = tx.QueryRow(ctx, "SELECT id FROM ai_topic_jobs WHERE id=$1 FOR UPDATE", *jobID).Scan(&id)
	}
	if err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE ai_topic_jobs SET status='pending',attempts=0,
		next_attempt_at=NOW(),regeneration_requested_by=$2,failure_notified=FALSE,last_error=NULL,updated_at=NOW()
		WHERE id=$1 AND id IN (SELECT id FROM regenerable_ai_jobs)`, id, actor)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, domain.ErrInvalidState
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log(actor_type,actor_id,action,object_type,object_id)
		VALUES('admin',$1,'ai.regenerate','ai_topic_job',$2)`, fmt.Sprint(actor), id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// next_attempt_at doubles as the claim deadline while a job is generating.
// Complete and MarkFailed check it so an expired worker cannot overwrite a newer result.
func (r *PostgresRepository) Claim(ctx context.Context, automatic bool, lease time.Duration) (*Job, error) {
	var job Job
	err := r.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT id FROM ai_topic_jobs WHERE status IN ('pending','retry','generating')
		AND next_attempt_at<=NOW() AND ($1 OR regeneration_requested_by IS NOT NULL)
		ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE ai_topic_jobs j SET status='generating', attempts=attempts+1,
		next_attempt_at=NOW()+$2::interval,updated_at=NOW() FROM candidate c WHERE j.id=c.id
		RETURNING j.id,scheduled_for,attempts,publication_id,regeneration_requested_by,next_attempt_at`,
		automatic, lease).Scan(&job.ID, &job.ScheduledFor, &job.Attempts, &job.PublicationID, &job.RequestedBy, &job.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &job, err
}

func (r *PostgresRepository) Complete(ctx context.Context, job *Job, draft domain.ScheduledDraft, topic Topic) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM ai_topic_jobs WHERE id=$1 AND status='generating'
		AND next_attempt_at=$2 FOR UPDATE`, job.ID, job.LeaseUntil).Scan(&id)
	if err != nil {
		return err
	}
	var publicationID int64
	if job.PublicationID == nil {
		if err := publishingapp.PrepareScheduledDraft(&draft); err != nil {
			return err
		}
		publicationID, err = publishing.CreateScheduledTx(ctx, tx, draft)
		if err != nil {
			return err
		}
	} else {
		publicationID = *job.PublicationID
	}
	if err := replaceQueuedMaterial(ctx, tx, publicationID, draft.Text, draft.RichMessage); err != nil {
		return err
	}
	if err = saveTopic(ctx, tx, job.ID, publicationID, topic); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// All callers already hold the job and publication locks. Serialize this
// final check with the archive write: two providers may have used the same
// history before either response arrived. No network call runs under the lock.
func saveTopic(ctx context.Context, tx pgx.Tx, jobID, publicationID int64, topic Topic) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(734002)"); err != nil {
		return err
	}
	var same bool
	if err := tx.QueryRow(ctx, "SELECT topic IS NOT DISTINCT FROM $2::jsonb FROM ai_topic_jobs WHERE id=$1", jobID, topic).Scan(&same); err != nil {
		return err
	}
	if !same {
		rows, err := tx.Query(ctx, `SELECT COALESCE(NULLIF(topic->>'title',''),NULLIF(topic->>'title_ru',''),
			NULLIF(topic->>'theme',''),topic->>'topic',''), COALESCE(topic->>'vocabulary_theme','') FROM ai_topic_versions`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var title, theme string
			if err = rows.Scan(&title, &theme); err != nil {
				return err
			}
			if err = topic.checkDuplicate(title, theme); err != nil {
				return err
			}
		}
		if err = rows.Err(); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE ai_topic_jobs SET status='scheduled',topic=$2,publication_id=$3,
		last_error=NULL,updated_at=NOW() WHERE id=$1`, jobID, topic, publicationID)
	return err
}

func (r *PostgresRepository) MarkFailed(ctx context.Context, job *Job, message string, retryAt *time.Time) error {
	status := "failed"
	at := time.Now().UTC()
	if retryAt != nil {
		status = "retry"
		at = *retryAt
	}
	_, err := r.pool.Exec(ctx, `UPDATE ai_topic_jobs SET status=$2,next_attempt_at=$3,last_error=$4,updated_at=NOW() WHERE id=$1 AND status='generating' AND next_attempt_at=$5`, job.ID, status, at, message, job.LeaseUntil)
	return err
}

type FailureNotice struct {
	ID           int64     `json:"id"`
	ScheduledFor time.Time `json:"scheduled_for"`
	Attempts     int       `json:"attempts"`
	Error        string    `json:"error"`
}

func (r *PostgresRepository) FailureNotices(ctx context.Context) ([]FailureNotice, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,scheduled_for,attempts,COALESCE(last_error,'') FROM ai_topic_jobs WHERE status='failed' AND regeneration_requested_by IS NULL AND failure_notified=FALSE ORDER BY updated_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []FailureNotice{}
	for rows.Next() {
		var item FailureNotice
		if err = rows.Scan(&item.ID, &item.ScheduledFor, &item.Attempts, &item.Error); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *PostgresRepository) MarkFailureNotified(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, `UPDATE ai_topic_jobs SET failure_notified=TRUE,updated_at=NOW() WHERE id=$1 AND status='failed' AND regeneration_requested_by IS NULL`, id)
	return err
}

func replaceQueuedMaterial(ctx context.Context, tx pgx.Tx, publicationID int64, text string, rich map[string]any) error {
	// ClaimDue locks the publication when claiming a delivery. Read delivery
	// state in a fresh statement after acquiring the same publication lock.
	var status, source string
	if err := tx.QueryRow(ctx, "SELECT status,source FROM publications WHERE id=$1 FOR UPDATE", publicationID).Scan(&status, &source); err != nil {
		return err
	}
	if status != "scheduled" || source != "ai_daily_topic" {
		return domain.ErrInvalidState
	}
	tag, err := tx.Exec(ctx, `UPDATE publications SET text=$2,rich_message=$3 WHERE id=$1
		AND EXISTS(SELECT 1 FROM publication_deliveries WHERE publication_id=$1)
		AND NOT EXISTS(SELECT 1 FROM publication_deliveries WHERE publication_id=$1 AND status<>'pending')`, publicationID, text, rich)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrInvalidState
	}
	return nil
}
