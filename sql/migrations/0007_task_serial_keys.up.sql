BEGIN;

ALTER TABLE anclax.tasks
    ADD COLUMN IF NOT EXISTS serial_key TEXT,
    ADD COLUMN IF NOT EXISTS serial_id INT;

UPDATE anclax.tasks
SET
    serial_key = NULLIF(attributes->>'serialKey', ''),
    serial_id = CASE
        WHEN attributes ? 'serialID' THEN (attributes->>'serialID')::int
        ELSE NULL
    END
WHERE serial_key IS NULL AND serial_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_serial_key ON anclax.tasks (serial_key);
CREATE INDEX IF NOT EXISTS idx_tasks_serial_order ON anclax.tasks (serial_key, serial_id, created_at, started_at, id);

COMMIT;
