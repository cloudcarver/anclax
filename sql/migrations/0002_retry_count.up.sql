BEGIN;

ALTER TABLE anchor.tasks ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;

UPDATE anchor.tasks SET attributes = jsonb_set(attributes, '{retryPolicy, maxAttempts}', '-1') WHERE (attributes->'retryPolicy'->'always_retry_on_failure')::BOOLEAN;

COMMIT;
