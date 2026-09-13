BEGIN;

-- A NULL limit is ordinary, unlimited metadata. Track these tags too, so
-- enabling a limit includes attempts that were admitted before it was set.
CREATE TABLE anclax.task_tag_concurrency (
    tag TEXT PRIMARY KEY,
    max_concurrency INT CHECK (max_concurrency >= 0),
    in_use INT NOT NULL DEFAULT 0 CHECK (in_use >= 0)
);

CREATE TABLE anclax.task_tags (
    task_id INT NOT NULL REFERENCES anclax.tasks(id) ON DELETE CASCADE,
    tag TEXT NOT NULL REFERENCES anclax.task_tag_concurrency(tag),
    PRIMARY KEY (task_id, tag)
);
CREATE INDEX idx_task_tags_tag ON anclax.task_tags (tag, task_id);

-- The task row is the expiry authority. Renewing a lease never writes the
-- shared tag counters. Permits retain the tags of the admitted attempt even
-- when attributes or the task's lease version change (pause/resume).
CREATE TABLE anclax.task_tag_permits (
    task_id INT NOT NULL REFERENCES anclax.tasks(id) ON DELETE CASCADE,
    lease_version BIGINT NOT NULL,
    tag TEXT NOT NULL REFERENCES anclax.task_tag_concurrency(tag),
    PRIMARY KEY (task_id, lease_version, tag)
);
CREATE INDEX idx_task_tag_permits_tag ON anclax.task_tag_permits (tag, task_id);

ALTER TABLE anclax.tasks
    ADD COLUMN lease_expires_at TIMESTAMPTZ,
    ADD COLUMN lease_duration_ms BIGINT CHECK (lease_duration_ms > 0),
    ADD COLUMN concurrency_wait_tag TEXT,
    ADD COLUMN concurrency_retry_at TIMESTAMPTZ;

DROP INDEX anclax.idx_tasks_pending_priority_created;
DROP INDEX anclax.idx_tasks_pending_weight_created;
CREATE INDEX idx_tasks_pending_priority_created
    ON anclax.tasks (priority DESC, created_at, id)
    WHERE status = 'pending' AND concurrency_wait_tag IS NULL;
CREATE INDEX idx_tasks_pending_weight_created
    ON anclax.tasks (weight DESC, created_at, id)
    WHERE status = 'pending' AND priority = 0 AND concurrency_wait_tag IS NULL;
CREATE INDEX idx_tasks_concurrency_wait_tag
    ON anclax.tasks (concurrency_wait_tag, COALESCE(started_at, '-infinity'::timestamptz), priority DESC, created_at, id)
    WHERE status = 'pending' AND concurrency_wait_tag IS NOT NULL;
CREATE INDEX idx_tasks_concurrency_retry
    ON anclax.tasks (concurrency_retry_at, id)
    WHERE status = 'pending' AND concurrency_wait_tag IS NOT NULL;
CREATE INDEX idx_tasks_lease_expiry
    ON anclax.tasks (lease_expires_at, id) WHERE lease_expires_at IS NOT NULL;
CREATE INDEX idx_tasks_legacy_lease
    ON anclax.tasks (locked_at, id) WHERE lease_expires_at IS NULL AND locked_at IS NOT NULL;

INSERT INTO anclax.task_tag_concurrency (tag)
SELECT DISTINCT tag
FROM anclax.tasks t
CROSS JOIN LATERAL jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'tags', 'null'::jsonb), '[]'::jsonb)) AS tags(tag);
INSERT INTO anclax.task_tags (task_id, tag)
SELECT DISTINCT t.id, tag
FROM anclax.tasks t
CROSS JOIN LATERAL jsonb_array_elements_text(COALESCE(NULLIF(t.attributes->'tags', 'null'::jsonb), '[]'::jsonb)) AS tags(tag);

