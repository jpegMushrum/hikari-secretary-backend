package postgres

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/domain"
)

func listIDs(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]int64, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

func candidateIDs(ctx context.Context, tx pgx.Tx, s domain.Season) ([]int64, error) {
	return listIDs(ctx, tx, "SELECT entry_id FROM mascot_candidates WHERE season_id=$1 AND round=$2 ORDER BY entry_id", s.ID, s.Round)
}

func results(ctx context.Context, tx pgx.Tx, season int64, round int) ([]application.Result, error) {
	rows, err := tx.Query(ctx, `WITH counts AS (
 SELECT e.id,e.name,count(a.person_id) FILTER (WHERE a.liked) likes,count(a.person_id) FILTER (WHERE NOT a.liked) skips
 FROM mascot_candidates c JOIN mascot_entries e ON e.id=c.entry_id
 LEFT JOIN mascot_answers a ON (a.season_id,a.round,a.entry_id)=(c.season_id,c.round,c.entry_id)
 WHERE c.season_id=$1 AND c.round=$2 GROUP BY e.id)
 SELECT id,name,rank() OVER (ORDER BY likes DESC),likes,skips,round(100.0*likes/nullif(likes+skips,0),1)::float8
 FROM counts ORDER BY likes DESC,id`, season, round)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[application.Result])
}

func leaders(ctx context.Context, tx pgx.Tx, s domain.Season) ([]int64, error) {
	scores, err := results(ctx, tx, s.ID, s.Round)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0)
	for _, v := range scores {
		if v.Place != 1 {
			break
		}
		ids = append(ids, v.EntryID)
	}
	return ids, nil
}

func prepareRound(ctx context.Context, tx pgx.Tx, s domain.Season, c application.Command) error {
	var ids []int64
	var err error
	if c.Action == "collect" {
		if s.Phase != "moderation" {
			return domain.ErrConflict
		}
		ids, err = listIDs(ctx, tx, "SELECT id FROM mascot_entries WHERE season_id=$1 AND approved ORDER BY id", s.ID)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return domain.ErrConflict
		}
	} else {
		if s.Phase != "results" || s.WinnerID != nil {
			return domain.ErrConflict
		}
		allowed, err := leaders(ctx, tx, s)
		if err != nil {
			return err
		}
		ids = slices.Clone(c.EntryIDs)
		slices.Sort(ids)
		if len(ids) < 2 || len(slices.Compact(slices.Clone(ids))) != len(ids) {
			return domain.ErrInvalid
		}
		for _, id := range ids {
			if !slices.Contains(allowed, id) {
				return domain.ErrInvalid
			}
		}
	}
	n := s.Round + 1
	if _, err = tx.Exec(ctx, "INSERT INTO mascot_rounds(season_id,number,status) VALUES($1,$2,'ready')", s.ID, n); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mascot_candidates SELECT $1,$2,unnest($3::bigint[])", s.ID, n, ids); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE mascot_seasons SET round=$2,phase='readiness' WHERE id=$1", s.ID, n); err != nil {
		return err
	}
	if len(ids) == 1 {
		s.Round = n
		return finish(ctx, tx, s)
	}
	return nil
}

func allowedVoter(ctx context.Context, tx pgx.Tx, s domain.Season, person int64) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT ($2=1 OR EXISTS(SELECT FROM mascot_voters WHERE season_id=$1 AND round=1 AND person_id=$3 AND NOT excluded))
 AND NOT EXISTS(SELECT FROM mascot_voters WHERE season_id=$1 AND round=$2 AND person_id=$3 AND excluded)`, s.ID, s.Round, person).Scan(&ok)
	return ok, err
}

func ready(ctx context.Context, tx pgx.Tx, s domain.Season, person int64) error {
	if s.Phase != "readiness" {
		return domain.ErrConflict
	}
	ok, err := allowedVoter(ctx, tx, s, person)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrForbidden
	}
	_, err = tx.Exec(ctx, "INSERT INTO mascot_voters(season_id,round,person_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", s.ID, s.Round, person)
	return err
}

func startVoting(ctx context.Context, tx pgx.Tx, s domain.Season) error {
	if s.Phase == "voting" {
		return nil
	}
	if s.Phase != "readiness" {
		return domain.ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE mascot_rounds SET status='voting' WHERE season_id=$1 AND number=$2 AND
 EXISTS(SELECT FROM mascot_voters WHERE season_id=$1 AND round=$2 AND NOT excluded)`, s.ID, s.Round)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	// Freeze a random roster order once. Each group of N voters gets all N
	// rotations of its own shuffled deck, balancing exposure at every position.
	_, err = tx.Exec(ctx, `WITH slots AS (
		SELECT person_id, row_number() OVER (ORDER BY random(),person_id)-1 AS slot
		FROM mascot_voters WHERE season_id=$1 AND round=$2 AND NOT excluded)
		UPDATE mascot_voters v SET ballot_slot=slots.slot FROM slots
		WHERE v.season_id=$1 AND v.round=$2 AND v.person_id=slots.person_id`, s.ID, s.Round)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE mascot_seasons SET phase='voting' WHERE id=$1", s.ID)
	return err
}

