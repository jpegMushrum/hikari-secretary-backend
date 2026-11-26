package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/domain"
)

func (r *Repository) View(ctx context.Context, person int64, admin bool) (application.View, error) {
	view := application.View{
		Entries: []application.Entry{}, Voters: []application.Voter{},
		Rounds: []application.Round{}, Finalists: []int64{}, PendingImages: []string{},
	}
	// Read phase, assignments and result tables from one snapshot: a concurrent
	// publication must not expose results to a request that still sees a closed ballot.
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	err := pgx.BeginTxFunc(ctx, r.pool, opts, func(tx pgx.Tx) error {
		season, err := current(ctx, tx)
		if err != nil {
			return err
		}
		if !admin && season.Phase == "preparation" {
			return domain.ErrNotFound
		}
		view.Season = season
		if !admin && season.Phase != "published" {
			view.Season.WinnerID = nil
		}
		if !admin {
			view.Eligible, err = eligible(ctx, tx, person)
			if err != nil {
				return err
			}
			if season.Phase == "submissions" {
				rows, err := tx.Query(ctx, `SELECT key FROM mascot_images
					WHERE season_id=$1 AND person_id=$2 AND entry_id IS NULL AND state='stored'
					ORDER BY touched_at,key`, season.ID, person)
				if err != nil {
					return err
				}
				view.PendingImages, err = pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return err
				}
			}
		}
		view.Entries, err = entries(ctx, tx, season.ID, person, admin, 0)
		if err != nil || season.Round == 0 {
			return err
		}
		if admin {
			err = loadRoster(ctx, tx, &view)
		} else {
			err = loadBallot(ctx, tx, person, &view)
		}
		if err != nil {
			return err
		}
		if admin || season.Phase == "published" {
			return loadRounds(ctx, tx, &view)
		}
		return nil
	})
	return view, err
}

func loadRoster(ctx context.Context, tx pgx.Tx, view *application.View) error {
	season := view.Season
	rows, err := tx.Query(ctx, `SELECT v.person_id,coalesce(nullif(p.full_name,''),t.display_name,'Участник'),v.excluded,
   t.telegram_user_id,coalesce(t.username,'')
   FROM mascot_voters v JOIN people p ON p.id=v.person_id
   LEFT JOIN telegram_identities t ON t.person_id=p.id
   WHERE v.season_id=$1 AND v.round=$2 ORDER BY 2,v.person_id`, season.ID, season.Round)
	if err != nil {
		return err
	}
	view.Voters, err = pgx.CollectRows(rows, pgx.RowToStructByPos[application.Voter])
	if err != nil {
		return err
	}
	if season.Round > 1 && (season.Phase == "readiness" || season.Phase == "voting") {
		view.Finalists, err = candidateIDs(ctx, tx, season)
	}
	return err
}

