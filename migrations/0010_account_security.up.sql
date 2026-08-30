-- 0010 up: profile phone plus password-reset tokens.

-- The settings Profile section shows a phone field with nowhere to store it.
ALTER TABLE users ADD COLUMN phone TEXT;

-- Password recovery. Without this a user who forgets their password is locked
-- out permanently: there is no reset flow and no administrative way back in.
CREATE TABLE password_reset_tokens (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- SHA-256 of the opaque token; the token itself never reaches the database,
    -- so a leaked dump contains nothing redeemable. Deterministic, so it can
    -- carry a UNIQUE index and be found in one probe — bcrypt's random salt
    -- would make lookup impossible, and its deliberate slowness would buy
    -- nothing against 256 bits of entropy.
    token_hash BYTEA       NOT NULL UNIQUE,

    expires_at TIMESTAMPTZ NOT NULL,

    -- Non-null once redeemed. Single-use: a link sitting in an inbox or a
    -- browser history must not work a second time.
    used_at    TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Invalidating a user's outstanding tokens when a new one is issued.
CREATE INDEX password_reset_tokens_user_idx ON password_reset_tokens (user_id);
-- Supports the periodic cleanup sweep.
CREATE INDEX password_reset_tokens_expires_idx ON password_reset_tokens (expires_at);
