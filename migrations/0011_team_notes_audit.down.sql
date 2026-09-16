-- 0011 down: reverse team membership, customer notes and the audit trail.

DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS customer_notes;

DROP INDEX IF EXISTS users_business_active_idx;
DROP INDEX IF EXISTS users_email_uniq;

-- Restore the table-level UNIQUE from 0001. This fails if two live rows share
-- an address, which can only happen if a removed teammate's email was reused —
-- deliberately loud rather than silently dropping one of them.
ALTER TABLE users ADD CONSTRAINT users_email_key UNIQUE (email);

ALTER TABLE users DROP COLUMN IF EXISTS invited_by;
ALTER TABLE users DROP COLUMN IF EXISTS deleted_at;
