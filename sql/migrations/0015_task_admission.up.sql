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

-- Configuration remains a rare, serialized control-plane operation. Task paths
-- never update a shared tag counter. These views retain read-side diagnostics.
DROP TRIGGER task_tag_limit_changed ON anclax.task_tag_concurrency;
DROP TRIGGER classify_task_tag_wait ON anclax.tasks;
DROP FUNCTION anclax.classify_task_tag_wait();
DROP FUNCTION anclax.wake_task_tag_waiters(TEXT, INT);
ALTER TABLE anclax.task_tags DROP CONSTRAINT task_tags_tag_fkey;
ALTER TABLE anclax.task_tag_permits RENAME TO task_tag_permits_v14;
ALTER TABLE anclax.task_tag_concurrency RENAME TO task_tag_limits;
ALTER TABLE anclax.task_tag_limits DROP COLUMN in_use;

CREATE TABLE anclax.task_tag_slots (
    tag TEXT NOT NULL REFERENCES anclax.task_tag_limits(tag) ON DELETE CASCADE,
    slot_no INT NOT NULL CHECK (slot_no > 0),
    retired BOOLEAN NOT NULL DEFAULT FALSE,
    task_id INT REFERENCES anclax.tasks(id),
    lease_version BIGINT,
    PRIMARY KEY (tag, slot_no),
    CHECK ((task_id IS NULL) = (lease_version IS NULL))
);
CREATE UNIQUE INDEX idx_task_tag_slots_owner ON anclax.task_tag_slots (task_id, tag)
    WHERE task_id IS NOT NULL;
CREATE INDEX idx_task_tag_slots_free ON anclax.task_tag_slots (tag, slot_no)
    WHERE task_id IS NULL AND NOT retired;
CREATE INDEX idx_task_tag_slots_overflow ON anclax.task_tag_slots (tag)
    WHERE task_id IS NOT NULL AND retired;

-- Pack existing owners before free slots. Overflow survives a newly imposed
-- lower quota, but is never an allocatable slot.
WITH owners AS (
    SELECT p.tag, p.task_id, p.lease_version,
           row_number() OVER (PARTITION BY p.tag ORDER BY p.task_id)::int AS slot_no
    FROM anclax.task_tag_permits_v14 p JOIN anclax.task_tag_limits c USING(tag)
    WHERE c.max_concurrency IS NOT NULL
), sizes AS (
    SELECT c.tag, c.max_concurrency, GREATEST(c.max_concurrency,
        (SELECT count(*)::int FROM owners o WHERE o.tag=c.tag)) AS n
    FROM anclax.task_tag_limits c WHERE c.max_concurrency IS NOT NULL
)
INSERT INTO anclax.task_tag_slots(tag,slot_no,retired,task_id,lease_version)
SELECT c.tag,seq.slot_no,seq.slot_no>c.max_concurrency,o.task_id,o.lease_version
FROM sizes c CROSS JOIN LATERAL generate_series(1,c.n) AS seq(slot_no)
LEFT JOIN owners o ON o.tag=c.tag AND o.slot_no=seq.slot_no;
DROP TABLE anclax.task_tag_permits_v14;
DELETE FROM anclax.task_tag_limits WHERE max_concurrency IS NULL;

CREATE VIEW anclax.task_tag_permits AS
SELECT task_id, lease_version, tag FROM anclax.task_tag_slots WHERE task_id IS NOT NULL;
CREATE VIEW anclax.task_tag_concurrency AS
SELECT c.tag, c.max_concurrency,
    (SELECT count(*)::int FROM anclax.task_tag_slots s WHERE s.tag=c.tag AND s.task_id IS NOT NULL) AS in_use
FROM anclax.task_tag_limits c;

-- Availability is read from slots, never copied into persistent task state.
DROP INDEX anclax.idx_tasks_pending_priority_created;
DROP INDEX anclax.idx_tasks_pending_weight_created;
ALTER TABLE anclax.tasks DROP COLUMN concurrency_wait_tag, DROP COLUMN concurrency_retry_at;
CREATE INDEX idx_tasks_pending_priority_created ON anclax.tasks(priority DESC,created_at,id)
    WHERE status='pending';
CREATE INDEX idx_tasks_pending_weight_created ON anclax.tasks(weight DESC,created_at,id)
    WHERE status='pending' AND priority=0;

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

CREATE FUNCTION anclax.lock_task_tag_configuration() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('transaction_isolation') NOT IN ('read committed', 'read uncommitted') THEN
        RAISE EXCEPTION 'task tag configuration requires READ COMMITTED isolation' USING ERRCODE = '0A000';
    END IF;
    LOCK TABLE anclax.tasks IN EXCLUSIVE MODE;
    RETURN NULL;
END;
$$;

