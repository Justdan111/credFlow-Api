-- 0007 down.

ALTER TABLE businesses
    DROP COLUMN IF EXISTS monthly_collection_target,
    DROP COLUMN IF EXISTS currency;
