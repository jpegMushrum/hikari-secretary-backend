package santa

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

func (s *Service) MemberAction(ctx context.Context, personID int64, c MemberCommand) error {
	if personID <= 0 || c.SeasonID <= 0 {
		return ErrInvalid
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		v, err := lockSeason(ctx, tx, c.SeasonID)
		if err != nil {
			return err
		}
		if v.Status != "open" {
			return ErrNotFound
		}
		p, err := readParticipant(tx.QueryRow(ctx, "SELECT "+participantColumns+participantsFrom+" WHERE sp.season_id=$1 AND sp.person_id=$2", v.ID, personID))
		found := !errors.Is(err, pgx.ErrNoRows)
		if found && err != nil {
			return err
		}
		if found && p.Status == "excluded" {
			return ErrConflict
		}
		if !found && c.Action != "join" {
			return ErrNotFound
		}
		signup := !v.Confirmed && v.RegistrationUntil != nil && time.Now().Before(*v.RegistrationUntil)
		c.Wishes = strings.TrimSpace(c.Wishes)
		switch c.Action {
		case "join", "wishes":
			if c.Wishes == "" || utf8.RuneCountInString(c.Wishes) > 2000 {
				return ErrInvalid
			}
			if c.Action == "join" {
				if !signup {
					return ErrConflict
				}
				if found {
					if p.Wishes == c.Wishes {
						return nil
					}
					return ErrConflict
				}
				if _, err = tx.Exec(ctx, "INSERT INTO santa_participants(season_id,person_id,wishes) VALUES ($1,$2,$3)", v.ID, personID, c.Wishes); err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, "DELETE FROM santa_wish_drafts WHERE season_id=$1 AND person_id=$2", v.ID, personID); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, "DELETE FROM santa_pairs WHERE season_id=$1", v.ID)
			} else {
				if p.Wishes == c.Wishes {
					return nil
				}
				if _, err = tx.Exec(ctx, "UPDATE santa_participants SET wishes=$3 WHERE season_id=$1 AND person_id=$2", v.ID, personID, c.Wishes); err != nil {
					return err
				}
				if v.Confirmed {
					var giver int64
					if err = tx.QueryRow(ctx, "SELECT giver_id FROM santa_pairs WHERE season_id=$1 AND recipient_id=$2", v.ID, personID).Scan(&giver); err != nil {
						return err
					}
					err = personNotice(ctx, tx, v, giver, "wishes", "Ваш получатель изменил пожелания в Тайном Санте. Откройте карточку, чтобы их прочитать.")
				}
			}
		case "cancel":
			if !signup {
				return ErrConflict
			}
			if _, err = tx.Exec(ctx, "DELETE FROM santa_pairs WHERE season_id=$1", v.ID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "INSERT INTO santa_wish_drafts (season_id,person_id,wishes) VALUES ($1,$2,$3)", v.ID, personID, p.Wishes); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "DELETE FROM santa_participants WHERE season_id=$1 AND person_id=$2", v.ID, personID)
		case "attendance":
			if !v.Confirmed {
				return ErrConflict
			}
			if c.Attendance != "yes" && c.Attendance != "no" {
				return ErrInvalid
			}
			if c.Attendance == p.Attendance {
				return nil
			}
			if _, err = tx.Exec(ctx, "UPDATE santa_participants SET attendance=$3 WHERE season_id=$1 AND person_id=$2", v.ID, personID, c.Attendance); err != nil {
				return err
			}
			if c.Attendance == "no" {
				err = s.notifyAdmins(ctx, tx, v, personID, "attendance", "не сможет прийти на обмен.")
			} else if p.Attendance == "no" {
				err = s.notifyAdmins(ctx, tx, v, personID, "attendance", "всё-таки придёт на обмен.")
			}
		case "gift", "gift_description":
			if !v.Confirmed {
				return ErrConflict
			}
			var current int64
			if err = tx.QueryRow(ctx, "SELECT recipient_id FROM santa_pairs WHERE season_id=$1 AND giver_id=$2", v.ID, personID).Scan(&current); err != nil {
				return err
			}
			if current != c.RecipientID {
				return ErrConflict
			}
			if c.Action == "gift_description" {
				if c.GiftDescription == nil || utf8.RuneCountInString(*c.GiftDescription) > 2000 {
					return ErrInvalid
				}
				description := strings.TrimSpace(*c.GiftDescription)
				if description == p.GiftDescription {
					return nil
				}
				_, err = tx.Exec(ctx, "UPDATE santa_participants SET gift_description=$3 WHERE season_id=$1 AND person_id=$2", v.ID, personID, description)
				break
			}
			if c.Gift != "ready" && c.Gift != "ordered" && c.Gift != "not_ready" {
				return ErrInvalid
			}
			if c.Gift == p.Gift {
				return nil
			}
			if _, err = tx.Exec(ctx, "UPDATE santa_participants SET gift=$3 WHERE season_id=$1 AND person_id=$2", v.ID, personID, c.Gift); err != nil {
				return err
			}
		case "request_exit", "retract_exit":
			if !v.Confirmed {
				return ErrConflict
			}
			status, msg := "exit_requested", "просит подтвердить выход из сезона."
			if c.Action == "retract_exit" {
				status, msg = "active", "отозвал запрос на выход."
			}
			if p.Status == status {
				return nil
			}
			if _, err = tx.Exec(ctx, "UPDATE santa_participants SET status=$3 WHERE season_id=$1 AND person_id=$2", v.ID, personID, status); err != nil {
				return err
			}
			err = s.notifyAdmins(ctx, tx, v, personID, "exit", msg)
		default:
			return ErrInvalid
		}
		if err != nil {
			return err
		}
		return bump(ctx, tx, v.ID)
	})
}
