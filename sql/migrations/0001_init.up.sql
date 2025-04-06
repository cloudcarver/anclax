BEGIN;

CREATE TABLE IF NOT EXISTS organizations (
    id            SERIAL,
    name          TEXT        NOT NULL,
    created_at    TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at    TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    UNIQUE (name),
    PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS users (
    id              SERIAL,
    name            TEXT        NOT NULL,
    password_hash   TEXT        NOT NULL,
    password_salt   TEXT        NOT NULL,
    organization_id INTEGER     NOT NULL REFERENCES organizations(id),
    created_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    UNIQUE (name),
    PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS organization_owners (
    user_id         INTEGER     NOT NULL REFERENCES users(id),
    organization_id INTEGER     NOT NULL REFERENCES organizations(id),
    created_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    PRIMARY KEY (user_id, organization_id)
);

CREATE TABLE IF NOT EXISTS refresh_tokens (
    token           TEXT        NOT NULL,
    expired_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    UNIQUE (token)
);

CREATE TABLE IF NOT EXISTS org_settings (
    organization_id INTEGER     NOT NULL REFERENCES organizations(id),
    timezone        TEXT        NOT NULL DEFAULT 'UTC',

    created_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    PRIMARY KEY (organization_id)
);

CREATE TABLE IF NOT EXISTS keys (
    id              UUID        NOT NULL DEFAULT gen_random_uuid(),
    public_key      TEXT        NOT NULL,
    private_key     TEXT        NOT NULL,
    expired_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at      TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,

    PRIMARY KEY (kid)
);

COMMIT;
