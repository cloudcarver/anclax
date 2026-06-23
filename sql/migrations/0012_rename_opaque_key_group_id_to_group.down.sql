BEGIN;

ALTER INDEX IF EXISTS anclax.opaque_keys_group_idx
    RENAME TO opaque_keys_group_id_idx;

ALTER TABLE anclax.opaque_keys
    ALTER COLUMN "group" TYPE INT
    USING CASE
        WHEN "group" IS NULL THEN NULL
        WHEN "group" ~ '^user:[0-9]+$' THEN substring("group" FROM 6)::INT
        ELSE NULL
    END;

ALTER TABLE anclax.opaque_keys
    RENAME COLUMN "group" TO group_id;

COMMIT;
