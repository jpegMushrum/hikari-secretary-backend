ALTER TABLE santa_participants DROP CONSTRAINT santa_participants_gift_check;

-- Intending to prepare a gift does not establish that an order was placed.
UPDATE santa_participants SET gift = 'unknown' WHERE gift = 'will_be_ready';

ALTER TABLE santa_participants ADD CONSTRAINT santa_participants_gift_check
    CHECK (gift IN ('unknown', 'ready', 'ordered', 'not_ready'));
