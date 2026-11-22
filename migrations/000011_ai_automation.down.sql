ALTER TABLE ai_topic_jobs DROP COLUMN paused_by_automation, DROP COLUMN automatic;
ALTER TABLE ai_topic_settings DROP COLUMN automation_enabled, DROP COLUMN automation_resumed_at;
