BEGIN;

ALTER TABLE anclax.opaque_keys
    DROP CONSTRAINT IF EXISTS opaque_keys_user_id_fkey;

ALTER TABLE anclax.opaque_keys
    RENAME COLUMN user_id TO group_id;

CREATE INDEX IF NOT EXISTS opaque_keys_group_id_idx
    ON anclax.opaque_keys (group_id);

COMMIT;
