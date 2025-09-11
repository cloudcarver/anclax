BEGIN;

CREATE TABLE counter (
    id    INTEGER NOT NULL,
    value INTEGER NOT NULL DEFAULT 0
);

INSERT INTO counter (id, value) VALUES (1, 0);

COMMIT;