CREATE TRIGGER lock_task_tag_configuration BEFORE INSERT OR UPDATE OR DELETE
ON anclax.task_tag_limits FOR EACH STATEMENT EXECUTE FUNCTION anclax.lock_task_tag_configuration();

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
CREATE TRIGGER task_tag_limit_changed AFTER INSERT OR UPDATE OF max_concurrency ON anclax.task_tag_limits
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

-- The task row is locked and its attempt fenced by the caller. Free only its
-- own slots. Allocators only mutate free slots under per-slot try guards, so a
-- release never acquires another task's slot or a shared tag lock.
CREATE OR REPLACE FUNCTION anclax.release_task_tag_permits(p_task_id INT) RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    UPDATE anclax.task_tag_slots SET task_id=NULL,lease_version=NULL WHERE task_id=p_task_id;
END;
$$;

CREATE FUNCTION anclax.try_task_tag_slot(p_tag TEXT, p_task_id INT, p_version BIGINT) RETURNS BOOLEAN
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    candidate INT;
BEGIN
    FOR candidate IN
        SELECT slot_no FROM anclax.task_tag_slots
        WHERE tag=p_tag AND task_id IS NULL AND NOT retired ORDER BY slot_no
    LOOP
        BEGIN
            IF NOT pg_try_advisory_xact_lock(hashtextextended('anclax:task-slot:' || p_tag || ':' || candidate,0)) THEN
                CONTINUE;
            END IF;
            -- A separate statement gets a fresh READ COMMITTED snapshot after
            -- acquiring the guard. A stale free-slot cursor must not follow an
            -- updated occupied tuple or retain a useless guard until commit.
            UPDATE anclax.task_tag_slots SET task_id=p_task_id,lease_version=p_version
            WHERE tag=p_tag AND slot_no=candidate AND task_id IS NULL AND NOT retired;
            IF FOUND THEN
                RETURN TRUE;
            END IF;
            RAISE EXCEPTION 'slot changed' USING ERRCODE='P0S01';
        EXCEPTION WHEN SQLSTATE 'P0S01' THEN
            NULL;
        END;
    END LOOP;
    RETURN FALSE;
END;
$$;

CREATE OR REPLACE FUNCTION anclax.try_admit_task_tags(v_task_id INT) RETURNS BOOLEAN LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    task_version BIGINT;
    task_type TEXT;
    wanted RECORD;
BEGIN
    IF current_setting('transaction_isolation') NOT IN ('read committed','read uncommitted') THEN
        RAISE EXCEPTION 'task admission requires READ COMMITTED isolation' USING ERRCODE='0A000';
    END IF;
    SELECT lease_version,spec->>'type' INTO task_version,task_type
    FROM anclax.tasks WHERE id=v_task_id FOR UPDATE;
    -- One candidate is an allocation unit, including reclaim of its expired
    -- attempt. Failure rolls back every partial slot and advisory acquisition.
    BEGIN
        PERFORM anclax.release_task_tag_permits(v_task_id);
        FOR wanted IN
            SELECT c.tag,c.max_concurrency FROM anclax.task_tag_limits c
            JOIN anclax.task_tags tt ON tt.tag=c.tag
            WHERE tt.task_id=v_task_id AND c.max_concurrency IS NOT NULL
              AND task_type NOT IN ('broadcastUpdateWorkerRuntimeConfig','applyWorkerRuntimeConfigToWorker','broadcastCancelTask','cancelTaskOnWorker','broadcastPauseTask','pauseTaskOnWorker')
            ORDER BY c.tag
        LOOP
            -- Only the exceptional shrink/backfill overflow period needs an
            -- admission guard: releases stay independent, and a fresh count
            -- prevents replacing active slots while excess owners still run.
            IF EXISTS (SELECT 1 FROM anclax.task_tag_slots WHERE tag=wanted.tag AND retired AND task_id IS NOT NULL) THEN
                IF NOT pg_try_advisory_xact_lock(hashtextextended('anclax:task-overflow:' || wanted.tag,0)) THEN
                    RAISE EXCEPTION 'overflow allocation busy' USING ERRCODE='P0S02';
                END IF;
                IF (SELECT count(*) FROM anclax.task_tag_slots WHERE tag=wanted.tag AND task_id IS NOT NULL) >= wanted.max_concurrency THEN
                    RAISE EXCEPTION 'overflow quota full' USING ERRCODE='P0S02';
                END IF;
            END IF;
            IF NOT anclax.try_task_tag_slot(wanted.tag,v_task_id,task_version+1) THEN
                RAISE EXCEPTION 'no free slot' USING ERRCODE='P0S02';
            END IF;
        END LOOP;
        RETURN TRUE;
    EXCEPTION WHEN SQLSTATE 'P0S02' THEN
        RETURN FALSE;
    END;
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
        UPDATE anclax.tasks SET locked_at = NULL, worker_id = NULL,
                lease_expires_at = NULL, lease_duration_ms = NULL WHERE id = expired_task.id;
    END LOOP;
END;
$$;

COMMIT;
