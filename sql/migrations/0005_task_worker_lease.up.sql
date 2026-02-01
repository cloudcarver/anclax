BEGIN;

CREATE TABLE IF NOT EXISTS anclax.workers (
    id UUID PRIMARY KEY,
    labels JSONB,
    status TEXT NOT NULL DEFAULT 'online',
    last_heartbeat TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE anclax.tasks
    ADD COLUMN IF NOT EXISTS locked_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS worker_id UUID;

CREATE INDEX IF NOT EXISTS idx_tasks_locked_at ON anclax.tasks (locked_at);
CREATE INDEX IF NOT EXISTS idx_tasks_worker_id ON anclax.tasks (worker_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status_started_at ON anclax.tasks (status, started_at);
CREATE INDEX IF NOT EXISTS idx_tasks_labels ON anclax.tasks USING GIN ((attributes->'labels'));
CREATE INDEX IF NOT EXISTS idx_workers_last_heartbeat ON anclax.workers (last_heartbeat);

COMMIT;
