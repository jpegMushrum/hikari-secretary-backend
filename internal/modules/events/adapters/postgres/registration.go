package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

var ErrRegistrationClosed = errors.New("event_unavailable")
var ErrRegistrationReminder = errors.New("invalid_reminder")

// RegisterTx is shared by both Telegram interfaces. A repeated submission
// preserves the original reminder. Re-registering clears the previous cancellation.
func RegisterTx(ctx context.Context, tx pgx.Tx, eventID, personID int64, offset *int) error {
	var starts, now time.Time
	err := tx.QueryRow(ctx, `SELECT e.starts_at,NOW() FROM events e JOIN event_publication_references ref ON ref.event_id=e.id
 WHERE e.id=$1 AND ref.published AND e.status='active' AND e.starts_at>NOW() FOR SHARE OF e`, eventID).Scan(&starts, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRegistrationClosed
	}
	if err != nil {
		return err
	}
	var already bool
	err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM event_registrations WHERE event_id=$1 AND person_id=$2 AND status='registered')", eventID, personID).Scan(&already)
	if err != nil || already {
		return err
	}
	if offset != nil && (*offset <= 0 || starts.Sub(now).Minutes() <= float64(*offset)) {
		return ErrRegistrationReminder
	}
	_, err = tx.Exec(ctx, `INSERT INTO event_registrations(event_id,person_id,reminders_enabled,reminder_offset_minutes,status,registered_at)
 VALUES($1,$2,$3,$4,'registered',NOW())
 ON CONFLICT(event_id,person_id) DO UPDATE SET reminders_enabled=EXCLUDED.reminders_enabled,reminder_offset_minutes=EXCLUDED.reminder_offset_minutes,status='registered',registered_at=NOW(),cancelled_at=NULL,reminder_sent_at=NULL,attendance_prompt_sent_at=NULL,updated_at=NOW()
 WHERE event_registrations.status='cancelled'`, eventID, personID, offset != nil, offset)
	return err
}
