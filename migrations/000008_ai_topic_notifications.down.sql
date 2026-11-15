DROP INDEX IF EXISTS idx_ai_topic_jobs_failure_notifications;
ALTER TABLE ai_topic_jobs DROP COLUMN IF EXISTS failure_notified;
