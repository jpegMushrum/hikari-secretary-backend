package legacyimport

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestDryRunValidatesCompleteSourceWithoutChangingIt(t *testing.T) {
	source := createLegacySQLite(t, false)

	report, err := DryRun(context.Background(), source)
	if err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	if !report.Valid {
		t.Fatalf("report is invalid: %#v", report.Issues)
	}
	if report.SourceVersion != expectedSQLiteSchemaVersion {
		t.Errorf("source version = %d, want %d", report.SourceVersion, expectedSQLiteSchemaVersion)
	}
	if report.Counts != (Counts{
		Profiles:                    1,
		Events:                      1,
		Registrations:               1,
		Feedback:                    1,
		ProjectedPeople:             1,
		ProjectedTelegramIdentities: 1,
		ProjectedAttendances:        1,
	}) {
		t.Errorf("counts = %#v", report.Counts)
	}

	database, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM user_profiles").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("source profile count = %d, want 1", count)
	}
}

func TestDryRunReportsBrokenEventReferenceWithoutLeakingFeedbackText(t *testing.T) {
	source := createLegacySQLite(t, true)

	report, err := DryRun(context.Background(), source)
	if err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	if report.Valid {
		t.Fatal("report.Valid = true, want false")
	}
	if !hasIssue(report, "feedback_entries", "7", "missing_event") {
		t.Errorf("issues = %#v, want missing_event for feedback #7", report.Issues)
	}
	for _, issue := range report.Issues {
		if issue.Message == "private feedback text" {
			t.Fatal("report leaked feedback text")
		}
	}
}

func createLegacySQLite(t *testing.T, brokenFeedbackReference bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bot.sqlite3")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	for _, statement := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`,
		`CREATE TABLE user_profiles (
			user_id INTEGER PRIMARY KEY, full_name TEXT, telegram_username TEXT,
			telegram_first_name TEXT, telegram_last_name TEXT,
			is_russian_citizen INTEGER, is_itmo_student INTEGER,
			feedback_enabled INTEGER, created_at TEXT, updated_at TEXT
		)`,
		`CREATE TABLE events (
			post_id INTEGER PRIMARY KEY, title TEXT, starts_at TEXT, ends_at TEXT,
			status TEXT, cancelled_at TEXT, created_at TEXT
		)`,
		`CREATE TABLE registrations (
			id INTEGER PRIMARY KEY, post_id INTEGER, user_id INTEGER,
			reminders_enabled INTEGER, status TEXT, registered_at TEXT,
			cancelled_at TEXT, reminder_offset_minutes INTEGER,
			reminder_sent_at TEXT, attendance_prompt_sent_at TEXT,
			attended INTEGER, attendance_recorded_at TEXT
		)`,
		`CREATE TABLE feedback_entries (
			id INTEGER PRIMARY KEY, user_id INTEGER, post_id INTEGER,
			text TEXT, created_at TEXT
		)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	const timeValue = "2026-10-01T10:00:00+00:00"
	if _, err := database.Exec("INSERT INTO schema_migrations(version) VALUES (6)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO user_profiles(
			user_id, full_name, telegram_username, telegram_first_name,
			telegram_last_name, is_russian_citizen, is_itmo_student,
			feedback_enabled, created_at, updated_at
		) VALUES (100, 'Test User', 'test_user', 'Test', 'User', 1, 0, 1, ?, ?)`, timeValue, timeValue); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO events(post_id, title, starts_at, ends_at, status, cancelled_at, created_at)
		VALUES (10, 'Meeting', ?, '2026-10-01T12:00:00+00:00', 'active', NULL, ?)`, timeValue, timeValue); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO registrations(
			id, post_id, user_id, reminders_enabled, status, registered_at,
			cancelled_at, reminder_offset_minutes, reminder_sent_at,
			attendance_prompt_sent_at, attended, attendance_recorded_at
		) VALUES (1, 10, 100, 1, 'registered', ?, NULL, 60, ?, ?, 1, ?)`, timeValue, timeValue, timeValue, "2026-10-01T10:30:00+00:00"); err != nil {
		t.Fatal(err)
	}
	feedbackPostID := any(nil)
	if brokenFeedbackReference {
		feedbackPostID = int64(999)
	}
	if _, err := database.Exec(`
		INSERT INTO feedback_entries(id, user_id, post_id, text, created_at)
		VALUES (7, 100, ?, 'private feedback text', ?)`, feedbackPostID, timeValue); err != nil {
		t.Fatal(err)
	}
	return path
}

func hasIssue(report Report, table, legacyID, code string) bool {
	for _, issue := range report.Issues {
		if issue.Table == table && issue.LegacyID == legacyID && issue.Code == code {
			return true
		}
	}
	return false
}
