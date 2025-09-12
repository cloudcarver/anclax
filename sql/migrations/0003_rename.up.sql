BEGIN;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_namespace
        WHERE nspname = 'anclax'
    ) THEN
        EXECUTE 'ALTER SCHEMA anchor RENAME TO anclax';
    END IF;
END;
$$;

COMMIT;
