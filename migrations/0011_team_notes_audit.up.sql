-- 0011 up: team membership, customer notes, and an append-only audit trail.

-- ---------------------------------------------------------------------------
-- Users become removable.
-- ---------------------------------------------------------------------------

-- Soft delete, matching customers/debts/payments. A removed teammate's audit
-- entries and authored notes must keep resolving, which a hard delete would
-- break (or cascade away).
ALTER TABLE users ADD COLUMN deleted_at TIMESTAMPTZ;

-- Records who invited whom. Nullable: the founding owner was not invited, and
-- the inviter may later be removed.
ALTER TABLE users ADD COLUMN invited_by UUID REFERENCES users(id) ON DELETE SET NULL;

-- The UNIQUE constraint from 0001 spans every row including removed ones, so a
-- removed teammate's address could never be reused — not even to re-invite the
-- same person. Replace it with a partial index over live rows only.
ALTER TABLE users DROP CONSTRAINT users_email_key;
CREATE UNIQUE INDEX users_email_uniq ON users (email) WHERE deleted_at IS NULL;

-- Hot path: list the team for one business.
CREATE INDEX users_business_active_idx
    ON users (business_id, created_at)
    WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- Customer notes: the follow-up history a collections conversation needs.
-- ---------------------------------------------------------------------------

CREATE TABLE customer_notes (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id UUID        NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    customer_id UUID        NOT NULL REFERENCES customers(id) ON DELETE CASCADE,

    -- SET NULL, not CASCADE: removing a teammate must not erase the collections
    -- history they recorded. author_name preserves attribution regardless.
    author_id   UUID        REFERENCES users(id) ON DELETE SET NULL,
    author_name TEXT        NOT NULL,

    body        TEXT        NOT NULL CHECK (length(btrim(body)) > 0),

    -- What kind of contact this was, so the timeline reads as a record of
    -- activity rather than a pile of undifferentiated text.
    channel     TEXT        NOT NULL DEFAULT 'note'
                            CHECK (channel IN ('note', 'call', 'sms', 'email', 'visit')),

    deleted_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TRIGGER customer_notes_set_updated_at
    BEFORE UPDATE ON customer_notes
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- Hot path: one customer's notes, newest first.
CREATE INDEX customer_notes_customer_idx
    ON customer_notes (customer_id, created_at DESC)
    WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- Audit trail.
-- ---------------------------------------------------------------------------

-- Append-only by construction: no updated_at, no set_updated_at trigger, and no
-- UPDATE or DELETE path in the repository. A record that can be edited is not
-- an audit trail.
CREATE TABLE audit_logs (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id   UUID        NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,

    -- SET NULL so removing a user cannot delete the evidence of what they did.
    actor_id      UUID        REFERENCES users(id) ON DELETE SET NULL,
    -- Denormalised on purpose: the entry must still name somebody once the
    -- user row is gone, and must not change if they later rename themselves.
    actor_email   TEXT        NOT NULL,
    actor_name    TEXT        NOT NULL,

    -- Dotted "<entity>.<verb>", e.g. "payment.voided".
    action        TEXT        NOT NULL,
    entity_type   TEXT        NOT NULL,
    -- Nullable: a few actions (a bulk change) name no single row.
    entity_id     UUID,

    -- Whatever context makes the entry readable a year later: the amount voided,
    -- the role a user was promoted to. Free-form so a new action needs no
    -- migration, JSONB so it stays queryable.
    metadata      JSONB       NOT NULL DEFAULT '{}'::JSONB,

    -- Best-effort attribution. Only meaningful when TRUST_PROXY_HEADERS matches
    -- the deployment; see the README.
    ip            TEXT,

    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Hot path: the audit screen, newest first for one tenant.
CREATE INDEX audit_logs_business_created_idx
    ON audit_logs (business_id, created_at DESC);

-- Filtering the trail by action or by the row it touched.
CREATE INDEX audit_logs_business_action_idx ON audit_logs (business_id, action);
CREATE INDEX audit_logs_entity_idx ON audit_logs (business_id, entity_type, entity_id);
