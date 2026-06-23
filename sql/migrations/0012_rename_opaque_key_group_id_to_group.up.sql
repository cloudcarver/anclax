BEGIN;

ALTER TABLE anclax.opaque_keys
    RENAME COLUMN group_id TO "group";

ALTER TABLE anclax.opaque_keys
    ALTER COLUMN "group" TYPE TEXT
    USING CASE
        WHEN "group" IS NULL THEN NULL
        ELSE 'user:' || "group"::TEXT
    END;

ALTER INDEX IF EXISTS anclax.opaque_keys_group_id_idx
    RENAME TO opaque_keys_group_idx;

COMMIT;
