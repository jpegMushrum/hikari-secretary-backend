package legacyimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type legacyProfile struct {
	userID, citizen, itmo, feedbackEnabled  sql.NullInt64
	fullName, username, firstName, lastName sql.NullString
	createdAt, updatedAt                    sql.NullString
}

type legacyEvent struct {
	postID                          sql.NullInt64
	title, startsAt, endsAt, status sql.NullString
	cancelledAt, createdAt          sql.NullString
	published                       bool
}

type legacyRegistration struct {
	id, postID, userID, remindersEnabled sql.NullInt64
	status, registeredAt, cancelledAt    sql.NullString
	reminderOffset, reminderSentAt       sql.NullInt64
	reminderSentAtText                   sql.NullString
	attendancePromptAt, attendedAt       sql.NullString
	attended, attendanceRecordedAt       sql.NullInt64
	attendanceRecordedAtText             sql.NullString
}

type legacyFeedback struct {
	id, userID, postID sql.NullInt64
	text, createdAt    sql.NullString
}

type legacySnapshot struct {
	profiles      map[int64]legacyProfile
	events        map[int64]legacyEvent
	registrations []legacyRegistration
	feedback      []legacyFeedback
	people        map[int64]struct{}
}

// Apply validates the source again, then imports the first domain slice in a
// single PostgreSQL transaction. legacy_import_map makes a rerun idempotent.
// It never deletes source data and should only be called during the cutover
// window while the bot is stopped.
func Apply(ctx context.Context, sourceSQLitePath, databaseURL string) (Report, error) {
	report, err := DryRun(ctx, sourceSQLitePath)
	if err != nil {
		return report, err
	}
	if !report.Valid {
		return report, fmt.Errorf("dry-run validation failed with %d issue(s)", len(report.Issues))
	}
	if strings.TrimSpace(databaseURL) == "" {
		return report, fmt.Errorf("DATABASE_URL is required for apply")
	}

	source, err := openReadOnly(report.SourceSQLitePath)
	if err != nil {
		return report, err
	}
	defer source.Close()
	snapshot, err := loadSnapshot(ctx, source)
	if err != nil {
		return report, err
	}

	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return report, fmt.Errorf("connect PostgreSQL for import: %w", err)
	}
	defer connection.Close(ctx)
	transaction, err := connection.Begin(ctx)
	if err != nil {
		return report, fmt.Errorf("begin PostgreSQL import: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	people, err := importPeople(ctx, transaction, snapshot)
	if err == nil {
		err = importEvents(ctx, transaction, snapshot, people)
	}
	if err == nil {
		err = importRegistrations(ctx, transaction, snapshot, people)
	}
	if err == nil {
		err = importFeedback(ctx, transaction, snapshot, people)
	}
	if err != nil {
		return report, fmt.Errorf("apply legacy import: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return report, fmt.Errorf("commit legacy import: %w", err)
	}
	report.Applied = true
	return report, nil
}

func loadSnapshot(ctx context.Context, source *sql.DB) (legacySnapshot, error) {
	snapshot := legacySnapshot{
		profiles: make(map[int64]legacyProfile),
		events:   make(map[int64]legacyEvent),
		people:   make(map[int64]struct{}),
	}
	rows, err := source.QueryContext(ctx, `SELECT user_id,full_name,telegram_username,telegram_first_name,telegram_last_name,is_russian_citizen,is_itmo_student,feedback_enabled,created_at,updated_at FROM user_profiles ORDER BY user_id`)
	if err != nil {
		return snapshot, fmt.Errorf("read profiles for import: %w", err)
	}
	for rows.Next() {
		var profile legacyProfile
		if err := rows.Scan(&profile.userID, &profile.fullName, &profile.username, &profile.firstName, &profile.lastName, &profile.citizen, &profile.itmo, &profile.feedbackEnabled, &profile.createdAt, &profile.updatedAt); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("scan profile for import: %w", err)
		}
		if profile.userID.Valid {
			snapshot.profiles[profile.userID.Int64] = profile
			snapshot.people[profile.userID.Int64] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, fmt.Errorf("iterate profiles for import: %w", err)
	}
	rows.Close()

	rows, err = source.QueryContext(ctx, `SELECT e.post_id,e.title,e.starts_at,e.ends_at,e.status,e.cancelled_at,e.created_at,EXISTS(SELECT 1 FROM deliveries d WHERE d.post_id=e.post_id AND d.status='sent') FROM events e ORDER BY e.post_id`)
	if err != nil {
		return snapshot, fmt.Errorf("read events for import: %w", err)
	}
	for rows.Next() {
		var event legacyEvent
		if err := rows.Scan(&event.postID, &event.title, &event.startsAt, &event.endsAt, &event.status, &event.cancelledAt, &event.createdAt, &event.published); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("scan event for import: %w", err)
		}
		if event.postID.Valid {
			snapshot.events[event.postID.Int64] = event
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, fmt.Errorf("iterate events for import: %w", err)
	}
	rows.Close()

	rows, err = source.QueryContext(ctx, `SELECT id,post_id,user_id,reminders_enabled,status,registered_at,cancelled_at,reminder_offset_minutes,reminder_sent_at,attendance_prompt_sent_at,attended,attendance_recorded_at FROM registrations ORDER BY id`)
	if err != nil {
		return snapshot, fmt.Errorf("read registrations for import: %w", err)
	}
	for rows.Next() {
		var registration legacyRegistration
		if err := rows.Scan(&registration.id, &registration.postID, &registration.userID, &registration.remindersEnabled, &registration.status, &registration.registeredAt, &registration.cancelledAt, &registration.reminderOffset, &registration.reminderSentAtText, &registration.attendancePromptAt, &registration.attended, &registration.attendanceRecordedAtText); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("scan registration for import: %w", err)
		}
		if registration.userID.Valid {
			snapshot.people[registration.userID.Int64] = struct{}{}
		}
		snapshot.registrations = append(snapshot.registrations, registration)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, fmt.Errorf("iterate registrations for import: %w", err)
	}
	rows.Close()

	rows, err = source.QueryContext(ctx, `SELECT id,user_id,post_id,text,created_at FROM feedback_entries ORDER BY id`)
	if err != nil {
		return snapshot, fmt.Errorf("read feedback for import: %w", err)
	}
	for rows.Next() {
		var feedback legacyFeedback
		if err := rows.Scan(&feedback.id, &feedback.userID, &feedback.postID, &feedback.text, &feedback.createdAt); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("scan feedback for import: %w", err)
		}
		if feedback.userID.Valid {
			snapshot.people[feedback.userID.Int64] = struct{}{}
		}
		snapshot.feedback = append(snapshot.feedback, feedback)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, fmt.Errorf("iterate feedback for import: %w", err)
	}
	rows.Close()
	return snapshot, nil
}

