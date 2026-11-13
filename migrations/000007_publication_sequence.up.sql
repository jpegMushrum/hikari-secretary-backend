-- 000005 runs before the legacy importer. The importer can subsequently add
-- event references whose legacy publication ID is larger than the sequence,
-- so bring it forward again on every existing installation.
SELECT setval(
    pg_get_serial_sequence('publications', 'id'),
    GREATEST(
        1,
        COALESCE((SELECT MAX(id) FROM publications), 1),
        COALESCE((SELECT MAX(external_publication_id::BIGINT)
                  FROM event_publication_references
                  WHERE external_publication_id ~ '^[0-9]+$'), 1)
    ),
    true
);
