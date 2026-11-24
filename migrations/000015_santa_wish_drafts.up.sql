CREATE TABLE santa_wish_drafts (
    season_id bigint NOT NULL REFERENCES santa_seasons(id) ON DELETE CASCADE,
    person_id bigint NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    wishes text NOT NULL CHECK (char_length(btrim(wishes)) BETWEEN 1 AND 2000),
    PRIMARY KEY (season_id, person_id)
);
