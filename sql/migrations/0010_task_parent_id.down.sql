BEGIN;

ALTER TABLE anclax.tasks
    DROP COLUMN IF EXISTS parent_task_id;

COMMIT;
