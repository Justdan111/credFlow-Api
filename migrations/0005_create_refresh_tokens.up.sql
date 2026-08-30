-- 0005 up: refresh tokens with family-based rotation and revocation.

CREATE TABLE refresh_tokens (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id              UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    business_id          UUID        NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,

    -- One login opens a family; every rotation stays inside it. Revoking a
    -- family therefore ends exactly one device's session, not all of them.
    -- Defaulted so a new family can be minted and returned by the INSERT
    -- itself, which keeps UUID generation in one place (Postgres).
    family_id            UUID        NOT NULL DEFAULT gen_random_uuid(),

    -- SHA-256 of the opaque token, never the token itself. Deterministic, so
    -- it can carry a UNIQUE index and be found in one probe. bcrypt could not:
    -- its random salt means the same token hashes differently every time, so
    -- there would be no value to index and a lookup would have to compare
    -- against every row in the table.
    token_hash           BYTEA       NOT NULL UNIQUE,

    expires_at           TIMESTAMPTZ NOT NULL,

    -- Copied forward unchanged on every rotation, so refreshing forever cannot
    -- extend a session past this hard deadline.
    absolute_expires_at  TIMESTAMPTZ NOT NULL,

    -- Non-null = retired by rotation. Presenting a retired token again is the
    -- reuse signal that revokes the family.
    used_at              TIMESTAMPTZ,
    -- Non-null = killed by logout or by reuse detection.
    revoked_at           TIMESTAMPTZ,

    user_agent           TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Revoking a family and listing a user's sessions are the two hot lookups.
CREATE INDEX refresh_tokens_family_idx  ON refresh_tokens(family_id);
CREATE INDEX refresh_tokens_user_idx    ON refresh_tokens(user_id);
-- Supports the periodic cleanup sweep.
CREATE INDEX refresh_tokens_expires_idx ON refresh_tokens(expires_at);
