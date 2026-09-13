BEGIN;

ALTER TABLE anclax.tasks
    ADD COLUMN lease_version BIGINT NOT NULL DEFAULT 0;

ALTER TABLE anclax.worker_runtime_configs
    ADD COLUMN request_id TEXT UNIQUE;

CREATE INDEX idx_tasks_pending_weight_created
    ON anclax.tasks (weight DESC, created_at, id)
    WHERE status = 'pending' AND priority = 0;

CREATE INDEX idx_tasks_parent_task_id ON anclax.tasks (parent_task_id);

COMMIT;
