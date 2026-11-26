package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/domain"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool} }

const seasonColumns = "id, CASE WHEN phase='submissions' AND deadline<=clock_timestamp() THEN 'moderation' ELSE phase END, deadline, round, winner_id"

func scanSeason(row pgx.Row) (domain.Season, error) {
	var s domain.Season
	err := row.Scan(&s.ID, &s.Phase, &s.Deadline, &s.Round, &s.WinnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.ErrNotFound
	}
	return s, err
}

func (r *Repository) Start(ctx context.Context) (domain.Season, error) {
	return scanSeason(r.pool.QueryRow(ctx, `INSERT INTO mascot_seasons(phase) VALUES('preparation')
 ON CONFLICT ((true)) WHERE phase<>'closed' DO UPDATE SET phase=mascot_seasons.phase RETURNING `+seasonColumns))
}

func (r *Repository) Close(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, "UPDATE mascot_seasons SET phase='closed' WHERE id=$1", id)
	if err == nil && tag.RowsAffected() != 1 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repository) Visibility(ctx context.Context, admin bool) (application.Visibility, error) {
	season, err := scanSeason(r.pool.QueryRow(ctx, "SELECT "+seasonColumns+" FROM mascot_seasons WHERE phase<>'closed' AND ($1 OR phase<>'preparation')", admin))
	if errors.Is(err, domain.ErrNotFound) {
		return application.Visibility{}, nil
	}
	if err != nil {
		return application.Visibility{}, err
	}
	view := application.Visibility{Visible: true}
	if season.Phase == "submissions" {
		view.SubmissionUntil = season.Deadline
	}
	return view, nil
}

func current(ctx context.Context, tx pgx.Tx) (domain.Season, error) {
	return scanSeason(tx.QueryRow(ctx, "SELECT "+seasonColumns+" FROM mascot_seasons WHERE phase<>'closed'"))
}

func lock(ctx context.Context, tx pgx.Tx, id int64) (domain.Season, error) {
	// Evaluate the deadline after acquiring the lock. A submission may have
	// arrived before midnight but spent the remaining time waiting to mutate.
	tag, err := tx.Exec(ctx, "SELECT id FROM mascot_seasons WHERE id=$1 AND phase<>'closed' FOR UPDATE", id)
	if err != nil {
		return domain.Season{}, err
	}
	if tag.RowsAffected() == 0 {
		return domain.Season{}, domain.ErrNotFound
	}
	return scanSeason(tx.QueryRow(ctx, "SELECT "+seasonColumns+" FROM mascot_seasons WHERE id=$1", id))
}

func eligible(ctx context.Context, tx pgx.Tx, person int64) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM event_registrations r JOIN events e ON e.id=r.event_id
 LEFT JOIN attendance a ON a.registration_id=r.id WHERE r.person_id=$1 AND
 (a.attended OR (r.status='registered' AND e.status='active' AND e.starts_at>clock_timestamp())))`, person).Scan(&ok)
	return ok, err
}

func (r *Repository) EndTestSubmissions(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `UPDATE mascot_seasons SET deadline=(date_trunc('day',clock_timestamp() AT TIME ZONE 'Europe/Moscow') AT TIME ZONE 'Europe/Moscow')
 WHERE id=$1 AND phase='submissions' AND deadline>clock_timestamp()`, id)
	if err == nil && tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return err
}

func (r *Repository) ResetTestSeason(ctx context.Context, id int64) error {
	if id <= 0 {
		return domain.ErrInvalid
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE mascot_seasons SET phase='closed' WHERE id=$1 AND phase<>'closed'", id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrConflict
		}
		_, err = tx.Exec(ctx, "INSERT INTO mascot_seasons(phase) VALUES('preparation')")
		return err
	})
}

func (r *Repository) Action(ctx context.Context, person int64, admin bool, c application.Command) error {
	if c.SeasonID <= 0 || (!admin && person <= 0) {
		return domain.ErrInvalid
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		s, err := lock(ctx, tx, c.SeasonID)
		if err != nil {
			return err
		}
		if !admin && s.Phase == "preparation" {
			return domain.ErrNotFound
		}
		if c.Round != s.Round {
			return domain.ErrConflict
		}
		if admin {
			return adminAction(ctx, tx, s, c)
		}
		switch c.Action {
		case "submit", "withdraw":
			return editEntry(ctx, tx, s, person, c)
		case "ready":
			return ready(ctx, tx, s, person)
		case "vote":
			return vote(ctx, tx, s, person, c)
		default:
			return domain.ErrInvalid
		}
	})
}

func adminAction(ctx context.Context, tx pgx.Tx, s domain.Season, c application.Command) error {
	switch c.Action {
	case "open":
		if s.Phase != "preparation" {
			return domain.ErrConflict
		}
		d, err := domain.SubmissionDeadline(c.Deadline, time.Now())
		if err != nil {
			return domain.ErrInvalid
		}
		_, err = tx.Exec(ctx, "UPDATE mascot_seasons SET phase='submissions',deadline=$2 WHERE id=$1", s.ID, d)
		return err
	case "moderate":
		if s.Phase != "moderation" {
			return domain.ErrConflict
		}
		if c.Approved == nil {
			return domain.ErrInvalid
		}
		tag, err := tx.Exec(ctx, "UPDATE mascot_entries SET approved=$3 WHERE season_id=$1 AND id=$2", s.ID, c.EntryID, *c.Approved)
		if err == nil && tag.RowsAffected() != 1 {
			return domain.ErrNotFound
		}
		return err
	case "collect", "runoff":
		return prepareRound(ctx, tx, s, c)
	case "exclude":
		if s.Phase != "readiness" {
			return domain.ErrConflict
		}
		tag, err := tx.Exec(ctx, "UPDATE mascot_voters SET excluded=true WHERE season_id=$1 AND round=$2 AND person_id=$3", s.ID, s.Round, c.PersonID)
		if err == nil && tag.RowsAffected() != 1 {
			return domain.ErrNotFound
		}
		return err
	case "start":
		return startVoting(ctx, tx, s)
	case "finish":
		if s.Phase != "voting" {
			return domain.ErrConflict
		}
		return finish(ctx, tx, s)
	case "choose":
		return choose(ctx, tx, s, c.EntryID)
	case "publish":
		if s.Phase == "published" {
			return nil
		}
		if s.Phase != "results" || s.WinnerID == nil {
			return domain.ErrConflict
		}
		_, err := tx.Exec(ctx, "UPDATE mascot_seasons SET phase='published' WHERE id=$1", s.ID)
		return err
	default:
		return domain.ErrInvalid
	}
}
