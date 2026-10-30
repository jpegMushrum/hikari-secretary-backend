CREATE TABLE people (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    full_name TEXT,
    is_russian_citizen BOOLEAN,
    is_itmo_student BOOLEAN,
    feedback_offers_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT people_full_name_length CHECK (
        full_name IS NULL OR char_length(btrim(full_name)) BETWEEN 1 AND 200
    )
);

CREATE TABLE telegram_identities (
    person_id BIGINT PRIMARY KEY REFERENCES people(id) ON DELETE CASCADE,
    telegram_user_id BIGINT NOT NULL UNIQUE,
    username TEXT,
    display_name TEXT NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT telegram_identities_display_name_length CHECK (
        char_length(btrim(display_name)) BETWEEN 1 AND 256
    ),
    CONSTRAINT telegram_identities_username_length CHECK (
        username IS NULL OR char_length(username) BETWEEN 1 AND 32
    )
);

CREATE INDEX idx_people_full_name_id ON people(full_name, id);
CREATE INDEX idx_telegram_identities_username ON telegram_identities(username);

CREATE TABLE events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title TEXT NOT NULL,
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_by_person_id BIGINT REFERENCES people(id) ON DELETE SET NULL,
    cancelled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT events_title_length CHECK (char_length(btrim(title)) BETWEEN 1 AND 160),
    CONSTRAINT events_time_range CHECK (starts_at < ends_at),
    CONSTRAINT events_status CHECK (status IN ('active', 'cancelled', 'completed')),
    CONSTRAINT events_cancelled_at CHECK (
        (status = 'cancelled' AND cancelled_at IS NOT NULL)
        OR (status <> 'cancelled' AND cancelled_at IS NULL)
    )
);

CREATE INDEX idx_events_status_starts_at_id ON events(status, starts_at, id);

CREATE TABLE event_publication_references (
    event_id BIGINT PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    external_publication_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT event_publication_references_provider CHECK (
        provider IN ('legacy_sqlite', 'hikari_publishing')
    ),
    UNIQUE(provider, external_publication_id)
);

CREATE TABLE event_registrations (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id BIGINT NOT NULL REFERENCES events(id) ON DELETE RESTRICT,
    person_id BIGINT NOT NULL REFERENCES people(id) ON DELETE RESTRICT,
    status TEXT NOT NULL DEFAULT 'registered',
    registered_at TIMESTAMPTZ NOT NULL,
    cancelled_at TIMESTAMPTZ,
    reminders_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    reminder_offset_minutes INTEGER,
    reminder_sent_at TIMESTAMPTZ,
    attendance_prompt_sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT event_registrations_status CHECK (status IN ('registered', 'cancelled')),
    CONSTRAINT event_registrations_cancelled_at CHECK (
        (status = 'cancelled' AND cancelled_at IS NOT NULL)
        OR (status = 'registered' AND cancelled_at IS NULL)
    ),
    CONSTRAINT event_registrations_reminder_offset CHECK (
        reminder_offset_minutes IS NULL OR reminder_offset_minutes > 0
    ),
    UNIQUE(event_id, person_id)
);

CREATE INDEX idx_event_registrations_event_status_registered_at
    ON event_registrations(event_id, status, registered_at, id);
CREATE INDEX idx_event_registrations_person_status
    ON event_registrations(person_id, status, event_id);
CREATE INDEX idx_event_registrations_reminder_due
    ON event_registrations(status, reminder_sent_at)
    WHERE reminders_enabled AND reminder_offset_minutes IS NOT NULL;

CREATE TABLE attendance (
    registration_id BIGINT PRIMARY KEY REFERENCES event_registrations(id) ON DELETE CASCADE,
    prompted_at TIMESTAMPTZ,
    attended BOOLEAN NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT attendance_recorded_after_prompt CHECK (
        prompted_at IS NULL OR recorded_at >= prompted_at
    )
);

CREATE TABLE feedback (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    person_id BIGINT NOT NULL REFERENCES people(id) ON DELETE RESTRICT,
    event_id BIGINT REFERENCES events(id) ON DELETE SET NULL,
    text TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT feedback_text_length CHECK (char_length(btrim(text)) BETWEEN 1 AND 4000)
);

CREATE INDEX idx_feedback_created_at_id ON feedback(created_at DESC, id DESC);
CREATE INDEX idx_feedback_person_created_at ON feedback(person_id, created_at DESC, id DESC);
CREATE INDEX idx_feedback_event_created_at ON feedback(event_id, created_at DESC, id DESC);

CREATE TABLE outbox (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id BIGINT NOT NULL,
    payload JSONB NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'pending',
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT outbox_status CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    CONSTRAINT outbox_attempts CHECK (attempts >= 0)
);

CREATE INDEX idx_outbox_due ON outbox(status, available_at, id);

CREATE TABLE audit_log (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_type TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    action TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id BIGINT,
    idempotency_key TEXT,
    data JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(actor_type, idempotency_key)
);

CREATE INDEX idx_audit_log_object ON audit_log(object_type, object_id, created_at DESC);

CREATE TABLE legacy_import_map (
    source_kind TEXT NOT NULL,
    source_id TEXT NOT NULL,
    target_kind TEXT NOT NULL,
    target_id BIGINT NOT NULL,
    imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_kind, source_id),
    UNIQUE(target_kind, target_id),
    CONSTRAINT legacy_import_map_source_kind CHECK (
        source_kind IN ('user_profile', 'event', 'registration', 'feedback')
    ),
    CONSTRAINT legacy_import_map_target_kind CHECK (
        target_kind IN ('person', 'event', 'registration', 'feedback')
    )
);
