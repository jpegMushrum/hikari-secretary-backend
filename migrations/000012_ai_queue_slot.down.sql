-- Restoring the old unique constraint fails atomically if multiple editions
-- already share a time. Keep their publication history intact.
ALTER TABLE ai_topic_jobs ADD UNIQUE (scheduled_for);
DROP INDEX ai_topic_jobs_current_slot;
ALTER TABLE ai_topic_jobs DROP COLUMN slot_released;
