BEGIN;

-- Stop all workers for this protocol change. Ready owns resources but no worker.
ALTER TABLE anclax.tasks ADD COLUMN ready_expires_at TIMESTAMPTZ;
ALTER TABLE anclax.tasks ADD CONSTRAINT tasks_ready_reservation_shape CHECK (
    (status='ready')=(ready_expires_at IS NOT NULL)
    AND (status<>'ready' OR (locked_at IS NULL AND worker_id IS NULL))
);
ALTER TABLE anclax.workers ADD COLUMN prefetch_capacity INT NOT NULL DEFAULT 0,
    ADD COLUMN prefetch_strict_percentage INT NOT NULL DEFAULT 100,
    ADD COLUMN prefetch_heartbeat_ttl_ms BIGINT NOT NULL DEFAULT 9000;
CREATE FUNCTION anclax.is_system_task(task_type TEXT) RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT task_type IN ('prefetchTasks', 'broadcastUpdateWorkerRuntimeConfig',
      'applyWorkerRuntimeConfigToWorker', 'broadcastCancelTask', 'cancelTaskOnWorker',
      'broadcastPauseTask', 'pauseTaskOnWorker');
$$;
CREATE TABLE anclax.task_admission_groups (
    id BIGSERIAL PRIMARY KEY,
    tags TEXT[] NOT NULL,
    labels TEXT[] NOT NULL,
    UNIQUE(tags,labels)
);
ALTER TABLE anclax.tasks ADD COLUMN admission_group_id BIGINT REFERENCES anclax.task_admission_groups(id);
CREATE INDEX idx_tasks_unclassified ON anclax.tasks(id)
    WHERE status='pending' AND admission_group_id IS NULL AND NOT anclax.is_system_task(spec->>'type');
CREATE INDEX idx_tasks_admission_candidates ON anclax.tasks
    (admission_group_id,priority DESC,(CASE WHEN priority=0 THEN weight ELSE 0 END) DESC,created_at,id) WHERE status='pending';
CREATE INDEX idx_tasks_ready_priority ON anclax.tasks(priority DESC,created_at,id) WHERE status='ready';
CREATE INDEX idx_tasks_ready_weight ON anclax.tasks(weight DESC,created_at,id) WHERE status='ready' AND priority=0;
CREATE INDEX idx_tasks_ready_expiry ON anclax.tasks(ready_expires_at,id) WHERE status='ready';
CREATE INDEX idx_tasks_system_pending ON anclax.tasks((COALESCE(started_at,created_at)),id)
    WHERE status IN ('pending','running') AND anclax.is_system_task(spec->>'type');
CREATE INDEX idx_tasks_serial_ready ON anclax.tasks(serial_key,ready_expires_at) WHERE status='ready' AND serial_key IS NOT NULL;
DROP INDEX anclax.idx_tasks_serial_pending_head;
CREATE INDEX idx_tasks_serial_pending_head ON anclax.tasks
    (serial_key,(serial_id IS NULL),(COALESCE(serial_id,2147483647)),created_at,
     (COALESCE(started_at,'-infinity'::timestamptz)),id)
    WHERE status IN ('pending','ready') AND serial_key IS NOT NULL;

