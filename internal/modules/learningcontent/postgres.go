package learningcontent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository { return &PostgresRepository{pool} }

func (r *PostgresRepository) Preferences(ctx context.Context) (Options, error) {
	var value Options
	err := r.pool.QueryRow(ctx, `SELECT jlpt_level,admin_comment FROM ai_topic_settings WHERE id=1`).Scan(&value.JLPTLevel, &value.AdminComment)
	return value, err
}
func (r *PostgresRepository) History(ctx context.Context, exclude *int64) ([]Topic, error) {
	rows, err := r.pool.Query(ctx, `SELECT topic FROM ai_topic_jobs WHERE status='scheduled' AND topic IS NOT NULL AND ($1::bigint IS NULL OR publication_id<>$1) ORDER BY scheduled_for DESC`, exclude)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Topic{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var topic Topic
		if json.Unmarshal(raw, &topic) == nil {
			items = append(items, topic)
		}
	}
	return items, rows.Err()
}
func (r *PostgresRepository) RecordManual(ctx context.Context, publicationID int64, topic Topic) error {
	raw, err := json.Marshal(topic)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO ai_topic_jobs(scheduled_for,status,next_attempt_at,prompt_version,topic,publication_id) SELECT scheduled_at,'scheduled',NOW(),'backend-v1',$2::jsonb,id FROM publications WHERE id=$1 ON CONFLICT(scheduled_for) DO UPDATE SET status='scheduled',topic=EXCLUDED.topic,publication_id=EXCLUDED.publication_id,last_error=NULL,updated_at=NOW()`, publicationID, string(raw))
	return err
}
func (r *PostgresRepository) ReplaceHistory(ctx context.Context, publicationID int64, topic Topic) (bool, error) {
	raw, err := json.Marshal(topic)
	if err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, `UPDATE ai_topic_jobs SET topic=$2::jsonb,updated_at=NOW() WHERE publication_id=$1 AND status='scheduled'`, publicationID, string(raw))
	return tag.RowsAffected() == 1, err
}

type Job struct {
	ID           int64
	ScheduledFor time.Time
	Attempts     int
}

func (r *PostgresRepository) Enqueue(ctx context.Context, scheduled time.Time) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `INSERT INTO ai_topic_jobs(scheduled_for,status,next_attempt_at,prompt_version) VALUES($1,'pending',NOW(),'backend-v1') ON CONFLICT(scheduled_for) DO UPDATE SET updated_at=NOW() RETURNING id`, scheduled).Scan(&id)
	return id, err
}
func (r *PostgresRepository) Claim(ctx context.Context) (*Job, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var job Job
	err = tx.QueryRow(ctx, `SELECT id,scheduled_for,attempts+1 FROM ai_topic_jobs WHERE status IN ('pending','retry') AND next_attempt_at<=NOW() ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&job.ID, &job.ScheduledFor, &job.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE ai_topic_jobs SET status='generating',attempts=attempts+1,updated_at=NOW() WHERE id=$1`, job.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &job, nil
}
func (r *PostgresRepository) MarkScheduled(ctx context.Context, id, publicationID int64, topic Topic) error {
	raw, _ := json.Marshal(topic)
	tag, err := r.pool.Exec(ctx, `UPDATE ai_topic_jobs SET status='scheduled',publication_id=$2,topic=$3::jsonb,last_error=NULL,updated_at=NOW() WHERE id=$1 AND status='generating'`, id, publicationID, string(raw))
	if err == nil && tag.RowsAffected() != 1 {
		return errors.New("AI job state changed")
	}
	return err
}
func (r *PostgresRepository) MarkFailed(ctx context.Context, id int64, message string, retryAt *time.Time) error {
	status := "failed"
	at := time.Now().UTC()
	if retryAt != nil {
		status = "retry"
		at = *retryAt
	}
	_, err := r.pool.Exec(ctx, `UPDATE ai_topic_jobs SET status=$2,next_attempt_at=$3,last_error=$4,updated_at=NOW() WHERE id=$1 AND status='generating'`, id, status, at, message)
	return err
}

type FailureNotice struct {
	ID           int64     `json:"id"`
	ScheduledFor time.Time `json:"scheduled_for"`
	Attempts     int       `json:"attempts"`
	Error        string    `json:"error"`
}

func (r *PostgresRepository) FailureNotices(ctx context.Context) ([]FailureNotice, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,scheduled_for,attempts,COALESCE(last_error,'') FROM ai_topic_jobs WHERE status='failed' AND failure_notified=FALSE ORDER BY updated_at`)
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
	_, err := r.pool.Exec(ctx, `UPDATE ai_topic_jobs SET failure_notified=TRUE,updated_at=NOW() WHERE id=$1 AND status='failed'`, id)
	return err
}
