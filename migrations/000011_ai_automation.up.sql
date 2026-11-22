ALTER TABLE ai_topic_settings ADD COLUMN automation_enabled boolean NOT NULL DEFAULT true;
ALTER TABLE ai_topic_settings ADD COLUMN automation_resumed_at timestamptz;
ALTER TABLE ai_topic_jobs
    ADD COLUMN automatic boolean NOT NULL DEFAULT true,
    ADD COLUMN paused_by_automation boolean NOT NULL DEFAULT false;

-- The scheduler owns unlinked jobs and its own stable publication keys.
-- One-off posts requested via the manual API retain their independent schedules.
UPDATE ai_topic_jobs j SET automatic=false FROM publications p
WHERE p.id=j.publication_id AND p.source_key IS DISTINCT FROM ('daily-topic-job:' || j.id::text);
