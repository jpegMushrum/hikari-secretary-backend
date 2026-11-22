ALTER TABLE people ADD COLUMN preferred_reminder_offset_minutes INTEGER DEFAULT 60
    CHECK (preferred_reminder_offset_minutes > 0);