-- Existing groups are read without updating a common counter or locking a hot row.
CREATE FUNCTION anclax.classify_task_admission() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE wanted_tags TEXT[]; wanted_labels TEXT[]; group_id BIGINT;
BEGIN
    IF anclax.is_system_task(NEW.spec->>'type') THEN
        NEW.admission_group_id := NULL;
        RETURN NEW;
    END IF;
    IF NEW.status NOT IN ('pending','ready','running','paused') AND NEW.locked_at IS NULL THEN
        NEW.admission_group_id := NULL;
        RETURN NEW;
    END IF;
    IF TG_OP='UPDATE' AND NEW.admission_group_id IS NOT NULL
       AND NEW.attributes->'tags' IS NOT DISTINCT FROM OLD.attributes->'tags'
       AND NEW.attributes->'labels' IS NOT DISTINCT FROM OLD.attributes->'labels'
       AND NEW.spec->>'type' IS NOT DISTINCT FROM OLD.spec->>'type' THEN
        RETURN NEW;
    END IF;
    SELECT COALESCE(array_agg(DISTINCT x.tag ORDER BY x.tag),'{}'::text[]) INTO wanted_tags
    FROM jsonb_array_elements_text(COALESCE(NULLIF(NEW.attributes->'tags','null'::jsonb),'[]')) x(tag)
    JOIN anclax.task_tag_limits c ON c.tag=x.tag AND c.max_concurrency IS NOT NULL;
    SELECT COALESCE(array_agg(DISTINCT x.label ORDER BY x.label),'{}'::text[]) INTO wanted_labels
    FROM jsonb_array_elements_text(COALESCE(NULLIF(NEW.attributes->'labels','null'::jsonb),'[]')) x(label);
    SELECT id INTO group_id FROM anclax.task_admission_groups WHERE tags=wanted_tags AND labels=wanted_labels;
    IF group_id IS NULL THEN
        -- Never wait on another transaction creating a different new group:
        -- opposite multi-task insertion orders must not form a unique-key cycle.
        -- The singleton scheduler classifies deferred rows through a small index.
        IF NOT pg_try_advisory_xact_lock(hashtextextended('anclax:admission-group-create',0)) THEN
            NEW.admission_group_id:=NULL;
            RETURN NEW;
        END IF;
        INSERT INTO anclax.task_admission_groups(tags,labels) VALUES(wanted_tags,wanted_labels)
        ON CONFLICT DO NOTHING RETURNING id INTO group_id;
        IF group_id IS NULL THEN
            SELECT id INTO group_id FROM anclax.task_admission_groups WHERE tags=wanted_tags AND labels=wanted_labels;
        END IF;
    END IF;
    NEW.admission_group_id := group_id;
    RETURN NEW;
END;
$$;
CREATE TRIGGER classify_task_admission BEFORE INSERT OR UPDATE OF attributes,spec,status,locked_at,admission_group_id
ON anclax.tasks FOR EACH ROW EXECUTE FUNCTION anclax.classify_task_admission();
UPDATE anclax.tasks SET admission_group_id=NULL WHERE status IN ('pending','running','paused') OR locked_at IS NOT NULL;

-- A ready reservation is invalidated under the task row lock before mutable
-- scheduling attributes change. Running attempts keep their original snapshot.
CREATE FUNCTION anclax.invalidate_task_reservation() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    -- Normalize before deciding whether this is a valid reservation transfer.
    IF NEW.status='running' AND NEW.locked_at IS NULL THEN NEW.status := 'pending'; END IF;
    IF OLD.status='ready' THEN
        IF NEW.status='ready' AND (NEW.attributes IS DISTINCT FROM OLD.attributes OR NEW.spec IS DISTINCT FROM OLD.spec
           OR NEW.started_at IS DISTINCT FROM OLD.started_at OR NEW.serial_key IS DISTINCT FROM OLD.serial_key
           OR NEW.serial_id IS DISTINCT FROM OLD.serial_id OR NEW.priority IS DISTINCT FROM OLD.priority
           OR NEW.weight IS DISTINCT FROM OLD.weight) THEN
            NEW.status := 'pending';
        END IF;
        IF NEW.status <> 'ready' THEN
            NEW.ready_expires_at := NULL;
            IF NEW.status <> 'running' THEN
                PERFORM anclax.release_task_tag_permits(OLD.id);
                NEW.lease_version := OLD.lease_version+1;
                NEW.lease_tags := '{}';
            END IF;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER aaa_invalidate_task_reservation BEFORE UPDATE ON anclax.tasks