-- Legacy leases have no recorded TTL. Keep counting them until a new worker
-- reclaims/reaps them using the legacy locked_at + configured TTL rule. Old
-- workers must be stopped for the upgrade; new leases record their own TTL.
INSERT INTO anclax.task_tag_permits (task_id, lease_version, tag)
SELECT t.id, t.lease_version, tt.tag
FROM anclax.tasks t JOIN anclax.task_tags tt ON tt.task_id = t.id
WHERE t.locked_at IS NOT NULL
  AND t.spec->>'type' NOT IN ('broadcastUpdateWorkerRuntimeConfig', 'applyWorkerRuntimeConfigToWorker', 'broadcastCancelTask', 'cancelTaskOnWorker', 'broadcastPauseTask', 'pauseTaskOnWorker');
UPDATE anclax.task_tag_concurrency c SET in_use = p.n
FROM (SELECT tag, count(*)::int AS n FROM anclax.task_tag_permits GROUP BY tag) p
WHERE c.tag = p.tag;

CREATE FUNCTION anclax.sync_task_tags() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.attributes->'tags' IS NOT DISTINCT FROM OLD.attributes->'tags' THEN
        RETURN NEW;
    END IF;
    INSERT INTO anclax.task_tag_concurrency (tag)
    SELECT DISTINCT tag
    FROM jsonb_array_elements_text(COALESCE(NULLIF(NEW.attributes->'tags', 'null'::jsonb), '[]'::jsonb)) AS tags(tag)
    ORDER BY tag ON CONFLICT DO NOTHING;
    DELETE FROM anclax.task_tags WHERE task_id = NEW.id;
    INSERT INTO anclax.task_tags (task_id, tag)
    SELECT DISTINCT NEW.id, tag
    FROM jsonb_array_elements_text(COALESCE(NULLIF(NEW.attributes->'tags', 'null'::jsonb), '[]'::jsonb)) AS tags(tag);
    RETURN NEW;
END;
$$;
CREATE TRIGGER sync_task_tags AFTER INSERT OR UPDATE OF attributes ON anclax.tasks
FOR EACH ROW EXECUTE FUNCTION anclax.sync_task_tags();

-- This is only an enqueue-time hint. Admission always rechecks under locks.
-- A lost wakeup is covered by the indexed, bounded retry sweep.
CREATE FUNCTION anclax.classify_task_tag_wait() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.attributes->'tags' IS NOT DISTINCT FROM OLD.attributes->'tags'
       AND NEW.spec->>'type' IS NOT DISTINCT FROM OLD.spec->>'type' THEN
        RETURN NEW;
    END IF;
    NEW.concurrency_wait_tag := NULL;
    NEW.concurrency_retry_at := NULL;
    IF NEW.status = 'pending' AND NEW.locked_at IS NULL AND NEW.spec->>'type' NOT IN ('broadcastUpdateWorkerRuntimeConfig', 'applyWorkerRuntimeConfigToWorker', 'broadcastCancelTask', 'cancelTaskOnWorker', 'broadcastPauseTask', 'pauseTaskOnWorker') THEN
        SELECT c.tag INTO NEW.concurrency_wait_tag
        FROM anclax.task_tag_concurrency c
        JOIN jsonb_array_elements_text(COALESCE(NULLIF(NEW.attributes->'tags', 'null'::jsonb), '[]'::jsonb)) AS tags(tag) ON tags.tag = c.tag
        WHERE c.in_use >= c.max_concurrency ORDER BY c.tag LIMIT 1;
        IF NEW.concurrency_wait_tag IS NOT NULL THEN
            NEW.concurrency_retry_at := statement_timestamp() + interval '5 seconds';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER classify_task_tag_wait BEFORE INSERT OR UPDATE OF attributes, spec ON anclax.tasks
FOR EACH ROW EXECUTE FUNCTION anclax.classify_task_tag_wait();

