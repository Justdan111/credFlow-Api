-- 0006 down.

ALTER TABLE debts
    DROP COLUMN IF EXISTS manually_marked_paid;
