BEGIN;

-- New versions do not rely on deletion tasks for correctness. Invalidate all
-- keys before removing expires_at so a downgrade cannot make them permanent.
DELETE FROM anclax.opaque_keys;

ALTER TABLE anclax.users
    DROP CONSTRAINT IF EXISTS users_name_unique;

ALTER TABLE anclax.opaque_keys
    DROP COLUMN expires_at;

COMMIT;
