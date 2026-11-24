ALTER TABLE santa_participants ADD COLUMN gift_description text NOT NULL DEFAULT ''
    CHECK (char_length(gift_description) <= 2000);