FOR EACH ROW EXECUTE FUNCTION anclax.invalidate_task_reservation();

DROP TRIGGER snapshot_task_lease_tags ON anclax.tasks;
CREATE OR REPLACE FUNCTION anclax.snapshot_task_lease_tags() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.locked_at IS NULL AND NEW.status<>'ready' THEN
        NEW.lease_tags := '{}';
    ELSIF TG_OP='INSERT' OR (NEW.status='ready' AND OLD.status<>'ready')
       OR (NEW.locked_at IS NOT NULL AND OLD.status<>'ready' AND
           (OLD.locked_at IS NULL OR (NEW.lease_version>OLD.lease_version AND NEW.locked_at IS DISTINCT FROM OLD.locked_at))) THEN
        SELECT COALESCE(array_agg(DISTINCT tag ORDER BY tag),'{}'::text[]) INTO NEW.lease_tags
        FROM jsonb_array_elements_text(COALESCE(NULLIF(NEW.attributes->'tags','null'::jsonb),'[]')) tags(tag)
        WHERE NOT anclax.is_system_task(NEW.spec->>'type');
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER snapshot_task_lease_tags BEFORE INSERT OR UPDATE OF locked_at,status ON anclax.tasks
FOR EACH ROW EXECUTE FUNCTION anclax.snapshot_task_lease_tags();

CREATE OR REPLACE FUNCTION anclax.sync_task_tags() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    needs_membership BOOLEAN := NEW.status IN ('pending', 'ready', 'running', 'paused') OR NEW.locked_at IS NOT NULL;
BEGIN
    -- Status and lease transitions maintain membership too. Ordinary renewals
    -- return before touching any tag rows, while restoring a historical task
    -- rebuilds membership even when its tags did not change.
    IF TG_OP = 'UPDATE' AND NEW.attributes->'tags' IS NOT DISTINCT FROM OLD.attributes->'tags'
       AND needs_membership = (OLD.status IN ('pending', 'ready', 'running', 'paused') OR OLD.locked_at IS NOT NULL) THEN
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
            FROM anclax.tasks WHERE (locked_at IS NOT NULL OR status='ready') AND NEW.tag=ANY(lease_tags)
        )
        INSERT INTO anclax.task_tag_slots(tag,slot_no,retired,task_id,lease_version)
        SELECT NEW.tag,n,n>NEW.max_concurrency,o.id,o.lease_version
        FROM generate_series(1,GREATEST(NEW.max_concurrency,(SELECT count(*)::int FROM owners))) n
        LEFT JOIN owners o ON o.slot_no=n;
    END IF;
    RETURN NEW;
END;
$$;

-- Configuration already holds the tasks-table barrier. Reclassify on enabling
-- or removing limits; ready owners are included in the existing slot backfill.
CREATE FUNCTION anclax.reclassify_task_admission_groups() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    UPDATE anclax.tasks SET admission_group_id=NULL
    WHERE status IN ('pending','ready','running','paused') AND NOT anclax.is_system_task(spec->>'type');
    RETURN NULL;
END;
$$;
CREATE TRIGGER reclassify_task_admission_groups AFTER INSERT OR DELETE OR UPDATE OF max_concurrency
ON anclax.task_tag_limits FOR EACH STATEMENT EXECUTE FUNCTION anclax.reclassify_task_admission_groups();

