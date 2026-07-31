BEGIN;

ALTER TABLE anclax.opaque_keys
    ADD COLUMN expires_at TIMESTAMPTZ;

-- Legacy keys have no trustworthy expiry timestamp. Invalidate them instead of
-- silently turning previously short-lived credentials into permanent ones.
DELETE FROM anclax.opaque_keys;

ALTER TABLE anclax.opaque_keys
    ALTER COLUMN expires_at SET NOT NULL;

-- Usernames are reserved even after soft deletion, matching the existing
-- IsUsernameExists and restore semantics. Fail closed if historical duplicates
-- require operator review rather than choosing an account to discard.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM anclax.users
        GROUP BY name
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot enforce username uniqueness: duplicate anclax.users.name values exist';
    END IF;
END $$;

ALTER TABLE anclax.users
    ADD CONSTRAINT users_name_unique UNIQUE (name);

COMMIT;
