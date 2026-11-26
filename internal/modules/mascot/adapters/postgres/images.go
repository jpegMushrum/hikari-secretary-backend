package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/domain"
)

func (r *Repository) ReserveUpload(ctx context.Context, person, season int64, key string) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		s, err := lock(ctx, tx, season)
		if err != nil {
			return err
		}
		if s.Phase == "preparation" {
			return domain.ErrNotFound
		}
		if s.Phase != "submissions" {
			return domain.ErrConflict
		}
		ok, err := eligible(ctx, tx, person)
		if err != nil {
			return err
		}
		if !ok {
			return domain.ErrForbidden
		}
		var count int
		err = tx.QueryRow(ctx, "SELECT count(*) FROM mascot_images WHERE season_id=$1 AND person_id=$2 AND entry_id IS NULL AND state<>'purging'", season, person).Scan(&count)
		if err != nil {
			return err
		}
		if count >= 8 {
			return domain.ErrConflict
		}
		_, err = tx.Exec(ctx, "INSERT INTO mascot_images(key,season_id,person_id) VALUES($1,$2,$3)", key, season, person)
		return err
	})
}

// ReleaseReservation frees the slot when no object PUT was attempted.
func (r *Repository) ReleaseReservation(ctx context.Context, person int64, key string) error {
	_, err := r.pool.Exec(ctx, "DELETE FROM mascot_images WHERE key=$1 AND person_id=$2 AND state='reserved' AND entry_id IS NULL", key, person)
	return err
}

// DiscardOwnedUpload makes an unbound upload unavailable and queues its object for deletion.
func (r *Repository) DiscardOwnedUpload(ctx context.Context, person int64, key string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE mascot_images SET state='purging'
		WHERE key=$1 AND person_id=$2 AND state='stored' AND entry_id IS NULL`, key, person)
	if err == nil && tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repository) FinishUpload(ctx context.Context, person int64, key string) error {
	tag, err := r.pool.Exec(ctx, "UPDATE mascot_images SET state='stored',touched_at=now() WHERE key=$1 AND person_id=$2 AND state='reserved'", key, person)
	if err == nil && tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return err
}

func (r *Repository) DiscardUpload(ctx context.Context, person int64, key string) error {
	// A timed out object PUT may already have reached the bucket. Keep its key for retryable deletion.
	_, err := r.pool.Exec(ctx, "UPDATE mascot_images SET state='purging' WHERE key=$1 AND person_id=$2 AND entry_id IS NULL", key, person)
	return err
}

func (r *Repository) UnusedUploads(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `WITH batch AS (
 SELECT i.key FROM mascot_images i JOIN mascot_seasons s ON s.id=i.season_id WHERE i.entry_id IS NULL AND
 (i.state='purging' OR i.touched_at<now()-interval '24 hours' OR
 (i.state='stored' AND (s.phase<>'submissions' OR s.deadline<=now())))
 ORDER BY i.touched_at LIMIT 100 FOR UPDATE OF i SKIP LOCKED)
 UPDATE mascot_images i SET state='purging',touched_at=now() FROM batch
 WHERE i.key=batch.key AND i.entry_id IS NULL RETURNING i.key`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (r *Repository) PurgedUpload(ctx context.Context, key string) error {
	_, err := r.pool.Exec(ctx, "DELETE FROM mascot_images WHERE key=$1 AND state='purging' AND entry_id IS NULL", key)
	return err
}

func (r *Repository) CanReadImage(ctx context.Context, person int64, admin bool, key string) (bool, error) {
	var ok bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM mascot_images i JOIN mascot_seasons s ON s.id=i.season_id
 WHERE i.key=$1 AND i.state='stored' AND s.phase<>'closed' AND
 (CASE WHEN $3 THEN i.entry_id IS NOT NULL ELSE i.person_id=$2 OR
 EXISTS(SELECT FROM mascot_candidates c WHERE c.season_id=s.id AND c.entry_id=i.entry_id AND
 (s.phase='published' OR (s.phase='voting' AND s.round=c.round AND EXISTS(
 SELECT FROM mascot_voters v WHERE v.season_id=s.id AND v.round=s.round AND v.person_id=$2 AND NOT v.excluded)))) END))`, key, person, admin).Scan(&ok)
	return ok, err
}
