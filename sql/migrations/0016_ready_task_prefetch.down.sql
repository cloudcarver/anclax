BEGIN;
UPDATE anclax.tasks SET status='pending',ready_expires_at=NULL WHERE status='ready';
UPDATE anclax.tasks SET status='pending' WHERE status='running';
DELETE FROM anclax.tasks WHERE spec->>'type'='prefetchTasks';
DROP FUNCTION anclax.prefetch_ready_tasks(INT,UUID,BIGINT,INT,BIGINT,BIGINT);
DROP FUNCTION anclax.prefetch_task_supply(INT,UUID,BIGINT,INT,BIGINT,BIGINT);
DROP FUNCTION anclax.recover_ready_tasks(INT);
DROP TRIGGER reclassify_task_admission_groups ON anclax.task_tag_limits;
DROP FUNCTION anclax.reclassify_task_admission_groups();
DROP TRIGGER aaa_invalidate_task_reservation ON anclax.tasks;
DROP FUNCTION anclax.invalidate_task_reservation();
DROP TRIGGER classify_task_admission ON anclax.tasks;
DROP FUNCTION anclax.classify_task_admission();
DROP FUNCTION anclax.try_admit_task_tags(INT,BIGINT);
ALTER FUNCTION anclax.allocate_task_tag_slots(INT) RENAME TO try_admit_task_tags;
CREATE OR REPLACE FUNCTION anclax.release_task_tags_on_unlock() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM anclax.release_task_tag_permits(OLD.id);
        RETURN OLD;
    END IF;
    IF OLD.locked_at IS NOT NULL AND NEW.locked_at IS NULL THEN
        PERFORM anclax.release_task_tag_permits(OLD.id);
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER snapshot_task_lease_tags ON anclax.tasks;
CREATE OR REPLACE FUNCTION anclax.snapshot_task_lease_tags() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.locked_at IS NULL THEN
        NEW.lease_tags := ARRAY[]::text[];
    ELSIF TG_OP = 'INSERT' OR OLD.locked_at IS NULL OR NEW.lease_version > OLD.lease_version THEN
        SELECT COALESCE(array_agg(DISTINCT tag ORDER BY tag), ARRAY[]::text[]) INTO NEW.lease_tags
        FROM jsonb_array_elements_text(COALESCE(NULLIF(NEW.attributes->'tags', 'null'::jsonb), '[]'::jsonb)) AS tags(tag)
        WHERE NEW.spec->>'type' NOT IN ('broadcastUpdateWorkerRuntimeConfig', 'applyWorkerRuntimeConfigToWorker', 'broadcastCancelTask', 'cancelTaskOnWorker', 'broadcastPauseTask', 'pauseTaskOnWorker');
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION anclax.sync_task_tags() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    needs_membership BOOLEAN := NEW.status IN ('pending', 'running', 'paused') OR NEW.locked_at IS NOT NULL;
BEGIN
    -- Status and lease transitions maintain membership too. Ordinary renewals
    -- return before touching any tag rows, while restoring a historical task
    -- rebuilds membership even when its tags did not change.
    IF TG_OP = 'UPDATE' AND NEW.attributes->'tags' IS NOT DISTINCT FROM OLD.attributes->'tags'
       AND needs_membership = (OLD.status IN ('pending', 'running', 'paused') OR OLD.locked_at IS NOT NULL) THEN
        RETURN NEW;
    END IF;
    IF NOT needs_membership THEN
        IF TG_OP = 'UPDATE' THEN
            DELETE FROM anclax.task_tags WHERE task_id = NEW.id;
        END IF;
        RETURN NEW;
    END IF;
    DELETE FROM anclax.task_tags WHERE task_id = NEW.id;
    INSERT INTO anclax.task_tags (task_id, tag)
    SELECT DISTINCT NEW.id, tag
    FROM jsonb_array_elements_text(COALESCE(NULLIF(NEW.attributes->'tags', 'null'::jsonb), '[]'::jsonb)) AS tags(tag);
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION anclax.task_tag_limit_changed() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    -- Rebuild from the durable attempt snapshot while the before-statement
    -- task-table barrier excludes every claim, release and renewal transaction.
    DELETE FROM anclax.task_tag_slots WHERE tag=NEW.tag;
    IF NEW.max_concurrency IS NOT NULL THEN
        WITH owners AS (
            SELECT id, lease_version, row_number() OVER (ORDER BY id)::int AS slot_no
            FROM anclax.tasks WHERE locked_at IS NOT NULL AND NEW.tag=ANY(lease_tags)
        )
        INSERT INTO anclax.task_tag_slots(tag,slot_no,retired,task_id,lease_version)
        SELECT NEW.tag,n,n>NEW.max_concurrency,o.id,o.lease_version
        FROM generate_series(1,GREATEST(NEW.max_concurrency,(SELECT count(*)::int FROM owners))) n
        LEFT JOIN owners o ON o.slot_no=n;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER snapshot_task_lease_tags BEFORE INSERT OR UPDATE OF locked_at ON anclax.tasks
FOR EACH ROW EXECUTE FUNCTION anclax.snapshot_task_lease_tags();
DROP INDEX anclax.idx_tasks_pending_due;
DROP INDEX anclax.idx_tasks_system_pending;
DROP INDEX anclax.idx_tasks_unclassified;
DROP FUNCTION anclax.is_system_task(TEXT);
DROP INDEX anclax.idx_tasks_ready_priority;
DROP INDEX anclax.idx_tasks_ready_weight;
DROP INDEX anclax.idx_tasks_ready_expiry;
DROP INDEX anclax.idx_tasks_serial_ready;
DROP INDEX anclax.idx_tasks_admission_candidates;
DROP INDEX anclax.idx_tasks_serial_pending_head;
CREATE INDEX idx_tasks_serial_pending_head ON anclax.tasks
 (serial_key,(serial_id IS NULL),(COALESCE(serial_id,2147483647)),created_at,(COALESCE(started_at,'-infinity'::timestamptz)),id)
 WHERE status='pending' AND serial_key IS NOT NULL;
ALTER TABLE anclax.tasks DROP CONSTRAINT tasks_ready_reservation_shape;
ALTER TABLE anclax.tasks DROP COLUMN ready_expires_at,DROP COLUMN admission_group_id;
DROP TABLE anclax.task_admission_groups;
ALTER TABLE anclax.workers DROP COLUMN prefetch_capacity,DROP COLUMN prefetch_strict_percentage,DROP COLUMN prefetch_heartbeat_ttl_ms;
COMMIT;