func loadBallot(ctx context.Context, tx pgx.Tx, person int64, view *application.View) error {
	season := view.Season
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM mascot_voters
   WHERE season_id=$1 AND round=$2 AND person_id=$3 AND NOT excluded)`, season.ID, season.Round, person).Scan(&view.Ready)
	if err != nil {
		return err
	}
	if season.Phase == "readiness" {
		view.CanReady, err = allowedVoter(ctx, tx, season, person)
		return err
	}
	if season.Phase != "voting" || !view.Ready {
		return nil
	}
	// Rank the complete deck before removing answered cards. Existing ballots
	// without a slot keep their original per-person order across upgrades.
	pending, err := listIDs(ctx, tx, `WITH deck AS (
		SELECT c.entry_id,v.ballot_slot,count(*) OVER () AS size
		FROM mascot_candidates c JOIN mascot_voters v USING(season_id,round)
		WHERE c.season_id=$1 AND c.round=$2 AND v.person_id=$3),
	ordered AS (
		SELECT *,row_number() OVER (ORDER BY md5(concat_ws('/', $1::bigint, $2::integer, entry_id,
			coalesce(ballot_slot / size,$3::bigint))),entry_id)-1 AS position FROM deck)
	SELECT entry_id FROM ordered d WHERE NOT EXISTS (
		SELECT FROM mascot_answers a WHERE a.season_id=$1 AND a.round=$2
		AND a.person_id=$3 AND a.entry_id=d.entry_id)
	ORDER BY (position+coalesce(ballot_slot,0)) % size`, season.ID, season.Round, person)
	if err != nil {
		return err
	}
	view.Remaining = len(pending)
	if view.Remaining == 0 {
		return nil
	}
	cards, err := entries(ctx, tx, season.ID, person, false, pending[0])
	if err != nil {
		return err
	}
	view.Card = &cards[0] // the candidate FK and repeatable-read snapshot guarantee this row
	return nil
}

func loadRounds(ctx context.Context, tx pgx.Tx, view *application.View) error {
	season := view.Season
	rows, err := tx.Query(ctx, `SELECT number,status='cancelled' FROM mascot_rounds
   WHERE season_id=$1 AND status IN ('finished','cancelled') ORDER BY number`, season.ID)
	if err != nil {
		return err
	}
	rounds, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (application.Round, error) {
		var round application.Round
		err := row.Scan(&round.Number, &round.Cancelled)
		return round, err
	})
	if err != nil {
		return err
	}
	for i := range rounds {
		round := &rounds[i]
		round.Rows, err = results(ctx, tx, season.ID, round.Number)
		if err != nil {
			return err
		}
		if season.Phase == "results" && season.WinnerID == nil && round.Number == season.Round {
			for _, row := range round.Rows {
				if row.Place == 1 {
					view.Finalists = append(view.Finalists, row.EntryID)
				}
			}
		}
	}
	view.Rounds = rounds
	return nil
}

const entryImages = `coalesce((SELECT jsonb_agg(jsonb_build_object('key',key,'ai',ai) ORDER BY position)
	FROM mascot_images WHERE entry_id=e.id),'[]'::jsonb)`

// ResultEntry serves the same anonymous card on a projector and in published member results.
func (r *Repository) ResultEntry(ctx context.Context, id int64, admin bool) (application.Entry, error) {
	var e application.Entry
	err := r.pool.QueryRow(ctx, `SELECT e.id,e.name,e.description,e.approved,`+entryImages+`
		FROM mascot_entries e JOIN mascot_seasons s ON s.id=e.season_id
		WHERE e.id=$1 AND s.phase<>'closed' AND ($2 OR s.phase='published')
		AND EXISTS (SELECT FROM mascot_candidates c JOIN mascot_rounds r ON (r.season_id,r.number)=(c.season_id,c.round)
			WHERE c.season_id=e.season_id AND c.entry_id=e.id AND r.status IN ('finished','cancelled'))`, id, admin).
		Scan(&e.ID, &e.Name, &e.Description, &e.Approved, &e.Images)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, domain.ErrNotFound
	}
	return e, err
}

func entries(ctx context.Context, tx pgx.Tx, season, person int64, admin bool, card int64) ([]application.Entry, error) {
	rows, err := tx.Query(ctx, `SELECT e.id,e.name,e.description,e.approved,
 CASE WHEN $3 THEN coalesce(nullif(p.full_name,''),t.display_name,'Участник') ELSE '' END,
 CASE WHEN $3 THEN t.telegram_user_id END, CASE WHEN $3 THEN coalesce(t.username,'') ELSE '' END,
 `+entryImages+`
 FROM mascot_entries e JOIN people p ON p.id=e.person_id LEFT JOIN telegram_identities t ON t.person_id=p.id
 WHERE e.season_id=$1 AND (CASE WHEN $4::bigint>0 THEN e.id=$4 ELSE ($3 OR e.person_id=$2) END) ORDER BY e.id`, season, person, admin, card)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (application.Entry, error) {
		var e application.Entry
		err := row.Scan(&e.ID, &e.Name, &e.Description, &e.Approved, &e.Author, &e.AuthorTelegramID, &e.AuthorUsername, &e.Images)
		return e, err
	})
}
