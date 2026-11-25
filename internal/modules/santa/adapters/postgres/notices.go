package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/santa/domain"
)

// NoticeDispatchableSQL applies to an unaliased outbox row. A reminder may
// have waited for a retry while the admin moved the exchange, paused the
// season, or removed its recipient. Recheck eligibility when claiming it.
const NoticeDispatchableSQL = `outbox.topic <> 'santa.notice' OR
    EXISTS (SELECT 1 FROM santa_seasons active WHERE active.id=outbox.aggregate_id AND active.status<>'closed') AND
    (COALESCE(outbox.payload->>'kind', split_part(outbox.idempotency_key, ':', 4)) <> 'reminder' OR EXISTS (
        SELECT 1 FROM santa_seasons s JOIN santa_participants p ON p.season_id=s.id
        JOIN telegram_identities t ON t.person_id=p.person_id
        WHERE s.id=outbox.aggregate_id AND s.status='open' AND s.confirmed_revision IS NOT NULL
          AND p.status<>'excluded' AND t.telegram_user_id=(outbox.payload->>'telegram_user_id')::bigint
          AND now()>=s.exchange_at-make_interval(mins=>s.reminder_minutes) AND now()<s.exchange_at
    ))`

func notice(ctx context.Context, tx pgx.Tx, v domain.Season, tgID int64, kind, msg, path, profileURL string) error {
	fields := map[string]any{"telegram_user_id": tgID, "text": msg, "path": path, "kind": kind}
	if profileURL != "" {
		fields["profile_url"] = profileURL
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("santa:%d:%d:%s:%d", v.ID, v.Revision, kind, tgID)
	_, err = tx.Exec(ctx, `INSERT INTO outbox(topic,aggregate_type,aggregate_id,payload,idempotency_key)
       VALUES ('santa.notice','santa_season',$1,$2,$3) ON CONFLICT(idempotency_key) DO NOTHING`, v.ID, payload, key)
	return err
}

func personNotice(ctx context.Context, tx pgx.Tx, v domain.Season, personID int64, kind, msg string) error {
	var tgID int64
	if err := tx.QueryRow(ctx, "SELECT telegram_user_id FROM telegram_identities WHERE person_id=$1", personID).Scan(&tgID); err != nil {
		return err
	}
	return notice(ctx, tx, v, tgID, kind, msg, "/santa", "")
}

func (s *Repository) notifyAdmins(ctx context.Context, tx pgx.Tx, v domain.Season, who int64, kind, action string) error {
	var name, profileURL string
	err := tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(p.full_name,''),t.display_name),
        CASE WHEN t.username COLLATE "C" ~ '^[A-Za-z][A-Za-z0-9_]{0,31}$'
        THEN 'https://t.me/' || t.username ELSE 'tg://user?id=' || t.telegram_user_id END
        FROM people p JOIN telegram_identities t ON t.person_id=p.id WHERE p.id=$1`, who).Scan(&name, &profileURL)
	if err != nil {
		return err
	}
	for _, admin := range s.admins {
		if err := notice(ctx, tx, v, admin, kind, "Тайный Санта. "+name+": "+action, "/admin/santa", profileURL); err != nil {
			return err
		}
	}
	return nil
}

// EnqueueReminders can run on every scheduler tick. The season row serializes
// enrollment and date changes with selecting the recipients of a due reminder.
func (s *Repository) EnqueueReminders(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		v, err := readSeason(tx.QueryRow(ctx, "SELECT "+seasonColumns+` FROM santa_seasons
            WHERE status='open' AND confirmed_revision IS NOT NULL
            AND now()>=exchange_at-make_interval(mins=>reminder_minutes) AND now()<exchange_at
            AND EXISTS(SELECT 1 FROM santa_participants WHERE season_id=santa_seasons.id AND status<>'excluded' AND NOT reminder_sent)
            FOR UPDATE SKIP LOCKED`))
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `UPDATE santa_participants SET reminder_sent=true WHERE season_id=$1 AND status<>'excluded' AND NOT reminder_sent RETURNING person_id, attendance, gift`, v.ID)
		if err != nil {
			return err
		}
		type reply struct {
			personID         int64
			attendance, gift string
		}
		replies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (reply, error) {
			var r reply
			err := row.Scan(&r.personID, &r.attendance, &r.gift)
			return r, err
		})
		if err != nil {
			return err
		}
		attendance := map[string]string{"unknown": "ответа пока нет", "yes": "приду", "no": "не смогу прийти"}
		gift := map[string]string{"unknown": "не подготовил", "ready": "подарок готов", "ordered": "подарок заказан (в пути)", "not_ready": "нет"}
		for _, r := range replies {
			msg := fmt.Sprintf("Тайный Санта. Ответы на момент подготовки напоминания:\nПридёшь на обмен? — %s.\nПодарок готов? — %s.\nОткрой карточку, чтобы проверить или изменить ответы.", attendance[r.attendance], gift[r.gift])
			if err := personNotice(ctx, tx, v, r.personID, "reminder", msg); err != nil {
				return err
			}
		}
		return nil
	})
}
