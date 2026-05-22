-- 0004 up: payments. Linked to a customer (required) and a debt (optional).
-- Idempotency keys prevent double-submission of money-moving requests.

CREATE TABLE payments (
    id              UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id     UUID            NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    customer_id     UUID            NOT NULL REFERENCES customers(id) ON DELETE RESTRICT,
    debt_id         UUID            REFERENCES debts(id) ON DELETE RESTRICT,
    amount          NUMERIC(14, 2)  NOT NULL CHECK (amount > 0),
    method          TEXT            NOT NULL DEFAULT 'cash'
                                    CHECK (method IN ('cash', 'card', 'bank_transfer', 'check', 'mobile_money', 'other')),
    reference       TEXT,
    notes           TEXT,
    paid_at         TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    idempotency_key TEXT,
    deleted_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE TRIGGER payments_set_updated_at
    BEFORE UPDATE ON payments
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- The idempotency guard. Per-tenant scope so different businesses can't
-- collide on key choice. Partial so:
--   1. Payments without a key are allowed (key is optional).
--   2. A voided payment frees up its key for reuse if needed.
CREATE UNIQUE INDEX payments_business_idempotency_uniq
    ON payments (business_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL AND deleted_at IS NULL;

-- Hot path: list active payments for a tenant, most recent first.
CREATE INDEX payments_business_id_active_idx
    ON payments (business_id, paid_at DESC)
    WHERE deleted_at IS NULL;

-- Nested endpoint: GET /api/customers/{id}/payments.
CREATE INDEX payments_customer_id_active_idx
    ON payments (customer_id)
    WHERE deleted_at IS NULL;

-- Used by both the debt-status SUM(amount) recompute and the debt-detail view.
CREATE INDEX payments_debt_id_active_idx
    ON payments (debt_id)
    WHERE debt_id IS NOT NULL AND deleted_at IS NULL;
