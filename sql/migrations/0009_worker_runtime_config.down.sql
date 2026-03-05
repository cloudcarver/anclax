BEGIN;

DROP INDEX IF EXISTS idx_workers_runtime_version;

DROP TABLE IF EXISTS anclax.worker_runtime_configs;

ALTER TABLE anclax.workers
    DROP COLUMN IF EXISTS applied_config_version;

COMMIT;
