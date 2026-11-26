-- NULL retains the original per-person order in rounds already in progress.
ALTER TABLE mascot_voters ADD COLUMN ballot_slot bigint CHECK (ballot_slot >= 0);
