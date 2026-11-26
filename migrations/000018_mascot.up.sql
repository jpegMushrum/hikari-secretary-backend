CREATE TABLE mascot_seasons (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    phase text NOT NULL DEFAULT 'preparation' CHECK (phase IN ('preparation','submissions','moderation','readiness','voting','results','published','closed')),
    deadline timestamptz,
    round integer NOT NULL DEFAULT 0 CHECK (round >= 0),
    winner_id bigint
);
CREATE UNIQUE INDEX mascot_single_current ON mascot_seasons ((true)) WHERE phase <> 'closed';

CREATE TABLE mascot_entries (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    season_id bigint NOT NULL REFERENCES mascot_seasons,
    person_id bigint NOT NULL REFERENCES people,
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 120),
    description text NOT NULL CHECK (char_length(btrim(description)) BETWEEN 1 AND 2000),
    approved boolean NOT NULL DEFAULT false,
    request_id text,
    UNIQUE (season_id,person_id,request_id),
    UNIQUE (season_id,id)
);
ALTER TABLE mascot_seasons ADD FOREIGN KEY (id,winner_id) REFERENCES mascot_entries(season_id,id);

CREATE TABLE mascot_images (
    key text PRIMARY KEY,
    season_id bigint NOT NULL REFERENCES mascot_seasons,
    person_id bigint NOT NULL REFERENCES people,
    entry_id bigint,
    position integer CHECK (position BETWEEN 0 AND 2),
    ai boolean NOT NULL DEFAULT false,
    state text NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved','stored','purging')),
    touched_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY(season_id,entry_id) REFERENCES mascot_entries(season_id,id),
    UNIQUE(entry_id,position) DEFERRABLE INITIALLY DEFERRED,
    CHECK ((entry_id IS NULL) = (position IS NULL))
);
CREATE INDEX mascot_image_owner ON mascot_images (season_id,person_id);

CREATE TABLE mascot_rounds (
    season_id bigint NOT NULL REFERENCES mascot_seasons,
    number integer NOT NULL CHECK(number>0),
    status text NOT NULL CHECK(status IN ('ready','voting','finished','cancelled')),
    PRIMARY KEY(season_id,number)
);
CREATE TABLE mascot_candidates (
    season_id bigint NOT NULL,
    round integer NOT NULL,
    entry_id bigint NOT NULL,
    PRIMARY KEY(season_id,round,entry_id),
    FOREIGN KEY(season_id,round) REFERENCES mascot_rounds,
    FOREIGN KEY(season_id,entry_id) REFERENCES mascot_entries(season_id,id)
);
CREATE TABLE mascot_voters (
    season_id bigint NOT NULL,
    round integer NOT NULL,
    person_id bigint NOT NULL REFERENCES people,
    excluded boolean NOT NULL DEFAULT false,
    PRIMARY KEY(season_id,round,person_id),
    FOREIGN KEY(season_id,round) REFERENCES mascot_rounds
);
CREATE TABLE mascot_answers (
    season_id bigint NOT NULL,
    round integer NOT NULL,
    person_id bigint NOT NULL,
    entry_id bigint NOT NULL,
    liked boolean NOT NULL,
    PRIMARY KEY(season_id,round,person_id,entry_id),
    FOREIGN KEY(season_id,round,person_id) REFERENCES mascot_voters,
    FOREIGN KEY(season_id,round,entry_id) REFERENCES mascot_candidates
);
CREATE INDEX mascot_answer_tally ON mascot_answers(season_id,round,entry_id);
