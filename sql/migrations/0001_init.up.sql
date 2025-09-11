BEGIN;

CREATE SCHEMA IF NOT EXISTS anclax;

CREATE TABLE IF NOT EXISTS anclax.orgs (
    id         SERIAL      PRIMARY KEY,
    name       TEXT        NOT NULL,
    tz         TEXT        NOT NULL DEFAULT 'Asia/Shanghai',
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS anclax.users (
    id              SERIAL      PRIMARY KEY,
    name            TEXT        NOT NULL,
    password_hash   TEXT        NOT NULL,
    password_salt   TEXT        NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    deleted_at      TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS anclax.user_default_orgs (
    user_id    INTEGER NOT NULL REFERENCES anclax.users(id) ON UPDATE CASCADE ON DELETE CASCADE,
    org_id     INTEGER NOT NULL REFERENCES anclax.orgs(id) ON UPDATE CASCADE ON DELETE CASCADE,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    PRIMARY KEY (user_id)
);

CREATE TABLE IF NOT EXISTS anclax.org_users (
    org_id     INTEGER NOT NULL REFERENCES anclax.orgs(id) ON UPDATE CASCADE ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES anclax.users(id) ON UPDATE CASCADE ON DELETE CASCADE,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    PRIMARY KEY (org_id, user_id)
);

CREATE TABLE IF NOT EXISTS anclax.org_owners (
    org_id     INTEGER NOT NULL REFERENCES anclax.orgs(id)  ON UPDATE CASCADE ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES anclax.users(id) ON UPDATE CASCADE,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    PRIMARY KEY (org_id)
);

CREATE TABLE IF NOT EXISTS anclax.opaque_keys (
    id              BIGSERIAL   PRIMARY KEY,
    key             BYTEA       NOT NULL,
    user_id         INT         NOT NULL REFERENCES anclax.users(id) ON DELETE CASCADE ON UPDATE CASCADE,
    created_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS anclax.access_key_pairs (
    access_key      VARCHAR(20) NOT NULL,
    secret_key      VARCHAR(40) NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    PRIMARY KEY (access_key)
);

CREATE TABLE IF NOT EXISTS anclax.access_rules (
    name        VARCHAR(255) NOT NULL,
    description TEXT         NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at  TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    PRIMARY KEY (name)
);

CREATE TABLE IF NOT EXISTS anclax.roles (
    id          SERIAL PRIMARY KEY,
    org_id      INTEGER      NOT NULL REFERENCES anclax.orgs(id) ON UPDATE CASCADE,
    name        VARCHAR(255) NOT NULL,
    description TEXT         NOT NULL,
    created_at  TIMESTAMPTZ  DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at  TIMESTAMPTZ  DEFAULT CURRENT_TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS anclax.role_access_rules (
    role_id          INTEGER NOT NULL,
    access_rule_name VARCHAR(255) NOT NULL REFERENCES anclax.access_rules(name) ON UPDATE CASCADE,
    created_at       TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at       TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    PRIMARY KEY (role_id, access_rule_name)
);

CREATE TABLE IF NOT EXISTS anclax.users_roles (
    user_id    INTEGER NOT NULL,
    role_id    INTEGER NOT NULL,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    PRIMARY KEY (user_id, role_id)
);

CREATE TABLE IF NOT EXISTS anclax.tasks (
    id          SERIAL PRIMARY KEY,
    attributes  JSONB NOT NULL,
    spec        JSONB NOT NULL,
    status      VARCHAR(255) NOT NULL,
    unique_tag  VARCHAR(255), -- for unique task
    started_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    UNIQUE (unique_tag)
);

CREATE TABLE IF NOT EXISTS anclax.events (
    id         SERIAL PRIMARY KEY,
    spec       JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMIT;
