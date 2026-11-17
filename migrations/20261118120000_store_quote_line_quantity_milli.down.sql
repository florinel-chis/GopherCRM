-- Migration: Restore whole quote line quantities (#343)
-- Re-adds quote_lines.quantity bigint NOT NULL (added with DEFAULT 0, then the default removed, as the column was created) when missing, sets it to quantity_milli rounded half up to whole units with a floor of 1 (GREATEST(1, (quantity_milli + 500) DIV 1000)) while quantity_milli exists, then drops quantity_milli when present. A fractional quantity loses its fraction; line and quote totals keep their stored values. Every step is guarded by an INFORMATION_SCHEMA probe. Run it before starting an older binary. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_lines' AND COLUMN_NAME = 'quantity'), 'SELECT 1', 'ALTER TABLE quote_lines ADD COLUMN quantity bigint NOT NULL DEFAULT 0'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_lines' AND COLUMN_NAME = 'quantity_milli'), 'UPDATE quote_lines SET quantity = GREATEST(1, (quantity_milli + 500) DIV 1000)', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_lines' AND COLUMN_NAME = 'quantity'), 'ALTER TABLE quote_lines MODIFY COLUMN quantity bigint NOT NULL', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_lines' AND COLUMN_NAME = 'quantity_milli'), 'ALTER TABLE quote_lines DROP COLUMN quantity_milli', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
