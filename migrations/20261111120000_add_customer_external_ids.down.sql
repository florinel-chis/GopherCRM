-- Rollback: drop the customer external identity mappings (issue #212). The
-- mappings are lost; no other table references this one.

DROP TABLE IF EXISTS customer_external_ids;
