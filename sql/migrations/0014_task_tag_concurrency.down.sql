BEGIN;
DROP TRIGGER release_task_tags_on_unlock ON anclax.tasks;
DROP TRIGGER release_task_tags_on_delete ON anclax.tasks;
DROP TRIGGER sync_task_tags ON anclax.tasks;
DROP TRIGGER classify_task_tag_wait ON anclax.tasks;
DROP TRIGGER task_tag_limit_changed ON anclax.task_tag_concurrency;
DROP FUNCTION anclax.maintain_task_concurrency(BIGINT);
DROP FUNCTION anclax.try_admit_task_tags(INT);
DROP FUNCTION anclax.release_task_tags_on_unlock();
DROP FUNCTION anclax.release_task_tag_permits(INT);
DROP FUNCTION anclax.task_tag_limit_changed();
DROP FUNCTION anclax.wake_task_tag_waiters(TEXT, INT);
DROP FUNCTION anclax.sync_task_tags();
DROP FUNCTION anclax.classify_task_tag_wait();
DROP TABLE anclax.task_tag_permits;
DROP TABLE anclax.task_tags;
DROP TABLE anclax.task_tag_concurrency;
DROP INDEX anclax.idx_tasks_pending_priority_created;
DROP INDEX anclax.idx_tasks_pending_weight_created;
ALTER TABLE anclax.tasks
    DROP COLUMN lease_expires_at,
    DROP COLUMN lease_duration_ms,
    DROP COLUMN concurrency_wait_tag,
    DROP COLUMN concurrency_retry_at;
CREATE INDEX idx_tasks_pending_priority_created
    ON anclax.tasks (priority DESC, created_at, id) WHERE status = 'pending';
CREATE INDEX idx_tasks_pending_weight_created
    ON anclax.tasks (weight DESC, created_at, id) WHERE status = 'pending' AND priority = 0;
COMMIT;