func finish(ctx context.Context, tx pgx.Tx, s domain.Season) error {
	ids, err := leaders(ctx, tx, s)
	if err != nil {
		return err
	}
	var winner *int64
	if len(ids) == 1 {
		winner = &ids[0]
	}
	if _, err = tx.Exec(ctx, "UPDATE mascot_rounds SET status='finished' WHERE season_id=$1 AND number=$2", s.ID, s.Round); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE mascot_seasons SET phase='results',winner_id=$2 WHERE id=$1", s.ID, winner)
	return err
}

func choose(ctx context.Context, tx pgx.Tx, s domain.Season, id int64) error {
	var allowed []int64
	var err error
	switch {
	case s.Phase == "results" && s.WinnerID == nil:
		allowed, err = leaders(ctx, tx, s)
	case (s.Phase == "readiness" || s.Phase == "voting") && s.Round > 1:
		allowed, err = candidateIDs(ctx, tx, s)
		if err == nil {
			_, err = tx.Exec(ctx, "UPDATE mascot_rounds SET status='cancelled' WHERE season_id=$1 AND number=$2", s.ID, s.Round)
		}
	case s.Phase == "results" && s.WinnerID != nil && *s.WinnerID == id:
		return nil
	default:
		return domain.ErrConflict
	}
	if err != nil {
		return err
	}
	if !slices.Contains(allowed, id) {
		return domain.ErrInvalid
	}
	_, err = tx.Exec(ctx, "UPDATE mascot_seasons SET phase='results',winner_id=$2 WHERE id=$1", s.ID, id)
	return err
}

func vote(ctx context.Context, tx pgx.Tx, s domain.Season, person int64, c application.Command) error {
	if c.Like == nil || c.EntryID <= 0 {
		return domain.ErrInvalid
	}
	var previous bool
	err := tx.QueryRow(ctx, "SELECT liked FROM mascot_answers WHERE season_id=$1 AND round=$2 AND person_id=$3 AND entry_id=$4", s.ID, s.Round, person, c.EntryID).Scan(&previous)
	if err == nil {
		if previous == *c.Like {
			return nil
		}
		return domain.ErrConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if s.Phase != "voting" {
		return domain.ErrConflict
	}
	tag, err := tx.Exec(ctx, `INSERT INTO mascot_answers(season_id,round,person_id,entry_id,liked)
 SELECT v.season_id,v.round,v.person_id,c.entry_id,$5 FROM mascot_voters v JOIN mascot_candidates c USING(season_id,round)
 WHERE v.season_id=$1 AND v.round=$2 AND v.person_id=$3 AND NOT v.excluded AND c.entry_id=$4`, s.ID, s.Round, person, c.EntryID, *c.Like)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrForbidden
	}
	var complete bool
	err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM mascot_answers WHERE season_id=$1 AND round=$2) =
 (SELECT count(*) FROM mascot_voters WHERE season_id=$1 AND round=$2 AND NOT excluded) *
 (SELECT count(*) FROM mascot_candidates WHERE season_id=$1 AND round=$2)`, s.ID, s.Round).Scan(&complete)
	if err != nil {
		return err
	}
	if complete {
		return finish(ctx, tx, s)
	}
	return nil
}
