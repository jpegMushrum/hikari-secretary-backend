package postgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/domain"
)

func editEntry(ctx context.Context, tx pgx.Tx, s domain.Season, person int64, c application.Command) error {
	if s.Phase != "submissions" {
		return domain.ErrConflict
	}
	if c.Action == "withdraw" {
		if _, err := tx.Exec(ctx, "UPDATE mascot_images SET entry_id=NULL,position=NULL,touched_at=now() WHERE entry_id=$1 AND person_id=$2 AND season_id=$3", c.EntryID, person, s.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "DELETE FROM mascot_entries WHERE id=$1 AND person_id=$2 AND season_id=$3", c.EntryID, person, s.ID)
		if err == nil && tag.RowsAffected() != 1 {
			return domain.ErrNotFound
		}
		return err
	}
	ok, err := eligible(ctx, tx, person)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrForbidden
	}
	name, description := strings.TrimSpace(c.Name), strings.TrimSpace(c.Description)
	if domain.ValidateConcept(name, description, c.Images) != nil || len(c.RequestID) > 80 {
		return domain.ErrInvalid
	}
	id := c.EntryID
	if id == 0 {
		var count int
		if c.RequestID != "" {
			var existingID int64
			err = tx.QueryRow(ctx, "SELECT id FROM mascot_entries WHERE season_id=$1 AND person_id=$2 AND request_id=$3", s.ID, person, c.RequestID).Scan(&existingID)
			if err == nil {
				found, err := entries(ctx, tx, s.ID, person, false, existingID)
				if err != nil {
					return err
				}
				if len(found) == 1 && found[0].Name == name && found[0].Description == description && slices.Equal(found[0].Images, c.Images) {
					return nil
				}
				return domain.ErrConflict
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		err = tx.QueryRow(ctx, "SELECT count(*) FROM mascot_entries WHERE season_id=$1 AND person_id=$2", s.ID, person).Scan(&count)
		if err != nil {
			return err
		}
		if count >= 2 {
			return domain.ErrConflict
		}
		err = tx.QueryRow(ctx, "INSERT INTO mascot_entries(season_id,person_id,name,description,request_id) VALUES($1,$2,$3,$4,nullif($5,'')) RETURNING id", s.ID, person, name, description, c.RequestID).Scan(&id)
		if err != nil {
			return err
		}
	} else {
		tag, err := tx.Exec(ctx, "UPDATE mascot_entries SET name=$4,description=$5 WHERE season_id=$1 AND person_id=$2 AND id=$3", s.ID, person, id, name, description)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return domain.ErrNotFound
		}
		if _, err = tx.Exec(ctx, "UPDATE mascot_images SET entry_id=NULL,position=NULL,touched_at=now() WHERE entry_id=$1", id); err != nil {
			return err
		}
	}
	for index, image := range c.Images {
		tag, err := tx.Exec(ctx, `UPDATE mascot_images SET entry_id=$1,position=$2,ai=$3 WHERE key=$4 AND entry_id IS NULL AND state='stored' AND season_id=$5 AND person_id=$6`, id, index, image.AI, image.Key, s.ID, person)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return domain.ErrInvalid
		}
	}
	return nil
}
