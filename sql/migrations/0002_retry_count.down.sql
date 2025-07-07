BEGIN;

ALTER TABLE anchor.tasks DROP COLUMN attempts;

COMMIT;

