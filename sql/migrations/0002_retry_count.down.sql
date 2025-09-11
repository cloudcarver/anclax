BEGIN;

ALTER TABLE anclax.tasks DROP COLUMN attempts;

COMMIT;

