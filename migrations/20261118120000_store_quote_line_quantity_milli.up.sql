-- Migration: Store quote line quantities in thousandths (#343)
-- Adds quote_lines.quantity_milli bigint NOT NULL DEFAULT 0 when missing, copies quantity x 1000 into it on every row still at 0 while the legacy whole-unit column quantity exists, then drops quantity when present. Every step is guarded by an INFORMATION_SCHEMA probe, so the migration is safe before or after the application's AutoMigrate and MigrateDatabase (which perform the same conversion). updated_at and the stored totals are not touched. No index. Stop every replica before applying it: an older binary cannot write quote lines once quantity is dropped. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_lines' AND COLUMN_NAME = 'quantity_milli'), 'SELECT 1', 'ALTER TABLE quote_lines ADD COLUMN quantity_milli bigint NOT NULL DEFAULT 0'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_lines' AND COLUMN_NAME = 'quantity'), 'UPDATE quote_lines SET quantity_milli = quantity * 1000 WHERE quantity_milli = 0', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_lines' AND COLUMN_NAME = 'quantity'), 'ALTER TABLE quote_lines DROP COLUMN quantity', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
