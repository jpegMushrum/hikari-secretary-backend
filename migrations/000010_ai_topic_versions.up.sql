CREATE TABLE ai_topic_versions (
    job_id BIGINT NOT NULL REFERENCES ai_topic_jobs(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version > 0),
    topic JSONB NOT NULL,
    PRIMARY KEY (job_id, version)
);

-- Only the last saved material is available for pre-migration jobs.
INSERT INTO ai_topic_versions(job_id, version, topic)
SELECT id, 1, topic FROM ai_topic_jobs WHERE jsonb_typeof(topic) = 'object';

-- The job write already serializes replacements for this particular job.
-- This also covers bot scheduling, imports and previously deployed writers.
CREATE FUNCTION record_ai_topic_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND OLD.topic IS NOT DISTINCT FROM NEW.topic THEN
        RETURN NEW;
    END IF;
    INSERT INTO ai_topic_versions(job_id, version, topic)
    SELECT NEW.id, COALESCE(MAX(version), 0) + 1, NEW.topic
    FROM ai_topic_versions WHERE job_id = NEW.id;
    RETURN NEW;
END
$$;

CREATE TRIGGER record_ai_topic_version
AFTER INSERT OR UPDATE OF topic ON ai_topic_jobs FOR EACH ROW
WHEN (jsonb_typeof(NEW.topic) = 'object')
EXECUTE FUNCTION record_ai_topic_version();
