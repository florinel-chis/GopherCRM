-- Rollback: drop the product external identity mappings (issue #239). The
-- mappings are lost; no other table references this one, and the products
-- table is untouched.

DROP TABLE IF EXISTS product_external_ids;
