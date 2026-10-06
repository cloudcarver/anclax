BEGIN;

CREATE OR REPLACE FUNCTION anclax.is_system_task(task_type TEXT) RETURNS BOOLEAN
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT task_type IN ('prefetchTasks', 'broadcastUpdateWorkerRuntimeConfig',
      'applyWorkerRuntimeConfigToWorker', 'broadcastCancelTask', 'cancelTaskOnWorker',
      'broadcastPauseTask', 'pauseTaskOnWorker', 'cleanupWorkerCommandTasks');
$$;

COMMIT;
