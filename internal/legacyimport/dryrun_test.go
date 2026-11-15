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
	if report.SourceVersion != 6 {
		t.Errorf("source version = %d, want 6", report.SourceVersion)
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
		`CREATE TABLE posts (id INTEGER PRIMARY KEY, creator_id INTEGER, text TEXT, entities_json TEXT, rich_message_json TEXT, source TEXT, source_key TEXT, status TEXT, scheduled_at TEXT, created_at TEXT)`,
		`CREATE TABLE deliveries (id INTEGER PRIMARY KEY, post_id INTEGER, platform TEXT, target_key TEXT, target_name TEXT, destination TEXT, message_thread_id INTEGER, status TEXT, attempts INTEGER, next_attempt_at TEXT)`,
		`CREATE TABLE media (post_id INTEGER, position INTEGER, path TEXT)`,
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
		`CREATE TABLE ai_topic_settings (id INTEGER PRIMARY KEY, jlpt_level TEXT, admin_comment TEXT, updated_by INTEGER, updated_at TEXT)`,
		`CREATE TABLE ai_topic_jobs (id INTEGER PRIMARY KEY, scheduled_for TEXT, status TEXT, attempts INTEGER, next_attempt_at TEXT, prompt_version TEXT, topic_json TEXT, post_id INTEGER, last_error TEXT, created_at TEXT, updated_at TEXT)`,
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

func TestLoadSnapshotIncludesScheduledNonEventPublication(t *testing.T) {
	source := createLegacySQLite(t, false)
	database, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	const scheduledAt = "2099-01-01T10:00:00+00:00"
	if _, err = database.Exec(`INSERT INTO posts(id,creator_id,text,entities_json,rich_message_json,source,status,scheduled_at,created_at) VALUES(77,100,'Ordinary scheduled post','[]','', 'admin','scheduled',?,?)`, scheduledAt, scheduledAt); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`INSERT INTO deliveries(post_id,platform,target_key,target_name,destination,message_thread_id,status,attempts,next_attempt_at) VALUES(77,'telegram','main','Main','-100',NULL,'pending',0,?)`, scheduledAt); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`INSERT INTO media(post_id,position,path) VALUES(77,0,'/app/data/media/post.jpg')`); err != nil {
		t.Fatal(err)
	}

	snapshot, err := loadSnapshot(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.publications) != 1 {
		t.Fatalf("scheduled publications = %d, want 1", len(snapshot.publications))
	}
	publication := snapshot.publications[0]
	if publication.id != 77 || len(publication.deliveries) != 1 || len(publication.media) != 1 {
		t.Fatalf("unexpected publication snapshot: %#v", publication)
	}
}

func TestDryRunAcceptsVersionFiveWithoutFeedbackTables(t *testing.T) {
	source := createLegacySQLite(t, false)
	database, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("UPDATE schema_migrations SET version=5"); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("DROP TABLE feedback_entries"); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("ALTER TABLE user_profiles DROP COLUMN feedback_enabled"); err != nil {
		t.Fatal(err)
	}
	if err = database.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := DryRun(context.Background(), source)
	if err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	if !report.Valid {
		t.Fatalf("version 5 report is invalid: %#v", report.Issues)
	}
	if report.SourceVersion != 5 || report.Counts.Feedback != 0 {
		t.Fatalf("unexpected version 5 report: %#v", report)
	}
}

func hasIssue(report Report, table, legacyID, code string) bool {
	for _, issue := range report.Issues {
		if issue.Table == table && issue.LegacyID == legacyID && issue.Code == code {
			return true
		}
	}
	return false
}
