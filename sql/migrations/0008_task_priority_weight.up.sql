BEGIN;

ALTER TABLE anclax.tasks
    ADD COLUMN IF NOT EXISTS priority INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS weight INT NOT NULL DEFAULT 1;

UPDATE anclax.tasks
SET
    priority = GREATEST(COALESCE((attributes->>'priority')::int, 0), 0),
    weight = GREATEST(COALESCE((attributes->>'weight')::int, 1), 1)
WHERE TRUE;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'tasks_priority_non_negative'
    ) THEN
        ALTER TABLE anclax.tasks
            ADD CONSTRAINT tasks_priority_non_negative CHECK (priority >= 0);
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'tasks_weight_positive'
    ) THEN
        ALTER TABLE anclax.tasks
            ADD CONSTRAINT tasks_weight_positive CHECK (weight >= 1);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_tasks_pending_priority_created
    ON anclax.tasks (priority DESC, created_at, id)
    WHERE status = 'pending';

COMMIT;
