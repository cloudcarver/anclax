BEGIN;

-- Workers must be stopped for this protocol upgrade. Ordinary tags no longer
-- have permits/counters. A task-local snapshot preserves admission-time tags
-- for the rare operation that enables a previously unlimited tag.
ALTER TABLE anclax.tasks ADD COLUMN lease_tags TEXT[] NOT NULL DEFAULT '{}';
UPDATE anclax.tasks t SET lease_tags = COALESCE(
    (SELECT array_agg(DISTINCT p.tag ORDER BY p.tag) FROM anclax.task_tag_permits p WHERE p.task_id = t.id),
    ARRAY(SELECT DISTINCT tag FROM jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'tags', 'null'::jsonb), '[]'::jsonb)) AS tags(tag) ORDER BY tag))
WHERE t.locked_at IS NOT NULL
  AND t.spec->>'type' NOT IN ('broadcastUpdateWorkerRuntimeConfig', 'applyWorkerRuntimeConfigToWorker', 'broadcastCancelTask', 'cancelTaskOnWorker', 'broadcastPauseTask', 'pauseTaskOnWorker');
ALTER TABLE anclax.task_tags DROP CONSTRAINT task_tags_tag_fkey;
DELETE FROM anclax.task_tag_permits p USING anclax.task_tag_concurrency c
WHERE p.tag = c.tag AND c.max_concurrency IS NULL;
DELETE FROM anclax.task_tag_concurrency WHERE max_concurrency IS NULL;
-- v14 could park a task on an unlimited tag solely because its counter row
-- was busy. Its registry row is gone now, so maintenance cannot find it.
UPDATE anclax.tasks t SET concurrency_wait_tag = NULL, concurrency_retry_at = NULL
WHERE t.concurrency_wait_tag IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM anclax.task_tag_concurrency c WHERE c.tag = t.concurrency_wait_tag);

CREATE INDEX idx_tasks_serial_leased ON anclax.tasks (serial_key)
    INCLUDE (lease_expires_at, locked_at)
    WHERE serial_key IS NOT NULL AND (lease_expires_at IS NOT NULL OR locked_at IS NOT NULL);
CREATE INDEX idx_tasks_serial_pending_head ON anclax.tasks
    (serial_key, (serial_id IS NULL), (COALESCE(serial_id, 2147483647)), created_at,
     (COALESCE(started_at, '-infinity'::timestamptz)), id)
    WHERE status = 'pending' AND serial_key IS NOT NULL;

CREATE FUNCTION anclax.snapshot_task_lease_tags() RETURNS TRIGGER LANGUAGE plpgsql AS $$
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
CREATE TRIGGER snapshot_task_lease_tags BEFORE INSERT OR UPDATE OF locked_at ON anclax.tasks
FOR EACH ROW EXECUTE FUNCTION anclax.snapshot_task_lease_tags();

-- Take the table lock BEFORE any configuration row is locked (statement
-- trigger, including INSERT ... ON CONFLICT). EXCLUSIVE also conflicts with
-- SELECT FOR UPDATE, so admission/expiry/finalize cannot straddle the backfill.
-- Counter-only UPDATEs do not run this trigger. This is configuration-time
-- synchronization, not a lock acquired by ordinary task transactions.
CREATE FUNCTION anclax.lock_task_tag_configuration() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('transaction_isolation') NOT IN ('read committed', 'read uncommitted') THEN
        RAISE EXCEPTION 'task tag configuration requires READ COMMITTED isolation' USING ERRCODE = '0A000';
    END IF;
    LOCK TABLE anclax.tasks IN EXCLUSIVE MODE;
    RETURN NULL;
END;
$$;
CREATE TRIGGER lock_task_tag_configuration BEFORE INSERT OR UPDATE OF max_concurrency OR DELETE
ON anclax.task_tag_concurrency FOR EACH STATEMENT EXECUTE FUNCTION anclax.lock_task_tag_configuration();

