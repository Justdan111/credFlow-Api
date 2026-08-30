-- 0008 up: daily risk-distribution snapshots.
--
-- customers.risk_level is a single mutable field. When a customer moves from
-- low to high the previous value is gone, so "what did the risk mix look like
-- in January" is unanswerable from the customers table alone. The analytics
-- screen charts exactly that, so the counts must be recorded as they happen.
--
-- One row per business per day holds the whole distribution: counting three
-- integers is far cheaper to write and read than a row per customer per day.

CREATE TABLE customer_risk_snapshots (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id   UUID        NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    snapshot_date DATE        NOT NULL,

    low_count     INTEGER     NOT NULL DEFAULT 0 CHECK (low_count    >= 0),
    medium_count  INTEGER     NOT NULL DEFAULT 0 CHECK (medium_count >= 0),
    high_count    INTEGER     NOT NULL DEFAULT 0 CHECK (high_count   >= 0),

    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Makes the daily writer idempotent: a second run on the same day corrects
    -- the row via ON CONFLICT DO UPDATE instead of duplicating it.
    UNIQUE (business_id, snapshot_date)
);

-- The trend query walks backwards from today, one business at a time.
CREATE INDEX customer_risk_snapshots_lookup_idx
    ON customer_risk_snapshots (business_id, snapshot_date DESC);
