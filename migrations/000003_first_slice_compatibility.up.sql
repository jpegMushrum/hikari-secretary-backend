-- The first draft of 000002 was usable before the bot cutover fields were
-- finalized. Keep upgrades safe for databases that already applied it.
ALTER TABLE event_publication_references
    ADD COLUMN IF NOT EXISTS published BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE event_registrations
    ADD COLUMN IF NOT EXISTS reminders_enabled BOOLEAN NOT NULL DEFAULT FALSE;

DROP INDEX IF EXISTS idx_event_registrations_reminder_due;
CREATE INDEX idx_event_registrations_reminder_due
    ON event_registrations(status, reminder_sent_at)
    WHERE reminders_enabled AND reminder_offset_minutes IS NOT NULL;
