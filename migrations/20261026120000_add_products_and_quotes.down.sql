-- Reverse of 20261026120000_add_products_and_quotes: the four tables, children
-- first so no foreign key blocks a drop. Nothing else was touched by the up
-- migration, so nothing else is touched here.

DROP TABLE IF EXISTS quote_status_changes;
DROP TABLE IF EXISTS quote_lines;
DROP TABLE IF EXISTS quotes;
DROP TABLE IF EXISTS products;
