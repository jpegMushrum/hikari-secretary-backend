ALTER TABLE ai_topic_jobs ADD COLUMN regeneration_requested_by BIGINT;

-- Older workers could commit the publication before linking its job.
-- Recover the preview and delivery state from the immutable source key.
UPDATE ai_topic_jobs j
SET publication_id=p.id, status='scheduled', last_error=NULL, updated_at=NOW()
FROM publications p
WHERE j.publication_id IS NULL AND j.status IN ('failed','retry','pending','generating','scheduled')
  AND p.source='ai_daily_topic' AND p.source_key='daily-topic-job:' || j.id::text
  AND p.status IN ('scheduled','sent','failed','cancelled');

CREATE VIEW regenerable_ai_jobs AS
SELECT j.id
FROM ai_topic_jobs j
LEFT JOIN publications p ON p.id = j.publication_id
WHERE (j.status = 'failed' AND j.publication_id IS NULL)
   OR (j.status IN ('scheduled', 'failed')
       AND p.status = 'scheduled' AND p.source = 'ai_daily_topic'
       AND EXISTS (SELECT 1 FROM publication_deliveries d WHERE d.publication_id = p.id)
       AND NOT EXISTS (SELECT 1 FROM publication_deliveries d WHERE d.publication_id = p.id AND d.status <> 'pending'));
