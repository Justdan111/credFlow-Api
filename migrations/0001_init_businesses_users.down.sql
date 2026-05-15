-- 0001 down: reverse 0001 up, in reverse order.

DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS businesses;
DROP FUNCTION IF EXISTS set_updated_at();
-- pgcrypto and citext extensions are left in place intentionally.
