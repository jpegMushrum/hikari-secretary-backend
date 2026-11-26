package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrRegistrationClosed = errors.New("event_unavailable")
var ErrRegistrationReminder = errors.New("invalid_reminder")

// RegisterTx is shared by both Telegram interfaces. A repeated submission
// preserves the original reminder. Callers hold a NO KEY UPDATE lock on the
// person while completing their profile. The following statement therefore
// sees all registrations committed before this person's turn, even for other events.
// The result indicates a first registration by a participant outside ITMO.
func RegisterTx(ctx context.Context, tx pgx.Tx, eventID, personID int64, offset *int) (bool, error) {
	var starts, now time.Time
	err := tx.QueryRow(ctx, `SELECT e.starts_at,NOW() FROM events e JOIN event_publication_references ref ON ref.event_id=e.id
 WHERE e.id=$1 AND ref.published AND e.status='active' AND e.starts_at>NOW() FOR SHARE OF e`, eventID).Scan(&starts, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrRegistrationClosed
	}
	if err != nil {
		return false, err
	}
	var already, firstOutsideITMO bool
	err = tx.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM event_registrations WHERE event_id=$1 AND person_id=$2 AND status='registered'),
		is_itmo_student IS FALSE AND NOT EXISTS (SELECT 1 FROM event_registrations WHERE person_id=$2)
		FROM people WHERE id=$2`, eventID, personID).Scan(&already, &firstOutsideITMO)
	if err != nil || already {
		return false, err
	}
	if offset != nil && (*offset <= 0 || starts.Sub(now).Minutes() <= float64(*offset)) {
		return false, ErrRegistrationReminder
	}
	_, err = tx.Exec(ctx, `INSERT INTO event_registrations(event_id,person_id,reminders_enabled,reminder_offset_minutes,status,registered_at)
 VALUES($1,$2,$3,$4,'registered',NOW())
 ON CONFLICT(event_id,person_id) DO UPDATE SET reminders_enabled=EXCLUDED.reminders_enabled,reminder_offset_minutes=EXCLUDED.reminder_offset_minutes,status='registered',registered_at=NOW(),cancelled_at=NULL,reminder_sent_at=NULL,attendance_prompt_sent_at=NULL,updated_at=NOW()
 WHERE event_registrations.status='cancelled'`, eventID, personID, offset != nil, offset)
	return firstOutsideITMO && err == nil, err
}
