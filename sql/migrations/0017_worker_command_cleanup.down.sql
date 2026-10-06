BEGIN;

-- Older workers cannot execute cleanup commands. Preserve their history while
-- removing unfinished commands from scheduling before restoring the type list.
UPDATE anclax.tasks SET status = 'cancelled', updated_at = CURRENT_TIMESTAMP
WHERE spec->>'type' = 'cleanupWorkerCommandTasks'
    AND status IN ('pending', 'ready', 'running', 'paused');

CREATE OR REPLACE FUNCTION anclax.is_system_task(task_type TEXT) RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT task_type IN ('prefetchTasks', 'broadcastUpdateWorkerRuntimeConfig',
      'applyWorkerRuntimeConfigToWorker', 'broadcastCancelTask', 'cancelTaskOnWorker',
      'broadcastPauseTask', 'pauseTaskOnWorker');
$$;

COMMIT;
