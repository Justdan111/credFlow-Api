-- 0002 down: drop customers; indexes and trigger go with the table.

DROP TABLE IF EXISTS customers;
-- set_updated_at() left in place; still used by businesses and users from 0001.
