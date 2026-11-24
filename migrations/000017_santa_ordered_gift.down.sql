ALTER TABLE santa_participants DROP CONSTRAINT santa_participants_gift_check;

-- The old choices require a prediction about meeting the deadline.
UPDATE santa_participants SET gift = 'unknown' WHERE gift IN ('ordered', 'not_ready');

ALTER TABLE santa_participants ADD CONSTRAINT santa_participants_gift_check
    CHECK (gift IN ('unknown', 'ready', 'will_be_ready', 'not_ready'));