-- Callers hold the tag row. SKIP LOCKED on task rows avoids reversing the
-- task -> sorted tags lock order used by admission and permit release.
CREATE FUNCTION anclax.wake_task_tag_waiters(p_tag TEXT, p_skip_task_id INT DEFAULT NULL) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    available INT;
BEGIN
    SELECT CASE WHEN max_concurrency IS NULL THEN 64 ELSE LEAST(64, GREATEST(max_concurrency - in_use, 0)) END
    INTO available FROM anclax.task_tag_concurrency WHERE tag = p_tag;
    WITH waiters AS (
        SELECT id FROM anclax.tasks
        WHERE concurrency_wait_tag = p_tag AND status = 'pending'
          AND id IS DISTINCT FROM p_skip_task_id
          AND COALESCE(started_at, '-infinity'::timestamptz) <= statement_timestamp()
        ORDER BY COALESCE(started_at, '-infinity'::timestamptz), priority DESC, created_at, id
        LIMIT available FOR UPDATE SKIP LOCKED
    )
    UPDATE anclax.tasks t SET concurrency_wait_tag = NULL, concurrency_retry_at = NULL
    FROM waiters w WHERE t.id = w.id;
END;
$$;

CREATE FUNCTION anclax.release_task_tag_permits(p_task_id INT) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    c RECORD;
    released INT;
BEGIN
    -- The task row must already be locked. Never release by a caller's tags:
    -- the durable permit snapshot, including an invalidated resume lease,
    -- is the authoritative set to release.
    FOR c IN
        SELECT s.tag FROM anclax.task_tag_concurrency s
        WHERE s.tag IN (SELECT p.tag FROM anclax.task_tag_permits p WHERE p.task_id = p_task_id)
        ORDER BY s.tag FOR NO KEY UPDATE
    LOOP
        DELETE FROM anclax.task_tag_permits WHERE task_id = p_task_id AND tag = c.tag;
        GET DIAGNOSTICS released = ROW_COUNT;
        UPDATE anclax.task_tag_concurrency SET in_use = in_use - released WHERE tag = c.tag;
        PERFORM anclax.wake_task_tag_waiters(c.tag, p_task_id);
    END LOOP;
END;
$$;

CREATE FUNCTION anclax.release_task_tags_on_unlock() RETURNS TRIGGER LANGUAGE plpgsql AS $$
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
CREATE TRIGGER release_task_tags_on_unlock AFTER UPDATE OF locked_at ON anclax.tasks
FOR EACH ROW EXECUTE FUNCTION anclax.release_task_tags_on_unlock();
CREATE TRIGGER release_task_tags_on_delete BEFORE DELETE ON anclax.tasks
FOR EACH ROW EXECUTE FUNCTION anclax.release_task_tags_on_unlock();

-- Candidates are locked, ordered and bounded by the claim query. This
-- VOLATILE function reads fresh counter values after acquiring the locks.
-- The candidate CTE streams locks until one task is admitted; the UPDATE installs that
-- attempt's task lease in the very same transaction.
CREATE FUNCTION anclax.try_admit_task_tags(v_task_id INT) RETURNS BOOLEAN LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    task_version BIGINT;
    task_type TEXT;
    wanted TEXT[];
    all_tags TEXT[];
    locked_tags TEXT[];
    blocked_tag TEXT;
    retry_delay INTERVAL;
