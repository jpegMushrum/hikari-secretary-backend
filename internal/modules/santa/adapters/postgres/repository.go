package postgres

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/santa/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/santa/domain"
)

type Repository struct {
	db     *pgxpool.Pool
	admins []int64
}

func NewRepository(db *pgxpool.Pool, admins []int64) *Repository {
	return &Repository{db: db, admins: admins}
}

const seasonColumns = "id,status,budget,exchange_at,registration_until,reminder_minutes,revision,COALESCE(confirmed_revision,0)"

func readSeason(row pgx.Row) (domain.Season, error) {
	var v domain.Season
	err := row.Scan(&v.ID, &v.Status, &v.Budget, &v.ExchangeAt, &v.RegistrationUntil, &v.ReminderMinutes, &v.Revision, &v.ConfirmedRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, domain.ErrNotFound
	}
	v.Confirmed = v.ConfirmedRevision > 0
	return v, err
}

func (s *Repository) Start(ctx context.Context) (domain.Season, error) {
	// A repeat activation returns the existing season without changing its settings.
	return readSeason(s.db.QueryRow(ctx, `INSERT INTO santa_seasons(status) VALUES ('preparation')
        ON CONFLICT ((true)) WHERE status<>'closed' DO UPDATE SET status=santa_seasons.status
        RETURNING `+seasonColumns))
}

func archiveSeason(ctx context.Context, tx pgx.Tx, id int64, actorType, actorID, action string) error {
	if _, err := tx.Exec(ctx, "UPDATE santa_seasons SET status='closed',revision=revision+1 WHERE id=$1", id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_log(actor_type,actor_id,action,object_type,object_id)
		VALUES ($1,$2,$3,'santa_season',$4)`, actorType, actorID, action, id)
	return err
}

// Close is an operator command bound to a specific season, including on retry.
func (s *Repository) Close(ctx context.Context, id int64) error {
	if id <= 0 {
		return domain.ErrInvalid
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		season, err := readSeason(tx.QueryRow(ctx, "SELECT "+seasonColumns+" FROM santa_seasons WHERE id=$1 FOR UPDATE", id))
		if err != nil || season.Status == "closed" {
			return err
		}
		return archiveSeason(ctx, tx, id, "operator", "cli", "santa.close")
	})
}

// Reset starts a clean local test round with the previous settings.
func (s *Repository) Reset(ctx context.Context, id, revision, actor int64) error {
	if id <= 0 || revision <= 0 || actor <= 0 {
		return domain.ErrInvalid
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		season, err := lockSeason(ctx, tx, id)
		if err != nil {
			return err
		}
		if revision != season.Revision {
			return domain.ErrConflict
		}
		if err := archiveSeason(ctx, tx, id, "admin", strconv.FormatInt(actor, 10), "santa.test_reset"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO santa_seasons (status,budget,exchange_at,registration_until,reminder_minutes)
			VALUES ('preparation',$1,$2,$3,$4)`, season.Budget, season.ExchangeAt, season.RegistrationUntil, season.ReminderMinutes)
		return err
	})
}

func (s *Repository) VisibleSeason(ctx context.Context, admin bool) (*domain.Season, error) {
	v, err := readSeason(s.db.QueryRow(ctx, "SELECT "+seasonColumns+" FROM santa_seasons WHERE status<>'closed' AND ($1 OR status='open')", admin))
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

const participantsFrom = ` FROM santa_participants sp JOIN people p ON p.id=sp.person_id
    JOIN telegram_identities t ON t.person_id=p.id `
const participantColumns = `sp.person_id,COALESCE(NULLIF(p.full_name,''),t.display_name),sp.wishes,sp.status,sp.attendance,sp.gift,sp.gift_description`

func readParticipant(row pgx.Row) (domain.Participant, error) {
	var p domain.Participant
	err := row.Scan(&p.PersonID, &p.Name, &p.Wishes, &p.Status, &p.Attendance, &p.Gift, &p.GiftDescription)
	return p, err
}

var snapshot = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

func (s *Repository) Admin(ctx context.Context) (application.AdminView, error) {
	var view application.AdminView
	err := pgx.BeginTxFunc(ctx, s.db, snapshot, func(tx pgx.Tx) error {
		var err error
		view.Season, err = readSeason(tx.QueryRow(ctx, "SELECT "+seasonColumns+" FROM santa_seasons WHERE status<>'closed'"))
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+participantColumns+",t.telegram_user_id,t.username"+participantsFrom+" WHERE sp.season_id=$1 ORDER BY 2,1", view.Season.ID)
		if err != nil {
			return err
		}
		view.Participants, err = pgx.CollectRows(rows, pgx.RowToStructByPos[application.AdminParticipant])
		if err != nil {
			return err
		}
		view.Pairs, err = pairs(ctx, tx, view.Season.ID)
		return err
	})
	return view, err
}

func (s *Repository) Member(ctx context.Context, personID int64) (application.MemberView, error) {
	var view application.MemberView
	err := pgx.BeginTxFunc(ctx, s.db, snapshot, func(tx pgx.Tx) error {
		var err error
		view.Season, err = readSeason(tx.QueryRow(ctx, "SELECT "+seasonColumns+" FROM santa_seasons WHERE status='open'"))
		if err != nil {
			return err
		}
		p, err := readParticipant(tx.QueryRow(ctx, "SELECT "+participantColumns+participantsFrom+" WHERE sp.season_id=$1 AND sp.person_id=$2", view.Season.ID, personID))
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.QueryRow(ctx, `SELECT COALESCE((
				SELECT wishes FROM santa_wish_drafts WHERE season_id=$1 AND person_id=$2
			), '')`, view.Season.ID, personID).Scan(&view.DraftWishes)
		}
		if err != nil {
			return err
		}
		view.Participation = &p
		if !view.Season.Confirmed || p.Status == "excluded" {
			return nil
		}
		var r application.Recipient
		err = tx.QueryRow(ctx, "SELECT sp.person_id,COALESCE(NULLIF(p.full_name,''),t.display_name),sp.wishes,t.telegram_user_id,t.username"+participantsFrom+
			" JOIN santa_pairs a ON a.season_id=sp.season_id AND a.recipient_id=sp.person_id WHERE a.season_id=$1 AND a.giver_id=$2",
			view.Season.ID, personID).Scan(&r.PersonID, &r.Name, &r.Wishes, &r.TelegramID, &r.Username)
		if err != nil {
			return err
		}
		view.Recipient = &r
		return nil
	})
	return view, err
}

func pairs(ctx context.Context, tx pgx.Tx, id int64) ([]domain.Pair, error) {
	rows, err := tx.Query(ctx, "SELECT giver_id,recipient_id FROM santa_pairs WHERE season_id=$1 ORDER BY giver_id", id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[domain.Pair])
}

func lockSeason(ctx context.Context, tx pgx.Tx, id int64) (domain.Season, error) {
	return readSeason(tx.QueryRow(ctx, "SELECT "+seasonColumns+" FROM santa_seasons WHERE id=$1 AND status<>'closed' FOR UPDATE", id))
}

func bump(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, "UPDATE santa_seasons SET revision=revision+1 WHERE id=$1", id)
	return err
}
