package santa

import (
	"context"
	cryptorand "crypto/rand"
	"math/rand/v2"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

func (s *Service) AdminAction(ctx context.Context, c AdminCommand) error {
	if c.SeasonID <= 0 || c.Revision <= 0 {
		return ErrInvalid
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		v, err := lockSeason(ctx, tx, c.SeasonID)
		if err != nil {
			return err
		}
		if c.Action == "confirm" && v.Confirmed && (c.Revision == v.confirmedRevision || c.Revision == v.Revision) {
			return nil
		}
		if v.Revision != c.Revision {
			return ErrConflict
		}
		switch c.Action {
		case "settings":
			c.Budget = strings.TrimSpace(c.Budget)
			if c.Budget == "" || utf8.RuneCountInString(c.Budget) > 200 || c.ExchangeAt == nil || c.RegistrationUntil == nil ||
				!c.RegistrationUntil.Before(*c.ExchangeAt) || c.ReminderMinutes < 1 || c.ReminderMinutes > 43200 {
				return ErrInvalid
			}
			_, err = tx.Exec(ctx, `UPDATE santa_seasons SET budget=$2,exchange_at=$3,registration_until=$4,reminder_minutes=$5 WHERE id=$1`, v.ID, c.Budget, c.ExchangeAt, c.RegistrationUntil, c.ReminderMinutes)
			if err == nil && (v.ExchangeAt == nil || !v.ExchangeAt.Equal(*c.ExchangeAt) || v.ReminderMinutes != c.ReminderMinutes) {
				_, err = tx.Exec(ctx, `UPDATE outbox SET available_at=$2::timestamptz-make_interval(mins=>$3)
					WHERE topic='santa.notice' AND aggregate_id=$1 AND status IN ('pending','failed')
					AND COALESCE(payload->>'kind', split_part(idempotency_key, ':', 4))='reminder'`,
					v.ID, c.ExchangeAt, c.ReminderMinutes)
			}
		case "open":
			if v.Status != "paused" && v.Status != "preparation" || v.ExchangeAt == nil || v.RegistrationUntil == nil || v.Budget == "" {
				return ErrConflict
			}
			_, err = tx.Exec(ctx, "UPDATE santa_seasons SET status='open' WHERE id=$1", v.ID)
		case "shuffle", "draft", "confirm":
			err = stageDraw(ctx, tx, v, c)
		case "exclude":
			err = s.exclude(ctx, tx, v, c)
		default:
			return ErrInvalid
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_log(actor_type,actor_id,action,object_type,object_id)
            VALUES ('admin',$1,$2,'santa_season',$3)`, strconv.FormatInt(c.ActorTelegramID, 10), "santa."+c.Action, v.ID)
		if err != nil {
			return err
		}
		return bump(ctx, tx, v.ID)
	})
}

func activeIDs(ctx context.Context, tx pgx.Tx, season int64) ([]int64, error) {
	rows, err := tx.Query(ctx, "SELECT person_id FROM santa_participants WHERE season_id=$1 AND status<>'excluded' ORDER BY person_id", season)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// A random cycle has exactly one incoming and one outgoing edge per person.
// With at least three people the generated draft has no reciprocal pairs.
func draw(ids []int64) []Pair {
	order := append([]int64(nil), ids...)
	var seed [32]byte
	cryptorand.Read(seed[:])
	random := rand.New(rand.NewChaCha8(seed))
	random.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	result := make([]Pair, len(order))
	for i, id := range order {
		result[i] = Pair{id, order[(i+1)%len(order)]}
	}
	return result
}

func validatePairs(ids []int64, assignments []Pair) error {
	if len(ids) != len(assignments) {
		return ErrInvalid
	}
	allowed := make(map[int64]bool, len(ids))
	givers := make(map[int64]bool, len(ids))
	recipients := make(map[int64]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	for _, p := range assignments {
		if !allowed[p.GiverID] || !allowed[p.RecipientID] || p.GiverID == p.RecipientID || givers[p.GiverID] || recipients[p.RecipientID] {
			return ErrInvalid
		}
		givers[p.GiverID] = true
		recipients[p.RecipientID] = true
	}
	return nil
}

func replacePairs(ctx context.Context, tx pgx.Tx, id int64, assignments []Pair) error {
	if _, err := tx.Exec(ctx, "DELETE FROM santa_pairs WHERE season_id=$1", id); err != nil {
		return err
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"santa_pairs"}, []string{"season_id", "giver_id", "recipient_id"}, pgx.CopyFromSlice(len(assignments), func(i int) ([]any, error) {
		return []any{id, assignments[i].GiverID, assignments[i].RecipientID}, nil
	}))
	return err
}

func stageDraw(ctx context.Context, tx pgx.Tx, v Season, c AdminCommand) error {
	if v.Confirmed || v.Status == "preparation" {
		return ErrConflict
	}
	ids, err := activeIDs(ctx, tx, v.ID)
	if err != nil {
		return err
	}
	if len(ids) < 3 {
		return ErrConflict
	}
	switch c.Action {
	case "shuffle":
		return replacePairs(ctx, tx, v.ID, draw(ids))
	case "draft":
		current, err := pairs(ctx, tx, v.ID)
		if err != nil {
			return err
		}
		if len(current) == 0 {
			return ErrConflict
		}
		if err := validatePairs(ids, c.Pairs); err != nil {
			return err
		}
		return replacePairs(ctx, tx, v.ID, c.Pairs)
	default:
		if v.Status != "open" {
			return ErrConflict
		}
		stored, err := pairs(ctx, tx, v.ID)
		if err != nil {
			return err
		}
		if validatePairs(ids, stored) != nil {
			return ErrConflict
		}
		if _, err = tx.Exec(ctx, "UPDATE santa_seasons SET confirmed_revision=revision WHERE id=$1", v.ID); err != nil {
			return err
		}
		for _, p := range stored {
			if err := personNotice(ctx, tx, v, p.GiverID, "draw", "Жеребьёвка Тайного Санты прошла! Открой карточку своего получателя."); err != nil {
				return err
			}
		}
		return nil
	}
}

func (s *Service) exclude(ctx context.Context, tx pgx.Tx, v Season, c AdminCommand) error {
	if c.PersonID <= 0 {
		return ErrInvalid
	}
	tag, err := tx.Exec(ctx, "UPDATE santa_participants SET status='excluded' WHERE season_id=$1 AND person_id=$2 AND status<>'excluded'", v.ID, c.PersonID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if !v.Confirmed {
		if _, err = tx.Exec(ctx, "DELETE FROM santa_pairs WHERE season_id=$1", v.ID); err != nil {
			return err
		}
	} else {
		ids, err := activeIDs(ctx, tx, v.ID)
		if err != nil {
			return err
		}
		if len(ids) < 2 {
			return ErrConflict
		}
		if err := validatePairs(ids, c.Pairs); err != nil {
			return err
		}
		old, err := pairs(ctx, tx, v.ID)
		if err != nil {
			return err
		}
		prior := map[int64]int64{}
		for _, p := range old {
			prior[p.GiverID] = p.RecipientID
		}
		if err = replacePairs(ctx, tx, v.ID, c.Pairs); err != nil {
			return err
		}
		for _, p := range c.Pairs {
			if prior[p.GiverID] == p.RecipientID {
				continue
			}
			if _, err := tx.Exec(ctx, "UPDATE santa_participants SET gift='unknown' WHERE season_id=$1 AND person_id=$2", v.ID, p.GiverID); err != nil {
				return err
			}
			if err := personNotice(ctx, tx, v, p.GiverID, "replacement", "Администратор изменил ваше назначение в Тайном Санте. Откройте обновлённую карточку."); err != nil {
				return err
			}
		}
	}
	if err := personNotice(ctx, tx, v, c.PersonID, "excluded", "Администратор подтвердил ваш выход из Тайного Санты."); err != nil {
		return err
	}
	return s.notifyAdmins(ctx, tx, v, c.PersonID, "excluded_admin", "исключение из сезона подтверждено.")
}
