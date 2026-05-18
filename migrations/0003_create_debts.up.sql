-- 0003 up: debts owed by customers. Tenant-scoped, soft-deletable.

CREATE TABLE debts (
    id              UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id     UUID            NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    customer_id     UUID            NOT NULL REFERENCES customers(id) ON DELETE RESTRICT,
    amount          NUMERIC(14, 2)  NOT NULL CHECK (amount > 0),
    description     TEXT,
    status          TEXT            NOT NULL DEFAULT 'pending'
                                    CHECK (status IN ('pending', 'partial', 'paid')),
    issued_date     DATE            NOT NULL DEFAULT CURRENT_DATE,
    due_date        DATE            NOT NULL,
    paid_at         TIMESTAMPTZ,
    deleted_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),

    CHECK (due_date >= issued_date)
);

CREATE TRIGGER debts_set_updated_at
    BEFORE UPDATE ON debts
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- Hot path: list active debts for a tenant, newest first.
CREATE INDEX debts_business_id_active_idx
    ON debts (business_id, created_at DESC)
    WHERE deleted_at IS NULL;

-- Nested endpoint: GET /api/customers/{id}/debts.
CREATE INDEX debts_customer_id_active_idx
    ON debts (customer_id)
    WHERE deleted_at IS NULL;

-- Due-date filtering (overdue queries, due-date sort).
CREATE INDEX debts_business_due_date_idx
    ON debts (business_id, due_date)
    WHERE deleted_at IS NULL;
