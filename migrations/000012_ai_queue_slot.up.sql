-- A cancelled or delivered edition remains in history when an administrator
-- requests a new edition for the very same publication time.
ALTER TABLE ai_topic_jobs ADD COLUMN slot_released boolean NOT NULL DEFAULT false;
ALTER TABLE ai_topic_jobs DROP CONSTRAINT ai_topic_jobs_scheduled_for_key;
CREATE UNIQUE INDEX ai_topic_jobs_current_slot ON ai_topic_jobs(scheduled_for) WHERE NOT slot_released;