CREATE OR REPLACE FUNCTION anclax.task_tag_limit_changed() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.max_concurrency IS NULL THEN
        DELETE FROM anclax.task_tag_permits WHERE tag = NEW.tag;
        UPDATE anclax.task_tag_concurrency SET in_use = 0 WHERE tag = NEW.tag;
    ELSIF TG_OP = 'INSERT' OR OLD.max_concurrency IS NULL THEN
        INSERT INTO anclax.task_tag_permits (task_id, lease_version, tag)
        SELECT id, lease_version, NEW.tag FROM anclax.tasks
        WHERE locked_at IS NOT NULL AND NEW.tag = ANY(lease_tags)
        ON CONFLICT DO NOTHING;
        UPDATE anclax.task_tag_concurrency SET in_use =
            (SELECT count(*)::int FROM anclax.task_tag_permits WHERE tag = NEW.tag)
        WHERE tag = NEW.tag;
    END IF;
    -- Configuration owns the task table lock, so this wakeup cannot invert a
    -- running task transaction's lock order. More waiters drain in maintenance.
    PERFORM anclax.wake_task_tag_waiters(NEW.tag);
    RETURN NEW;
END;
$$;
DROP TRIGGER task_tag_limit_changed ON anclax.task_tag_concurrency;
CREATE TRIGGER task_tag_limit_changed AFTER INSERT OR UPDATE OF max_concurrency ON anclax.task_tag_concurrency
FOR EACH ROW EXECUTE FUNCTION anclax.task_tag_limit_changed();

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

-- These nonblocking guards protect every task-driven counter mutation. Unlike
-- tuple locks, they do not follow concurrently updated row versions. Holding
-- guards across several tasks is safe because no path waits for another guard.
CREATE OR REPLACE FUNCTION anclax.release_task_tag_permits(p_task_id INT) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    v_tag TEXT;
    released INT;
BEGIN
    FOR v_tag IN SELECT tag FROM anclax.task_tag_permits WHERE task_id = p_task_id GROUP BY tag ORDER BY tag LOOP
        IF NOT pg_try_advisory_xact_lock(hashtextextended('anclax:task-tag:' || v_tag, 0)) THEN
            RAISE EXCEPTION 'task tag busy: %', v_tag USING ERRCODE = '55P03';
        END IF;
    END LOOP;
    FOR v_tag IN SELECT tag FROM anclax.task_tag_permits WHERE task_id = p_task_id GROUP BY tag ORDER BY tag LOOP
        DELETE FROM anclax.task_tag_permits WHERE task_id = p_task_id AND tag = v_tag;
        GET DIAGNOSTICS released = ROW_COUNT;
        UPDATE anclax.task_tag_concurrency SET in_use = in_use - released WHERE tag = v_tag;
    END LOOP;
    -- Do not lock other task rows while holding tag guards. Available waiters
    -- are discovered by the indexed maintenance query below.
END;
$$;

CREATE OR REPLACE FUNCTION anclax.try_admit_task_tags(v_task_id INT) RETURNS BOOLEAN LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    task_version BIGINT;
    task_type TEXT;
    snapshot_tags TEXT[];
    wanted TEXT[];
    all_tags TEXT[];
    v_tag TEXT;
    blocked_tag TEXT;
    retry_delay INTERVAL := interval '100 milliseconds';
