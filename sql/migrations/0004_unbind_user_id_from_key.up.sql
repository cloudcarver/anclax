BEGIN;

ALTER TABLE anclax.opaque_keys
    ALTER COLUMN user_id DROP NOT NULL;

COMMIT;
