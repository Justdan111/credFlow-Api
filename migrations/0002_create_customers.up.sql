-- 0002 up: customers, soft-deleted, tenant-scoped, indexed for list reads.

CREATE TABLE customers (
    id              UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id     UUID            NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    name            TEXT            NOT NULL,
    email           CITEXT,
    phone           TEXT,
    company_name    TEXT,
    address         TEXT,
    risk_level      TEXT            NOT NULL DEFAULT 'low'
                                    CHECK (risk_level IN ('low', 'medium', 'high')),
    credit_limit    NUMERIC(14, 2)  NOT NULL DEFAULT 0,
    notes           TEXT,
    deleted_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE TRIGGER customers_set_updated_at
    BEFORE UPDATE ON customers
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- Hot path: "list active customers for my tenant, sorted by created_at desc".
-- Partial index excluding soft-deleted rows keeps it lean.
CREATE INDEX customers_business_id_active_idx
    ON customers (business_id, created_at DESC)
    WHERE deleted_at IS NULL;

-- Per-tenant email uniqueness. Partial so:
--   1. customers without an email are allowed (CITEXT column is nullable)
--   2. an email freed by soft-delete can be reused by a new customer
CREATE UNIQUE INDEX customers_business_id_email_uniq
    ON customers (business_id, email)
    WHERE email IS NOT NULL AND deleted_at IS NULL;
