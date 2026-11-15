-- Migration: Add quote tax (#324)
-- Adds quote_lines.tax_rate_bp and quote_lines.tax_cents, and quotes.tax_cents and quotes.gross_total_cents, each bigint NOT NULL DEFAULT 0 and each only when missing (safe after AutoMigrate). Existing lines read a rate and a tax of 0; existing quotes read a tax of 0, and the one backfill sets gross_total_cents = total_cents + tax_cents wherever it differs, without touching updated_at. No index. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_lines' AND COLUMN_NAME = 'tax_rate_bp'), 'SELECT 1', 'ALTER TABLE quote_lines ADD COLUMN tax_rate_bp bigint NOT NULL DEFAULT 0'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_lines' AND COLUMN_NAME = 'tax_cents'), 'SELECT 1', 'ALTER TABLE quote_lines ADD COLUMN tax_cents bigint NOT NULL DEFAULT 0'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quotes' AND COLUMN_NAME = 'tax_cents'), 'SELECT 1', 'ALTER TABLE quotes ADD COLUMN tax_cents bigint NOT NULL DEFAULT 0'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quotes' AND COLUMN_NAME = 'gross_total_cents'), 'SELECT 1', 'ALTER TABLE quotes ADD COLUMN gross_total_cents bigint NOT NULL DEFAULT 0'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

UPDATE quotes SET gross_total_cents = total_cents + tax_cents WHERE gross_total_cents <> total_cents + tax_cents;
