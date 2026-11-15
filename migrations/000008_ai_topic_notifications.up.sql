ALTER TABLE ai_topic_jobs
    ADD COLUMN failure_notified BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX idx_ai_topic_jobs_failure_notifications
    ON ai_topic_jobs(status, failure_notified)
    WHERE status = 'failed' AND failure_notified = FALSE;
