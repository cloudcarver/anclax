BEGIN;

DROP INDEX IF EXISTS idx_workers_last_heartbeat;
DROP INDEX IF EXISTS idx_tasks_labels;
DROP INDEX IF EXISTS idx_tasks_status_started_at;
DROP INDEX IF EXISTS idx_tasks_worker_id;
DROP INDEX IF EXISTS idx_tasks_locked_at;

ALTER TABLE anclax.tasks
    DROP COLUMN IF EXISTS locked_at,
    DROP COLUMN IF EXISTS worker_id;

DROP TABLE IF EXISTS anclax.workers;

COMMIT;
