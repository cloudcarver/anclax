BEGIN;

DROP INDEX IF EXISTS idx_tasks_serial_order;
DROP INDEX IF EXISTS idx_tasks_serial_key;

ALTER TABLE anclax.tasks
    DROP COLUMN IF EXISTS serial_key,
    DROP COLUMN IF EXISTS serial_id;

COMMIT;
