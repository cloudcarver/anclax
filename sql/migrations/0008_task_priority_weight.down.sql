BEGIN;

DROP INDEX IF EXISTS idx_tasks_pending_priority_created;

ALTER TABLE anclax.tasks
    DROP CONSTRAINT IF EXISTS tasks_priority_non_negative,
    DROP CONSTRAINT IF EXISTS tasks_weight_positive,
    DROP COLUMN IF EXISTS priority,
    DROP COLUMN IF EXISTS weight;

COMMIT;
