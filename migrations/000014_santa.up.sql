CREATE TABLE santa_seasons (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    status text NOT NULL DEFAULT 'preparation' CHECK (status IN ('preparation','open','paused','closed')),
    budget text NOT NULL DEFAULT '' CHECK (char_length(budget) <= 200),
    registration_until timestamptz,
    exchange_at timestamptz,
    reminder_minutes integer NOT NULL DEFAULT 1440 CHECK (reminder_minutes BETWEEN 1 AND 43200),
    revision bigint NOT NULL DEFAULT 1,
    confirmed_revision bigint,
    CHECK (registration_until < exchange_at)
);
CREATE UNIQUE INDEX santa_single_current ON santa_seasons ((true)) WHERE status <> 'closed';

CREATE TABLE santa_participants (
    season_id bigint NOT NULL REFERENCES santa_seasons(id),
    person_id bigint NOT NULL REFERENCES people(id),
    wishes text NOT NULL CHECK (char_length(btrim(wishes)) BETWEEN 1 AND 2000),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','exit_requested','excluded')),
    attendance text NOT NULL DEFAULT 'unknown' CHECK (attendance IN ('unknown','yes','no')),
    gift text NOT NULL DEFAULT 'unknown' CHECK (gift IN ('unknown','ready','will_be_ready','not_ready')),
    reminder_sent boolean NOT NULL DEFAULT false,
    PRIMARY KEY (season_id, person_id)
);

-- Before confirmation these rows are private to the administrator.
CREATE TABLE santa_pairs (
    season_id bigint NOT NULL,
    giver_id bigint NOT NULL,
    recipient_id bigint NOT NULL,
    PRIMARY KEY (season_id, giver_id),
    UNIQUE (season_id, recipient_id),
    FOREIGN KEY (season_id, giver_id) REFERENCES santa_participants (season_id, person_id),
    FOREIGN KEY (season_id, recipient_id) REFERENCES santa_participants (season_id, person_id),
    CHECK (giver_id <> recipient_id)
);
