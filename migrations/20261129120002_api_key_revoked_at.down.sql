-- Migration: Remove API key revoked_at (#525)
-- Date: 2026-11-29
-- Description: Drops the column revoked_at of api_keys, only when present, and nothing else. Every key row is kept with its is_active value, so a revoked key stays inactive; only the time of its revocation is lost. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'api_keys' AND COLUMN_NAME = 'revoked_at'), 'ALTER TABLE api_keys DROP COLUMN revoked_at', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