-- All entry points share the serial guard. A ready owner blocks later serial
-- tasks even though it has not acquired a worker execution lease.
ALTER FUNCTION anclax.try_admit_task_tags(INT) RENAME TO allocate_task_tag_slots;
CREATE OR REPLACE FUNCTION anclax.try_admit_task_tags(v_task_id INT,p_legacy_ttl_ms BIGINT DEFAULT 9000) RETURNS BOOLEAN LANGUAGE plpgsql VOLATILE AS $$
DECLARE t anclax.tasks;
BEGIN
    SELECT * INTO t FROM anclax.tasks WHERE id=v_task_id FOR UPDATE;
    IF NOT FOUND THEN RETURN FALSE; END IF;
    IF anclax.is_system_task(t.spec->>'type') THEN RETURN TRUE; END IF;
    IF t.status='ready' THEN RETURN t.ready_expires_at>statement_timestamp(); END IF;
    IF t.serial_key IS NOT NULL THEN
        IF NOT pg_try_advisory_xact_lock(hashtextextended('anclax:serial-admit:'||t.serial_key,0)) THEN RETURN FALSE; END IF;
        IF EXISTS(SELECT 1 FROM anclax.tasks a WHERE a.serial_key=t.serial_key AND a.id<>t.id
            AND a.status='ready' AND a.ready_expires_at>statement_timestamp())
           OR EXISTS(SELECT 1 FROM anclax.tasks a WHERE a.serial_key=t.serial_key AND a.id<>t.id
            AND a.locked_at IS NOT NULL
            AND COALESCE(a.lease_expires_at,a.locked_at+p_legacy_ttl_ms*interval '1 millisecond')>statement_timestamp()) THEN
            RETURN FALSE;
        END IF;
        IF EXISTS(SELECT 1 FROM anclax.tasks h WHERE h.serial_key=t.serial_key AND h.status IN ('pending','ready') AND
           ROW(h.serial_id IS NULL,COALESCE(h.serial_id,2147483647),h.created_at,COALESCE(h.started_at,'-infinity'::timestamptz),h.id)
           < ROW(t.serial_id IS NULL,COALESCE(t.serial_id,2147483647),t.created_at,COALESCE(t.started_at,'-infinity'::timestamptz),t.id)) THEN RETURN FALSE; END IF;
    END IF;
    RETURN anclax.allocate_task_tag_slots(v_task_id);
END;
$$;

CREATE OR REPLACE FUNCTION anclax.release_task_tags_on_unlock() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        PERFORM anclax.release_task_tag_permits(OLD.id);
        RETURN OLD;
    END IF;
    IF OLD.locked_at IS NOT NULL AND NEW.locked_at IS NULL AND NEW.status<>'ready' THEN
        PERFORM anclax.release_task_tag_permits(OLD.id);
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION anclax.recover_ready_tasks(p_limit INT) RETURNS INT LANGUAGE plpgsql AS $$
DECLARE n INT;
BEGIN
    WITH expired AS (
        SELECT id FROM anclax.tasks WHERE status='ready' AND ready_expires_at<=statement_timestamp()
        ORDER BY ready_expires_at,id LIMIT p_limit FOR UPDATE SKIP LOCKED
    )
    UPDATE anclax.tasks t SET status='pending',ready_expires_at=NULL,updated_at=statement_timestamp()
    FROM expired e WHERE t.id=e.id;
    GET DIAGNOSTICS n=ROW_COUNT;
    RETURN n;
END;
$$;

CREATE FUNCTION anclax.prefetch_ready_tasks(p_task_id INT,p_worker UUID,p_version BIGINT,
    p_batch INT,p_ready_ttl_ms BIGINT,p_lock_ttl_ms BIGINT) RETURNS INT LANGUAGE plpgsql VOLATILE SET jit=off AS $$
DECLARE target INT; strict_target INT; current_ready INT; current_strict INT; n INT:=0; selected_task RECORD;
    group_cursor BIGINT; total_weight BIGINT; group_names TEXT[]; group_bounds BIGINT[];
    ordered_groups TEXT[]; weighted_labels TEXT[]; preferred_index INT:=1; i INT;
