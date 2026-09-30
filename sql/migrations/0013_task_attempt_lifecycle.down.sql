BEGIN;
DROP INDEX IF EXISTS anclax.idx_tasks_parent_task_id;
DROP INDEX IF EXISTS anclax.idx_tasks_pending_weight_created;
ALTER TABLE anclax.worker_runtime_configs DROP COLUMN request_id;
ALTER TABLE anclax.tasks DROP COLUMN lease_version;
COMMIT;
