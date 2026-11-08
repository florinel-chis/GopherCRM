-- Migration: Customer external identity mappings
-- Date: 2026-11-11
-- Description: One row per (source, scope, external_id) identity an integrator
-- maps to a customer (issue #212). The triple is unique and compared exactly:
-- the three columns carry utf8mb4_bin, so values differing only in letter case
-- or accents are distinct. Columns, index names and the foreign-key name match
-- what GORM's AutoMigrate produces plus the collation guard of
-- models.MigrateDatabase, and the table is created only when missing, so this
-- migration is safe on a database whose application startup already created
-- it (that startup also converted the collation). No deleted_at: mappings are
-- hard-deleted, and the erasure of a customer hard-deletes its mappings. MySQL
-- 8 and MariaDB 10.11 both accept this as written.

CREATE TABLE IF NOT EXISTS customer_external_ids (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    customer_id bigint unsigned NOT NULL,
    source varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    scope varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
    external_id varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_customer_external_ids_identity (source, scope, external_id),
    KEY idx_customer_external_ids_customer_id (customer_id),
    CONSTRAINT fk_customer_external_ids_customer FOREIGN KEY (customer_id) REFERENCES customers (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
