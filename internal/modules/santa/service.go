// Package santa owns the seasonal gift exchange and its private assignments.
package santa

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("Santa season or participant not available")
	ErrInvalid  = errors.New("invalid Santa request")
	ErrConflict = errors.New("Santa state changed or action unavailable")
)

type Service struct {
	db     *pgxpool.Pool
	admins []int64
}

func New(db *pgxpool.Pool, admins []int64) *Service {
	return &Service{db: db, admins: admins}
}

type Season struct {
	ID                int64      `json:"id"`
	Status            string     `json:"status"`
	Budget            string     `json:"budget"`
	ExchangeAt        *time.Time `json:"exchange_at"`
	RegistrationUntil *time.Time `json:"registration_until"`
	ReminderMinutes   int        `json:"reminder_minutes"`
	Revision          int64      `json:"revision"`
	Confirmed         bool       `json:"confirmed"`
	confirmedRevision int64
}
type Participant struct {
	PersonID        int64  `json:"person_id"`
	Name            string `json:"name"`
	Wishes          string `json:"wishes"`
	Status          string `json:"status"`
	Attendance      string `json:"attendance"`
	Gift            string `json:"gift"`
	GiftDescription string `json:"gift_description"`
}
type Recipient struct {
	PersonID   int64   `json:"person_id"`
	Name       string  `json:"name"`
	Wishes     string  `json:"wishes"`
	TelegramID int64   `json:"telegram_id"`
	Username   *string `json:"username"`
}
type AdminParticipant struct {
	Participant
	TelegramID int64   `json:"telegram_id"`
	Username   *string `json:"username"`
}
type Pair struct {
	GiverID     int64 `json:"giver_id"`
	RecipientID int64 `json:"recipient_id"`
}
type AdminView struct {
	Season       Season             `json:"season"`
	Participants []AdminParticipant `json:"participants"`
	Pairs        []Pair             `json:"pairs"`
}
type MemberView struct {
	Season        Season       `json:"season"`
	Participation *Participant `json:"participation"`
	Recipient     *Recipient   `json:"recipient"`
	DraftWishes   string       `json:"draft_wishes"`
}
type AdminCommand struct {
	Action            string     `json:"action"`
	SeasonID          int64      `json:"season_id"`
	Revision          int64      `json:"revision"`
	Budget            string     `json:"budget"`
	ExchangeAt        *time.Time `json:"exchange_at"`
	RegistrationUntil *time.Time `json:"registration_until"`
	ReminderMinutes   int        `json:"reminder_minutes"`
	Pairs             []Pair     `json:"pairs"`
	PersonID          int64      `json:"person_id"`
	ActorTelegramID   int64      `json:"-"`
}
type MemberCommand struct {
	RecipientID     int64   `json:"recipient_id"`
	Action          string  `json:"action"`
	SeasonID        int64   `json:"season_id"`
	Wishes          string  `json:"wishes"`
	Attendance      string  `json:"attendance"`
	Gift            string  `json:"gift"`
	GiftDescription *string `json:"gift_description"`
}

const seasonColumns = "id,status,budget,exchange_at,registration_until,reminder_minutes,revision,COALESCE(confirmed_revision,0)"

func readSeason(row pgx.Row) (Season, error) {
	var v Season
	err := row.Scan(&v.ID, &v.Status, &v.Budget, &v.ExchangeAt, &v.RegistrationUntil, &v.ReminderMinutes, &v.Revision, &v.confirmedRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	v.Confirmed = v.confirmedRevision > 0
	return v, err
}

func (s *Service) Start(ctx context.Context) (Season, error) {
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
func (s *Service) Close(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrInvalid
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
func (s *Service) Reset(ctx context.Context, id, revision, actor int64) error {
	if id <= 0 || revision <= 0 || actor <= 0 {
		return ErrInvalid
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		season, err := lockSeason(ctx, tx, id)
		if err != nil {
			return err
		}
		if revision != season.Revision {
			return ErrConflict
		}
		if err := archiveSeason(ctx, tx, id, "admin", strconv.FormatInt(actor, 10), "santa.test_reset"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO santa_seasons (status,budget,exchange_at,registration_until,reminder_minutes)
			VALUES ('preparation',$1,$2,$3,$4)`, season.Budget, season.ExchangeAt, season.RegistrationUntil, season.ReminderMinutes)
		return err
	})
}

func (s *Service) VisibleSeason(ctx context.Context, admin bool) (*Season, error) {
	v, err := readSeason(s.db.QueryRow(ctx, "SELECT "+seasonColumns+" FROM santa_seasons WHERE status<>'closed' AND ($1 OR status='open')", admin))
	if errors.Is(err, ErrNotFound) {
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

func readParticipant(row pgx.Row) (Participant, error) {
	var p Participant
	err := row.Scan(&p.PersonID, &p.Name, &p.Wishes, &p.Status, &p.Attendance, &p.Gift, &p.GiftDescription)
	return p, err
}

var snapshot = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

func (s *Service) Admin(ctx context.Context) (AdminView, error) {
	var view AdminView
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
		view.Participants, err = pgx.CollectRows(rows, pgx.RowToStructByPos[AdminParticipant])
		if err != nil {
			return err
		}
		view.Pairs, err = pairs(ctx, tx, view.Season.ID)
		return err
	})
	return view, err
}

func (s *Service) Member(ctx context.Context, personID int64) (MemberView, error) {
	var view MemberView
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
		var r Recipient
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
func pairs(ctx context.Context, tx pgx.Tx, id int64) ([]Pair, error) {
	rows, err := tx.Query(ctx, "SELECT giver_id,recipient_id FROM santa_pairs WHERE season_id=$1 ORDER BY giver_id", id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Pair])
}

func lockSeason(ctx context.Context, tx pgx.Tx, id int64) (Season, error) {
	return readSeason(tx.QueryRow(ctx, "SELECT "+seasonColumns+" FROM santa_seasons WHERE id=$1 AND status<>'closed' FOR UPDATE", id))
}
func bump(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, "UPDATE santa_seasons SET revision=revision+1 WHERE id=$1", id)
	return err
}
