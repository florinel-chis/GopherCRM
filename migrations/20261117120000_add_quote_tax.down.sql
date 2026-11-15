-- Migration: Remove quote tax (#324)
-- Drops quotes.gross_total_cents, quotes.tax_cents, quote_lines.tax_cents and quote_lines.tax_rate_bp, each only when present. Every row is kept; the tax rates and amounts are lost.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quotes' AND COLUMN_NAME = 'gross_total_cents'), 'ALTER TABLE quotes DROP COLUMN gross_total_cents', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quotes' AND COLUMN_NAME = 'tax_cents'), 'ALTER TABLE quotes DROP COLUMN tax_cents', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_lines' AND COLUMN_NAME = 'tax_cents'), 'ALTER TABLE quote_lines DROP COLUMN tax_cents', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_lines' AND COLUMN_NAME = 'tax_rate_bp'), 'ALTER TABLE quote_lines DROP COLUMN tax_rate_bp', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
