-- Migration: Product external identity mappings
-- Date: 2026-11-12
-- Description: One row per (source, scope, external_id) identity an integrator
-- maps to a product (issue #239), following the customer mappings of #212.
-- The triple is unique and compared exactly: the three columns carry
-- utf8mb4_bin, so values differing only in letter case or accents are
-- distinct. Columns, index names and the foreign-key name match what GORM's
-- AutoMigrate produces plus the collation guard of models.MigrateDatabase, and
-- the table is created only when missing, so this migration is safe on a
-- database whose application startup already created it (that startup also
-- converted the collation). No deleted_at: mappings are hard-deleted, and
-- deleting a product hard-deletes its mappings in the same transaction.
-- products.sku is left as it is (indexed, not unique). MySQL 8 and MariaDB
-- 10.11 both accept this as written.

CREATE TABLE IF NOT EXISTS product_external_ids (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    product_id bigint unsigned NOT NULL,
    source varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    scope varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
    external_id varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_product_external_ids_identity (source, scope, external_id),
    KEY idx_product_external_ids_product_id (product_id),
    CONSTRAINT fk_product_external_ids_product FOREIGN KEY (product_id) REFERENCES products (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