BEGIN
    SELECT t.lease_version, t.spec->>'type' INTO task_version, task_type
    FROM anclax.tasks t WHERE t.id = v_task_id FOR UPDATE;
    SELECT COALESCE(array_agg(tt.tag ORDER BY tt.tag), ARRAY[]::text[]) INTO wanted
    FROM anclax.task_tags tt WHERE tt.task_id = v_task_id
      AND task_type NOT IN ('broadcastUpdateWorkerRuntimeConfig', 'applyWorkerRuntimeConfigToWorker', 'broadcastCancelTask', 'cancelTaskOnWorker', 'broadcastPauseTask', 'pauseTaskOnWorker');
    SELECT COALESCE(array_agg(tag ORDER BY tag), ARRAY[]::text[]) INTO all_tags FROM (
        SELECT unnest(wanted) AS tag UNION SELECT p.tag FROM anclax.task_tag_permits p WHERE p.task_id = v_task_id
    ) tags;
    IF cardinality(all_tags) = 0 THEN
        RETURN TRUE;
    END IF;

    SELECT COALESCE(array_agg(tag), ARRAY[]::text[]) INTO locked_tags FROM (
        SELECT c.tag FROM anclax.task_tag_concurrency c WHERE c.tag = ANY(all_tags)
        ORDER BY c.tag FOR NO KEY UPDATE SKIP LOCKED
    ) locked;
    SELECT tag INTO blocked_tag FROM unnest(all_tags) AS tags(tag)
    WHERE NOT (tag = ANY(locked_tags)) ORDER BY tag LIMIT 1;
    retry_delay := interval '100 milliseconds';
    IF blocked_tag IS NULL THEN
        -- Any previous attempt was already found expired by the claim
        -- query. Task + all old/new tag rows are locked before reclaim.
        PERFORM anclax.release_task_tag_permits(v_task_id);
        SELECT c.tag INTO blocked_tag FROM anclax.task_tag_concurrency c
        WHERE c.tag = ANY(wanted) AND c.in_use >= c.max_concurrency
        ORDER BY c.tag LIMIT 1;
        retry_delay := interval '5 seconds';
    END IF;
    IF blocked_tag IS NOT NULL THEN
        UPDATE anclax.tasks t SET concurrency_wait_tag = blocked_tag,
            concurrency_retry_at = statement_timestamp() + retry_delay
        WHERE t.id = v_task_id;
        RETURN FALSE;
    END IF;

    INSERT INTO anclax.task_tag_permits (task_id, lease_version, tag)
    SELECT v_task_id, task_version + 1, unnest(wanted);
    UPDATE anclax.task_tag_concurrency SET in_use = in_use + 1 WHERE tag = ANY(wanted);
    RETURN TRUE;
END;
$$;

CREATE FUNCTION anclax.task_tag_limit_changed() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.max_concurrency IS NULL OR NEW.max_concurrency > OLD.max_concurrency THEN
        PERFORM anclax.wake_task_tag_waiters(NEW.tag);
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER task_tag_limit_changed AFTER UPDATE OF max_concurrency ON anclax.task_tag_concurrency
FOR EACH ROW EXECUTE FUNCTION anclax.task_tag_limit_changed();

CREATE FUNCTION anclax.maintain_task_concurrency(p_legacy_ttl_ms BIGINT) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE
    expired_task RECORD;
    held_tags TEXT[];
BEGIN
    -- Bounded maintenance runs separately from claims, so it never holds
    -- candidate locks while searching for expired attempts or retry waiters.
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
        -- A sweep retains locks for several tasks. Try-lock their tag rows
        -- so concurrent sweeps cannot form a cycle across different tasks.
        SELECT COALESCE(array_agg(tag), ARRAY[]::text[]) INTO held_tags FROM (
            SELECT c.tag FROM anclax.task_tag_concurrency c
            WHERE c.tag IN (SELECT p.tag FROM anclax.task_tag_permits p WHERE p.task_id = expired_task.id)
            ORDER BY c.tag FOR NO KEY UPDATE SKIP LOCKED
        ) locked;
        IF NOT EXISTS (SELECT 1 FROM anclax.task_tag_permits p WHERE p.task_id = expired_task.id AND NOT (p.tag = ANY(held_tags))) THEN
            UPDATE anclax.tasks SET locked_at = NULL, worker_id = NULL,
                lease_expires_at = NULL, lease_duration_ms = NULL WHERE id = expired_task.id;
        END IF;
    END LOOP;
    WITH retry AS (
        SELECT id FROM anclax.tasks
        WHERE status = 'pending' AND concurrency_wait_tag IS NOT NULL
          AND concurrency_retry_at <= statement_timestamp()
        ORDER BY concurrency_retry_at, id LIMIT 64 FOR UPDATE SKIP LOCKED
    )
    UPDATE anclax.tasks t SET
        concurrency_wait_tag = CASE WHEN c.max_concurrency IS NULL OR c.in_use < c.max_concurrency THEN NULL ELSE t.concurrency_wait_tag END,
        concurrency_retry_at = CASE WHEN c.max_concurrency IS NULL OR c.in_use < c.max_concurrency THEN NULL ELSE statement_timestamp() + interval '5 seconds' END
    FROM retry r, anclax.task_tag_concurrency c
    WHERE t.id = r.id AND c.tag = t.concurrency_wait_tag;
END;
$$;

COMMIT;