BEGIN
    IF current_setting('transaction_isolation') NOT IN ('read committed', 'read uncommitted') THEN
        RAISE EXCEPTION 'task admission requires READ COMMITTED isolation' USING ERRCODE = '0A000';
    END IF;
    SELECT t.lease_version, t.spec->>'type' INTO task_version, task_type
    FROM anclax.tasks t WHERE t.id = v_task_id FOR UPDATE;
    SELECT COALESCE(array_agg(tt.tag ORDER BY tt.tag), ARRAY[]::text[]) INTO snapshot_tags
    FROM anclax.task_tags tt WHERE tt.task_id = v_task_id
      AND task_type NOT IN ('broadcastUpdateWorkerRuntimeConfig', 'applyWorkerRuntimeConfigToWorker', 'broadcastCancelTask', 'cancelTaskOnWorker', 'broadcastPauseTask', 'pauseTaskOnWorker');
    SELECT COALESCE(array_agg(c.tag ORDER BY c.tag), ARRAY[]::text[]) INTO wanted
    FROM anclax.task_tag_concurrency c WHERE c.tag = ANY(snapshot_tags) AND c.max_concurrency IS NOT NULL;
    SELECT COALESCE(array_agg(tag ORDER BY tag), ARRAY[]::text[]) INTO all_tags FROM (
        SELECT unnest(wanted) AS tag UNION SELECT p.tag FROM anclax.task_tag_permits p WHERE p.task_id = v_task_id
    ) tags;
    IF cardinality(all_tags) = 0 THEN
        RETURN TRUE;
    END IF;
    -- An exception subtransaction releases only this candidate's acquisitions
    -- and mutations. Earlier successfully admitted tasks retain their guards.
    BEGIN
        FOREACH v_tag IN ARRAY all_tags LOOP
            IF NOT pg_try_advisory_xact_lock(hashtextextended('anclax:task-tag:' || v_tag, 0)) THEN
                blocked_tag := v_tag;
                RAISE EXCEPTION 'task tag busy' USING ERRCODE = '55P03';
            END IF;
        END LOOP;
        PERFORM anclax.release_task_tag_permits(v_task_id);
        SELECT c.tag INTO blocked_tag FROM anclax.task_tag_concurrency c
        WHERE c.tag = ANY(wanted) AND c.in_use >= c.max_concurrency ORDER BY c.tag LIMIT 1;
        IF blocked_tag IS NOT NULL THEN
            retry_delay := interval '5 seconds';
            RAISE EXCEPTION 'task tag full' USING ERRCODE = 'P0T01';
        END IF;
        INSERT INTO anclax.task_tag_permits (task_id, lease_version, tag)
        SELECT v_task_id, task_version + 1, unnest(wanted);
        UPDATE anclax.task_tag_concurrency SET in_use = in_use + 1 WHERE tag = ANY(wanted);
        RETURN TRUE;
    EXCEPTION WHEN lock_not_available OR SQLSTATE 'P0T01' THEN
        -- Variables survive rollback, unlike the candidate's guards/permits.
    END;
    UPDATE anclax.tasks SET concurrency_wait_tag = blocked_tag,
        concurrency_retry_at = statement_timestamp() + retry_delay WHERE id = v_task_id;
    RETURN FALSE;
END;
$$;

CREATE OR REPLACE FUNCTION anclax.maintain_task_concurrency(p_legacy_ttl_ms BIGINT) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    expired_task RECORD;
BEGIN
    FOR expired_task IN
        WITH expired AS (
            SELECT id FROM anclax.tasks WHERE lease_expires_at <= statement_timestamp()
            ORDER BY lease_expires_at, id LIMIT 64
        ), legacy AS (
            SELECT id FROM anclax.tasks
            WHERE lease_expires_at IS NULL AND locked_at < statement_timestamp() - p_legacy_ttl_ms * interval '1 millisecond'
            ORDER BY locked_at, id LIMIT 64
        )
        SELECT t.id FROM anclax.tasks t
        WHERE t.id IN (SELECT id FROM expired UNION ALL SELECT id FROM legacy)
          AND (t.lease_expires_at <= statement_timestamp() OR
              (t.lease_expires_at IS NULL AND t.locked_at < statement_timestamp() - p_legacy_ttl_ms * interval '1 millisecond'))
        FOR UPDATE OF t SKIP LOCKED
    LOOP
        BEGIN
            UPDATE anclax.tasks SET locked_at = NULL, worker_id = NULL,
                lease_expires_at = NULL, lease_duration_ms = NULL WHERE id = expired_task.id;
        EXCEPTION WHEN lock_not_available THEN
            -- The release trigger couldn't acquire every guard; keep the old
            -- lease/permits intact and retry on the next bounded sweep.
        END;
    END LOOP;
    -- Start from available configured tags, then use the per-tag waiting index.
    -- Capacity release is visible here immediately, regardless of the fallback
    -- retry hint. Full tags never put their backlog back into the ready index.
    WITH ready AS MATERIALIZED (
        SELECT w.id FROM anclax.task_tag_concurrency c
        CROSS JOIN LATERAL (
            SELECT t.id FROM anclax.tasks t
            WHERE t.concurrency_wait_tag = c.tag AND t.status = 'pending'
              AND COALESCE(t.started_at, '-infinity'::timestamptz) <= statement_timestamp()
            ORDER BY COALESCE(t.started_at, '-infinity'::timestamptz), t.priority DESC, t.created_at, t.id
            LIMIT CASE WHEN c.max_concurrency IS NULL THEN 64 ELSE LEAST(64, GREATEST(0, c.max_concurrency - c.in_use)) END
            FOR UPDATE OF t SKIP LOCKED
        ) w
        WHERE c.max_concurrency IS NULL OR c.in_use < c.max_concurrency
        LIMIT 64
    )
    UPDATE anclax.tasks t SET concurrency_wait_tag = NULL, concurrency_retry_at = NULL
    FROM ready r WHERE t.id = r.id;
END;
$$;

COMMIT;