BEGIN
    IF p_batch<1 OR p_batch>256 OR p_ready_ttl_ms<1 THEN RAISE EXCEPTION 'invalid prefetch bounds'; END IF;
    -- Fence the singleton system task in every batch; an old process cannot
    -- keep admitting work after its scheduler lease has been taken over.
    SELECT COALESCE((spec->'payload'->>'admissionCursor')::bigint,0) INTO group_cursor
    FROM anclax.tasks WHERE id=p_task_id AND worker_id=p_worker AND lease_version=p_version
        AND unique_tag='anclax:system:prefetch' AND spec->>'type'='prefetchTasks' AND status IN ('pending','running')
        AND locked_at IS NOT NULL AND lease_expires_at>statement_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN 0; END IF;
    PERFORM anclax.recover_ready_tasks(256);
    PERFORM anclax.maintain_task_concurrency(p_lock_ttl_ms);
    IF EXISTS(SELECT 1 FROM anclax.tasks WHERE status='pending' AND admission_group_id IS NULL
            AND NOT anclax.is_system_task(spec->>'type'))
       AND pg_try_advisory_xact_lock(hashtextextended('anclax:admission-group-create',0)) THEN
    WITH unclassified AS (
        SELECT id FROM anclax.tasks WHERE status='pending' AND admission_group_id IS NULL
            AND NOT anclax.is_system_task(spec->>'type') ORDER BY id LIMIT 256 FOR UPDATE SKIP LOCKED
    )
    UPDATE anclax.tasks t SET admission_group_id=NULL FROM unclassified u WHERE t.id=u.id;
    END IF;
    SELECT LEAST(4096,COALESCE(sum(prefetch_capacity),0))::int,
           LEAST(4096,COALESCE(sum((prefetch_capacity::bigint*prefetch_strict_percentage+99)/100),0))::int
    INTO target,strict_target FROM anclax.workers
    WHERE status='online' AND last_heartbeat>statement_timestamp()-prefetch_heartbeat_ttl_ms*interval '1 millisecond';
    SELECT count(*)::int,count(*) FILTER(WHERE priority>0)::int INTO current_ready,current_strict
    FROM anclax.tasks WHERE status='ready';
    target:=LEAST(p_batch,target-current_ready);
    IF target<=0 THEN RETURN 0; END IF;
    -- Match the Worker's sorted weighted-group wheel without expanding large
    -- weights into one row per unit. Only the singleton job advances the cursor.
    WITH configured AS (
        SELECT CASE WHEN key='default' THEN '__default__' ELSE key END AS name,value::bigint AS weight
        FROM jsonb_each_text(COALESCE((SELECT payload->'labelWeights' FROM anclax.worker_runtime_configs
            ORDER BY version DESC LIMIT 1),'{}')) WHERE value::bigint>0
    ), weights AS (
        SELECT name,max(weight) AS weight FROM configured GROUP BY name
        UNION ALL SELECT '__default__',1 WHERE NOT EXISTS(SELECT 1 FROM configured WHERE name='__default__')
    ), bounds AS (SELECT name,sum(weight) OVER(ORDER BY name) AS upper_bound FROM weights)
    SELECT array_agg(name ORDER BY name),array_agg(upper_bound::bigint ORDER BY name),max(upper_bound)::bigint
    INTO group_names,group_bounds,total_weight FROM bounds;
    group_cursor:=group_cursor%total_weight;
    FOR i IN 1..cardinality(group_names) LOOP
        IF group_cursor<group_bounds[i] THEN preferred_index:=i; EXIT; END IF;
    END LOOP;
    ordered_groups:=group_names[preferred_index:cardinality(group_names)]||group_names[1:preferred_index-1];
    weighted_labels:=array_remove(group_names,'__default__');
    FOR selected_task IN
        WITH capacities AS MATERIALIZED (
            SELECT l.tag,(SELECT count(*)::int FROM (SELECT 1 FROM anclax.task_tag_slots s
                WHERE s.tag=l.tag AND s.task_id IS NULL AND NOT s.retired LIMIT p_batch) free) AS free_slots
            FROM anclax.task_tag_limits l WHERE l.max_concurrency IS NOT NULL
        ), unavailable AS MATERIALIZED (
            SELECT COALESCE(array_agg(tag),'{}'::text[]) tags FROM capacities WHERE free_slots=0
        ), candidates AS MATERIALIZED (
            SELECT t.id,t.priority,t.weight,t.created_at,
                CASE WHEN t.priority=0 THEN array_position(ordered_groups,COALESCE(
                    (SELECT min(label) FROM unnest(g.labels) label WHERE label=ANY(weighted_labels)),
                    '__default__')) ELSE 0 END AS group_order
            FROM anclax.task_admission_groups g CROSS JOIN unavailable u
            CROSS JOIN LATERAL (
                SELECT t.id,t.priority,CASE WHEN t.priority=0 THEN t.weight ELSE 0 END AS weight,t.created_at FROM anclax.tasks t
                WHERE t.admission_group_id=g.id AND t.status='pending'
                  AND (t.started_at IS NULL OR t.started_at<=statement_timestamp())
                  AND (t.locked_at IS NULL OR COALESCE(t.lease_expires_at,t.locked_at+p_lock_ttl_ms*interval '1 millisecond')<=statement_timestamp())
                  AND (t.priority=0 OR current_strict<strict_target)
                  AND (t.serial_key IS NULL OR NOT EXISTS(SELECT 1 FROM anclax.tasks h
                      WHERE h.serial_key=t.serial_key AND h.status IN ('pending','ready')
                      AND ROW(h.serial_id IS NULL,COALESCE(h.serial_id,2147483647),h.created_at,COALESCE(h.started_at,'-infinity'::timestamptz),h.id)
                        < ROW(t.serial_id IS NULL,COALESCE(t.serial_id,2147483647),t.created_at,COALESCE(t.started_at,'-infinity'::timestamptz),t.id)))
                ORDER BY t.priority DESC,CASE WHEN t.priority=0 THEN t.weight ELSE 0 END DESC,t.created_at,t.id
                LIMIT LEAST(p_batch,COALESCE((SELECT min(a.free_slots) FROM capacities a WHERE a.tag=ANY(g.tags)),p_batch))
            ) t
            WHERE NOT g.tags && u.tags
              AND EXISTS(SELECT 1 FROM anclax.workers w WHERE w.status='online' AND w.prefetch_capacity>0
                AND w.last_heartbeat>statement_timestamp()-w.prefetch_heartbeat_ttl_ms*interval '1 millisecond'
                AND w.labels @> to_jsonb(g.labels) AND (t.priority=0 OR w.prefetch_strict_percentage>0))
        )
        SELECT t.id,t.priority FROM candidates c JOIN anclax.tasks t ON t.id=c.id
        WHERE t.status='pending' ORDER BY c.priority DESC,c.group_order,c.weight DESC,c.created_at,c.id
        LIMIT p_batch FOR UPDATE OF t SKIP LOCKED
    LOOP
        EXIT WHEN n>=target;
        CONTINUE WHEN selected_task.priority>0 AND current_strict>=strict_target;
        IF anclax.try_admit_task_tags(selected_task.id,p_lock_ttl_ms) THEN
            UPDATE anclax.tasks SET status='ready',ready_expires_at=statement_timestamp()+p_ready_ttl_ms*interval '1 millisecond',
                locked_at=NULL,worker_id=NULL,lease_expires_at=NULL,lease_duration_ms=NULL,
                lease_version=lease_version+1,updated_at=statement_timestamp() WHERE id=selected_task.id;
            n:=n+1;
            IF selected_task.priority>0 THEN current_strict:=current_strict+1; END IF;
        END IF;
    END LOOP;
    IF n>0 THEN
        UPDATE anclax.tasks SET spec=jsonb_set(spec,'{payload,admissionCursor}',to_jsonb((group_cursor+1)%total_weight),true)
        WHERE id=p_task_id;
    END IF;
    RETURN n;
END;
$$;
COMMIT;
