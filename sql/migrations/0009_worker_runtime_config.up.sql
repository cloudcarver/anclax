BEGIN;

ALTER TABLE anclax.workers
    ADD COLUMN IF NOT EXISTS applied_config_version BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS anclax.worker_runtime_configs (
    version BIGSERIAL PRIMARY KEY,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_workers_runtime_version
    ON anclax.workers (applied_config_version);

COMMIT;
