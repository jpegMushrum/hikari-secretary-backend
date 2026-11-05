CREATE TABLE bot_flows (
    telegram_user_id BIGINT NOT NULL REFERENCES telegram_identities(telegram_user_id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('registration', 'profile_edit', 'feedback')),
    publication_id BIGINT,
    stage TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (telegram_user_id, kind)
);

CREATE INDEX idx_bot_flows_updated_at ON bot_flows(updated_at);