func importPeople(ctx context.Context, tx pgx.Tx, snapshot legacySnapshot) (map[int64]int64, error) {
	ids := make([]int64, 0, len(snapshot.people))
	for userID := range snapshot.people {
		ids = append(ids, userID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := make(map[int64]int64, len(ids))
	for _, userID := range ids {
		profile, hasProfile := snapshot.profiles[userID]
		personID, err := legacyMap(ctx, tx, "user_profile", strconv.FormatInt(userID, 10))
		if err != nil {
			return nil, err
		}
		if personID == 0 {
			err = tx.QueryRow(ctx, "SELECT person_id FROM telegram_identities WHERE telegram_user_id=$1", userID).Scan(&personID)
			if errors.Is(err, pgx.ErrNoRows) {
				if err := tx.QueryRow(ctx, "INSERT INTO people DEFAULT VALUES RETURNING id").Scan(&personID); err != nil {
					return nil, err
				}
			} else if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO legacy_import_map(source_kind,source_id,target_kind,target_id) VALUES('user_profile',$1,'person',$2)`, strconv.FormatInt(userID, 10), personID); err != nil {
				return nil, err
			}
		}
		result[userID] = personID
		fullName, citizen, itmo, feedbackEnabled := any(nil), any(nil), any(nil), true
		createdAt, updatedAt := time.Now().UTC(), time.Now().UTC()
		username, displayName := "", "Пользователь #"+strconv.FormatInt(userID, 10)
		if hasProfile {
			if profile.fullName.Valid {
				fullName = strings.TrimSpace(profile.fullName.String)
			}
			if profile.citizen.Valid {
				citizen = profile.citizen.Int64 == 1
			}
			if profile.itmo.Valid {
				itmo = profile.itmo.Int64 == 1
			}
			if profile.feedbackEnabled.Valid {
				feedbackEnabled = profile.feedbackEnabled.Int64 == 1
			}
			username = strings.TrimPrefix(strings.TrimSpace(profile.username.String), "@")
			displayName = strings.TrimSpace(strings.TrimSpace(profile.firstName.String) + " " + strings.TrimSpace(profile.lastName.String))
			if displayName == "" {
				displayName = "Пользователь #" + strconv.FormatInt(userID, 10)
			}
			if parsed, err := parseLegacyTime(profile.createdAt.String); err == nil {
				createdAt = parsed
			}
			if parsed, err := parseLegacyTime(profile.updatedAt.String); err == nil {
				updatedAt = parsed
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE people SET full_name=$2,is_russian_citizen=$3,is_itmo_student=$4,feedback_offers_enabled=$5,created_at=$6,updated_at=$7 WHERE id=$1`, personID, fullName, citizen, itmo, feedbackEnabled, createdAt, updatedAt); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO telegram_identities(person_id,telegram_user_id,username,display_name,last_seen_at) VALUES($1,$2,NULLIF($3,''),$4,$5) ON CONFLICT(telegram_user_id) DO UPDATE SET person_id=EXCLUDED.person_id,username=EXCLUDED.username,display_name=EXCLUDED.display_name,last_seen_at=EXCLUDED.last_seen_at,updated_at=NOW()`, personID, userID, username, displayName, updatedAt); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func importEvents(ctx context.Context, tx pgx.Tx, snapshot legacySnapshot, people map[int64]int64) error {
	for postID, event := range snapshot.events {
		start, _ := parseLegacyTime(event.startsAt.String)
		end, _ := parseLegacyTime(event.endsAt.String)
		created, _ := parseLegacyTime(event.createdAt.String)
		if created.IsZero() {
			created = time.Now().UTC()
		}
		targetID, err := legacyMap(ctx, tx, "event", strconv.FormatInt(postID, 10))
		if err != nil {
			return err
		}
		if targetID == 0 {
			err = tx.QueryRow(ctx, `SELECT event_id FROM event_publication_references WHERE provider='legacy_sqlite' AND external_publication_id=$1`, strconv.FormatInt(postID, 10)).Scan(&targetID)
			if errors.Is(err, pgx.ErrNoRows) {
				if err := tx.QueryRow(ctx, `INSERT INTO events(title,starts_at,ends_at,status,cancelled_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$6) RETURNING id`, strings.TrimSpace(event.title.String), start, end, strings.TrimSpace(event.status.String), nullableTime(event.cancelledAt), created).Scan(&targetID); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO event_publication_references(event_id,provider,external_publication_id,published) VALUES($1,'legacy_sqlite',$2,$3) ON CONFLICT(provider,external_publication_id) DO UPDATE SET event_id=EXCLUDED.event_id,published=EXCLUDED.published`, targetID, strconv.FormatInt(postID, 10), event.published); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO legacy_import_map(source_kind,source_id,target_kind,target_id) VALUES('event',$1,'event',$2)`, strconv.FormatInt(postID, 10), targetID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE events SET title=$2,starts_at=$3,ends_at=$4,status=$5,cancelled_at=$6,created_at=$7,updated_at=$7 WHERE id=$1`, targetID, strings.TrimSpace(event.title.String), start, end, strings.TrimSpace(event.status.String), nullableTime(event.cancelledAt), created); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE event_publication_references SET published=$2 WHERE event_id=$1 AND provider='legacy_sqlite'`, targetID, event.published); err != nil {
			return err
		}
	}
	return nil
}

func importRegistrations(ctx context.Context, tx pgx.Tx, snapshot legacySnapshot, people map[int64]int64) error {
	for _, registration := range snapshot.registrations {
		postID, userID := registration.postID.Int64, registration.userID.Int64
		eventID, err := legacyMap(ctx, tx, "event", strconv.FormatInt(postID, 10))
		if err != nil {
			return err
		}
		personID := people[userID]
		registeredAt, _ := parseLegacyTime(registration.registeredAt.String)
		cancelledAt := nullableTime(registration.cancelledAt)
		var targetID int64
		if existing, err := legacyMap(ctx, tx, "registration", strconv.FormatInt(registration.id.Int64, 10)); err != nil {
			return err
		} else {
			targetID = existing
		}
		if err := tx.QueryRow(ctx, `INSERT INTO event_registrations(event_id,person_id,status,registered_at,cancelled_at,reminders_enabled,reminder_offset_minutes,reminder_sent_at,attendance_prompt_sent_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$4,$4) ON CONFLICT(event_id,person_id) DO UPDATE SET status=EXCLUDED.status,registered_at=EXCLUDED.registered_at,cancelled_at=EXCLUDED.cancelled_at,reminders_enabled=EXCLUDED.reminders_enabled,reminder_offset_minutes=EXCLUDED.reminder_offset_minutes,reminder_sent_at=EXCLUDED.reminder_sent_at,attendance_prompt_sent_at=EXCLUDED.attendance_prompt_sent_at,updated_at=EXCLUDED.updated_at RETURNING id`, eventID, personID, registration.status.String, registeredAt, cancelledAt, registration.remindersEnabled.Int64 == 1, nullableInt(registration.reminderOffset), nullableTime(registration.reminderSentAtText), nullableTime(registration.attendancePromptAt)).Scan(&targetID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO legacy_import_map(source_kind,source_id,target_kind,target_id) VALUES('registration',$1,'registration',$2) ON CONFLICT(source_kind,source_id) DO UPDATE SET target_id=EXCLUDED.target_id`, strconv.FormatInt(registration.id.Int64, 10), targetID); err != nil {
			return err
		}
		if registration.attended.Valid {
			recordedAt := nullableTime(registration.attendanceRecordedAtText)
			if recordedAt == nil {
				recordedAt = &registeredAt
			}
			if _, err := tx.Exec(ctx, `INSERT INTO attendance(registration_id,prompted_at,attended,recorded_at) VALUES($1,$2,$3,$4) ON CONFLICT(registration_id) DO UPDATE SET prompted_at=EXCLUDED.prompted_at,attended=EXCLUDED.attended,recorded_at=EXCLUDED.recorded_at`, targetID, nullableTime(registration.attendancePromptAt), registration.attended.Int64 == 1, recordedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func importFeedback(ctx context.Context, tx pgx.Tx, snapshot legacySnapshot, people map[int64]int64) error {
	for _, feedback := range snapshot.feedback {
		personID := people[feedback.userID.Int64]
		var eventID *int64
		if feedback.postID.Valid {
			value, err := legacyMap(ctx, tx, "event", strconv.FormatInt(feedback.postID.Int64, 10))
			if err != nil {
				return err
			}
			if value > 0 {
				eventID = &value
			}
		}
		createdAt, _ := parseLegacyTime(feedback.createdAt.String)
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		targetID, err := legacyMap(ctx, tx, "feedback", strconv.FormatInt(feedback.id.Int64, 10))
		if err != nil {
			return err
		}
		if targetID == 0 {
			if err := tx.QueryRow(ctx, `INSERT INTO feedback(person_id,event_id,text,created_at) VALUES($1,$2,$3,$4) RETURNING id`, personID, eventID, feedback.text.String, createdAt).Scan(&targetID); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE feedback SET person_id=$2,event_id=$3,text=$4,created_at=$5 WHERE id=$1`, targetID, personID, eventID, feedback.text.String, createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO legacy_import_map(source_kind,source_id,target_kind,target_id) VALUES('feedback',$1,'feedback',$2) ON CONFLICT(source_kind,source_id) DO UPDATE SET target_id=EXCLUDED.target_id`, strconv.FormatInt(feedback.id.Int64, 10), targetID); err != nil {
			return err
		}
	}
	return nil
}

func legacyMap(ctx context.Context, tx pgx.Tx, sourceKind, sourceID string) (int64, error) {
	var targetID int64
	err := tx.QueryRow(ctx, "SELECT target_id FROM legacy_import_map WHERE source_kind=$1 AND source_id=$2", sourceKind, sourceID).Scan(&targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return targetID, err
}
func nullableTime(value sql.NullString) *time.Time {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	parsed, err := parseLegacyTime(value.String)
	if err != nil {
		return nil
	}
	return &parsed
}
func nullableInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int64)
	return &result
}
