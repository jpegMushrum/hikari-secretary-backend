// Package legacyimport validates the one-time SQLite to PostgreSQL migration.
// It deliberately has no PostgreSQL dependency: DryRun opens the source SQLite
// database read-only and never changes either source or target data.
package legacyimport

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const expectedSQLiteSchemaVersion = 6

var requiredTables = []string{
	"schema_migrations",
	"user_profiles",
	"events",
	"registrations",
	"feedback_entries",
}

// Counts contains source totals and the equivalent projected PostgreSQL totals.
// It intentionally contains only aggregates, not personal data.
type Counts struct {
	Profiles                    int64 `json:"profiles"`
	Events                      int64 `json:"events"`
	Registrations               int64 `json:"registrations"`
	Feedback                    int64 `json:"feedback"`
	ProjectedPeople             int64 `json:"projected_people"`
	ProjectedTelegramIdentities int64 `json:"projected_telegram_identities"`
	ProjectedAttendances        int64 `json:"projected_attendances"`
}

// Issue explains why a source record cannot be safely imported. It never
// includes a name, username, message text or other personal data.
type Issue struct {
	Table    string `json:"table"`
	LegacyID string `json:"legacy_id,omitempty"`
	Field    string `json:"field,omitempty"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// Report is a non-mutating import preflight result.
type Report struct {
	SourceSQLitePath string  `json:"source_sqlite_path"`
	SourceVersion    int64   `json:"source_schema_version"`
	Valid            bool    `json:"valid"`
	Counts           Counts  `json:"counts"`
	Issues           []Issue `json:"issues"`
}

type validator struct {
	report        *Report
	people        map[int64]struct{}
	events        map[int64]struct{}
	registrations map[string]struct{}
}

// DryRun reads and validates the legacy SQLite data required by the first
// domain cutover. Validation errors are returned in Report; operational errors
// (an unreadable file or broken SQL connection) are returned as error.
func DryRun(ctx context.Context, sourceSQLitePath string) (Report, error) {
	absPath, err := filepath.Abs(sourceSQLitePath)
	if err != nil {
		return Report{}, fmt.Errorf("resolve SQLite path: %w", err)
	}

	report := Report{SourceSQLitePath: absPath, Issues: make([]Issue, 0)}
	database, err := openReadOnly(absPath)
	if err != nil {
		return report, err
	}
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return report, fmt.Errorf("open legacy SQLite database read-only: %w", err)
	}

	tables, err := existingTables(ctx, database)
	if err != nil {
		return report, err
	}
	for _, table := range requiredTables {
		if !tables[table] {
			addIssue(&report, table, "", "", "missing_table", "required legacy table is missing")
		}
	}
	if len(report.Issues) != 0 {
		return report, nil
	}

	version, err := sourceSchemaVersion(ctx, database)
	if err != nil {
		return report, err
	}
	report.SourceVersion = version
	if version < expectedSQLiteSchemaVersion {
		addIssue(&report, "schema_migrations", "", "version", "unsupported_schema", "legacy SQLite schema must be migrated to version 6 before import")
	}

	validator := validator{
		report:        &report,
		people:        make(map[int64]struct{}),
		events:        make(map[int64]struct{}),
		registrations: make(map[string]struct{}),
	}
	if err := validator.validateProfiles(ctx, database); err != nil {
		return report, err
	}
	if err := validator.validateEvents(ctx, database); err != nil {
		return report, err
	}
	if err := validator.validateRegistrations(ctx, database); err != nil {
		return report, err
	}
	if err := validator.validateFeedback(ctx, database); err != nil {
		return report, err
	}

	report.Counts.ProjectedPeople = int64(len(validator.people))
	report.Counts.ProjectedTelegramIdentities = int64(len(validator.people))
	report.Valid = len(report.Issues) == 0
	return report, nil
}

func openReadOnly(path string) (*sql.DB, error) {
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		// A Windows drive must be part of a file URI path, not its authority:
		// file:///C:/... rather than file://C:/...
		uriPath = "/" + uriPath
	}
	uri := &url.URL{Scheme: "file", Path: uriPath}
	query := url.Values{}
	query.Set("mode", "ro")
	uri.RawQuery = query.Encode()

	database, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, fmt.Errorf("prepare legacy SQLite connection: %w", err)
	}
	database.SetMaxOpenConns(1)
	return database, nil
}

func existingTables(ctx context.Context, database *sql.DB) (map[string]bool, error) {
	rows, err := database.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return nil, fmt.Errorf("list legacy SQLite tables: %w", err)
	}
	defer rows.Close()

	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read legacy table name: %w", err)
		}
		tables[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy SQLite tables: %w", err)
	}
	return tables, nil
}

func sourceSchemaVersion(ctx context.Context, database *sql.DB) (int64, error) {
	var version sql.NullInt64
	if err := database.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		return 0, fmt.Errorf("read legacy schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return version.Int64, nil
}

func (v *validator) validateProfiles(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `
		SELECT user_id, full_name, telegram_username, telegram_first_name,
			telegram_last_name, is_russian_citizen, is_itmo_student,
			feedback_enabled, created_at, updated_at
		FROM user_profiles
		ORDER BY user_id`)
	if err != nil {
		return fmt.Errorf("read legacy profiles: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			userID, citizen, itmo, feedbackEnabled  sql.NullInt64
			fullName, username, firstName, lastName sql.NullString
			createdAt, updatedAt                    sql.NullString
		)
		if err := rows.Scan(&userID, &fullName, &username, &firstName, &lastName, &citizen, &itmo, &feedbackEnabled, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("scan legacy profile: %w", err)
		}
		reportCount(&v.report.Counts.Profiles)
		legacyID := nullableID(userID)
		if !v.validPositiveID("user_profiles", legacyID, "user_id", userID) {
			continue
		}
		v.people[userID.Int64] = struct{}{}
		v.validateFullName("user_profiles", legacyID, fullName)
		v.validateUsername("user_profiles", legacyID, username)
		v.validateDisplayName("user_profiles", legacyID, firstName, lastName, userID.Int64)
		v.validateNullableBool("user_profiles", legacyID, "is_russian_citizen", citizen)
		v.validateNullableBool("user_profiles", legacyID, "is_itmo_student", itmo)
		v.validateRequiredBool("user_profiles", legacyID, "feedback_enabled", feedbackEnabled)
		v.requiredTime("user_profiles", legacyID, "created_at", createdAt)
		v.requiredTime("user_profiles", legacyID, "updated_at", updatedAt)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy profiles: %w", err)
	}
	return nil
}

func (v *validator) validateEvents(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `
		SELECT post_id, title, starts_at, ends_at, status, cancelled_at, created_at
		FROM events
		ORDER BY post_id`)
	if err != nil {
		return fmt.Errorf("read legacy events: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			postID                                                  sql.NullInt64
			title, startsAt, endsAt, status, cancelledAt, createdAt sql.NullString
		)
		if err := rows.Scan(&postID, &title, &startsAt, &endsAt, &status, &cancelledAt, &createdAt); err != nil {
			return fmt.Errorf("scan legacy event: %w", err)
		}
		reportCount(&v.report.Counts.Events)
		legacyID := nullableID(postID)
		if !v.validPositiveID("events", legacyID, "post_id", postID) {
			continue
		}
		v.events[postID.Int64] = struct{}{}
		v.validateTitle("events", legacyID, title)
		start, validStart := v.requiredTime("events", legacyID, "starts_at", startsAt)
		end, validEnd := v.requiredTime("events", legacyID, "ends_at", endsAt)
		if validStart && validEnd && !start.Before(end) {
			v.issue("events", legacyID, "ends_at", "invalid_time_range", "event end must be after start")
		}
		v.validateEventStatus(legacyID, status, cancelledAt)
		v.requiredTime("events", legacyID, "created_at", createdAt)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy events: %w", err)
	}
	return nil
}

func (v *validator) validateRegistrations(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `
		SELECT id, post_id, user_id, reminders_enabled, status, registered_at,
			cancelled_at, reminder_offset_minutes, reminder_sent_at,
			attendance_prompt_sent_at, attended, attendance_recorded_at
		FROM registrations
		ORDER BY id`)
	if err != nil {
		return fmt.Errorf("read legacy registrations: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, postID, userID, remindersEnabled, reminderOffset, attended sql.NullInt64
			status, registeredAt, cancelledAt, reminderSentAt              sql.NullString
			attendancePromptAt, attendanceRecordedAt                       sql.NullString
		)
		if err := rows.Scan(&id, &postID, &userID, &remindersEnabled, &status, &registeredAt, &cancelledAt, &reminderOffset, &reminderSentAt, &attendancePromptAt, &attended, &attendanceRecordedAt); err != nil {
			return fmt.Errorf("scan legacy registration: %w", err)
		}
		reportCount(&v.report.Counts.Registrations)
		legacyID := nullableID(id)
		if !v.validPositiveID("registrations", legacyID, "id", id) {
			continue
		}
		if v.validPositiveID("registrations", legacyID, "post_id", postID) {
			if _, exists := v.events[postID.Int64]; !exists {
				v.issue("registrations", legacyID, "post_id", "missing_event", "registration refers to an event absent from the import set")
			}
		}
		if v.validPositiveID("registrations", legacyID, "user_id", userID) {
			v.people[userID.Int64] = struct{}{}
		}
		v.validateRegistrationStatus(legacyID, status, cancelledAt)
		v.requiredTime("registrations", legacyID, "registered_at", registeredAt)
		v.validateRequiredBool("registrations", legacyID, "reminders_enabled", remindersEnabled)
		if reminderOffset.Valid && reminderOffset.Int64 <= 0 {
			v.issue("registrations", legacyID, "reminder_offset_minutes", "invalid_offset", "reminder offset must be positive when present")
		}
		v.optionalTime("registrations", legacyID, "reminder_sent_at", reminderSentAt)
		promptedAt, promptValid := v.optionalTime("registrations", legacyID, "attendance_prompt_sent_at", attendancePromptAt)
		if attended.Valid {
			if attended.Int64 != 0 && attended.Int64 != 1 {
				v.issue("registrations", legacyID, "attended", "invalid_boolean", "boolean value must be 0 or 1")
			} else {
				recordedAt, recordedValid := v.requiredTime("registrations", legacyID, "attendance_recorded_at", attendanceRecordedAt)
				if promptValid && recordedValid && recordedAt.Before(promptedAt) {
					v.issue("registrations", legacyID, "attendance_recorded_at", "invalid_time_order", "attendance answer cannot precede the prompt")
				}
				if recordedValid {
					reportCount(&v.report.Counts.ProjectedAttendances)
				}
			}
		} else if attendanceRecordedAt.Valid && strings.TrimSpace(attendanceRecordedAt.String) != "" {
			v.issue("registrations", legacyID, "attendance_recorded_at", "orphaned_timestamp", "attendance timestamp exists without an attendance answer")
		}

		if postID.Valid && userID.Valid {
			key := strconv.FormatInt(postID.Int64, 10) + ":" + strconv.FormatInt(userID.Int64, 10)
			if _, exists := v.registrations[key]; exists {
				v.issue("registrations", legacyID, "post_id,user_id", "duplicate_registration", "duplicate event and person pair")
			}
			v.registrations[key] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy registrations: %w", err)
	}
	return nil
}

func (v *validator) validateFeedback(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `
		SELECT id, user_id, post_id, text, created_at
		FROM feedback_entries
		ORDER BY id`)
	if err != nil {
		return fmt.Errorf("read legacy feedback: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, userID, postID sql.NullInt64
		var text, createdAt sql.NullString
		if err := rows.Scan(&id, &userID, &postID, &text, &createdAt); err != nil {
			return fmt.Errorf("scan legacy feedback: %w", err)
		}
		reportCount(&v.report.Counts.Feedback)
		legacyID := nullableID(id)
		if !v.validPositiveID("feedback_entries", legacyID, "id", id) {
			continue
		}
		if v.validPositiveID("feedback_entries", legacyID, "user_id", userID) {
			v.people[userID.Int64] = struct{}{}
		}
		if postID.Valid {
			if postID.Int64 <= 0 {
				v.issue("feedback_entries", legacyID, "post_id", "invalid_id", "post ID must be positive when present")
			} else if _, exists := v.events[postID.Int64]; !exists {
				v.issue("feedback_entries", legacyID, "post_id", "missing_event", "feedback refers to an event absent from the import set")
			}
		}
		if !text.Valid || utf8.RuneCountInString(strings.TrimSpace(text.String)) == 0 || utf8.RuneCountInString(text.String) > 4000 {
			v.issue("feedback_entries", legacyID, "text", "invalid_text", "feedback text must contain 1 to 4000 characters")
		}
		v.requiredTime("feedback_entries", legacyID, "created_at", createdAt)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy feedback: %w", err)
	}
	return nil
}

func (v *validator) validPositiveID(table, legacyID, field string, value sql.NullInt64) bool {
	if !value.Valid || value.Int64 <= 0 {
		v.issue(table, legacyID, field, "invalid_id", "ID must be a positive integer")
		return false
	}
	return true
}

func (v *validator) validateFullName(table, legacyID string, value sql.NullString) {
	if !value.Valid || utf8.RuneCountInString(strings.TrimSpace(value.String)) == 0 || utf8.RuneCountInString(strings.TrimSpace(value.String)) > 200 {
		v.issue(table, legacyID, "full_name", "invalid_name", "full name must contain 1 to 200 characters")
	}
}

func (v *validator) validateUsername(table, legacyID string, value sql.NullString) {
	username := strings.TrimPrefix(strings.TrimSpace(value.String), "@")
	if utf8.RuneCountInString(username) > 32 {
		v.issue(table, legacyID, "telegram_username", "invalid_username", "Telegram username must contain at most 32 characters")
	}
}

func (v *validator) validateDisplayName(table, legacyID string, firstName, lastName sql.NullString, userID int64) {
	displayName := strings.TrimSpace(strings.TrimSpace(firstName.String) + " " + strings.TrimSpace(lastName.String))
	if displayName == "" {
		displayName = "Пользователь #" + strconv.FormatInt(userID, 10)
	}
	if utf8.RuneCountInString(displayName) > 256 {
		v.issue(table, legacyID, "telegram_first_name,telegram_last_name", "invalid_display_name", "display name must contain at most 256 characters")
	}
}

func (v *validator) validateNullableBool(table, legacyID, field string, value sql.NullInt64) {
	if value.Valid && value.Int64 != 0 && value.Int64 != 1 {
		v.issue(table, legacyID, field, "invalid_boolean", "boolean value must be 0 or 1 when present")
	}
}

func (v *validator) validateRequiredBool(table, legacyID, field string, value sql.NullInt64) {
	if !value.Valid || (value.Int64 != 0 && value.Int64 != 1) {
		v.issue(table, legacyID, field, "invalid_boolean", "boolean value must be 0 or 1")
	}
}

func (v *validator) validateTitle(table, legacyID string, value sql.NullString) {
	if !value.Valid || utf8.RuneCountInString(strings.TrimSpace(value.String)) == 0 || utf8.RuneCountInString(strings.TrimSpace(value.String)) > 160 {
		v.issue(table, legacyID, "title", "invalid_title", "event title must contain 1 to 160 characters")
	}
}

func (v *validator) validateEventStatus(legacyID string, status, cancelledAt sql.NullString) {
	value := strings.TrimSpace(status.String)
	if value != "active" && value != "cancelled" && value != "completed" {
		v.issue("events", legacyID, "status", "invalid_status", "event status must be active, cancelled or completed")
		return
	}
	_, cancelledAtValid := v.optionalTime("events", legacyID, "cancelled_at", cancelledAt)
	if value == "cancelled" && !cancelledAtValid {
		v.issue("events", legacyID, "cancelled_at", "missing_timestamp", "cancelled event must have a cancellation timestamp")
	}
	if value != "cancelled" && cancelledAt.Valid && strings.TrimSpace(cancelledAt.String) != "" {
		v.issue("events", legacyID, "cancelled_at", "unexpected_timestamp", "only cancelled event may have a cancellation timestamp")
	}
}

func (v *validator) validateRegistrationStatus(legacyID string, status, cancelledAt sql.NullString) {
	value := strings.TrimSpace(status.String)
	if value != "registered" && value != "cancelled" {
		v.issue("registrations", legacyID, "status", "invalid_status", "registration status must be registered or cancelled")
		return
	}
	_, cancelledAtValid := v.optionalTime("registrations", legacyID, "cancelled_at", cancelledAt)
	if value == "cancelled" && !cancelledAtValid {
		v.issue("registrations", legacyID, "cancelled_at", "missing_timestamp", "cancelled registration must have a cancellation timestamp")
	}
	if value == "registered" && cancelledAt.Valid && strings.TrimSpace(cancelledAt.String) != "" {
		v.issue("registrations", legacyID, "cancelled_at", "unexpected_timestamp", "registered item must not have a cancellation timestamp")
	}
}

func (v *validator) requiredTime(table, legacyID, field string, value sql.NullString) (time.Time, bool) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		v.issue(table, legacyID, field, "missing_timestamp", "timestamp is required")
		return time.Time{}, false
	}
	parsed, err := parseLegacyTime(value.String)
	if err != nil {
		v.issue(table, legacyID, field, "invalid_timestamp", "timestamp is not a supported UTC value")
		return time.Time{}, false
	}
	return parsed, true
}

func (v *validator) optionalTime(table, legacyID, field string, value sql.NullString) (time.Time, bool) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}, false
	}
	parsed, err := parseLegacyTime(value.String)
	if err != nil {
		v.issue(table, legacyID, field, "invalid_timestamp", "timestamp is not a supported UTC value")
		return time.Time{}, false
	}
	return parsed, true
}

func (v *validator) issue(table, legacyID, field, code, message string) {
	addIssue(v.report, table, legacyID, field, code, message)
}

func addIssue(report *Report, table, legacyID, field, code, message string) {
	report.Issues = append(report.Issues, Issue{
		Table: table, LegacyID: legacyID, Field: field, Code: code, Message: message,
	})
}

func nullableID(value sql.NullInt64) string {
	if !value.Valid {
		return ""
	}
	return strconv.FormatInt(value.Int64, 10)
}

func reportCount(counter *int64) {
	*counter = *counter + 1
}

func parseLegacyTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp")
}
