-- Migration: API key revoked_at (#525)
-- Date: 2026-11-29
-- Description: Revoking an API key is final and records when it happened. Adds revoked_at datetime(3) NULL to api_keys only when missing, so this is safe after the application's AutoMigrate already added it; name, type and nullability match what AutoMigrate produces. Existing rows keep revoked_at NULL and their is_active unchanged: a key that is already inactive stays inactive and cannot be switched on again. No backfill, no index, no other change. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'api_keys' AND COLUMN_NAME = 'revoked_at'), 'SELECT 1', 'ALTER TABLE api_keys ADD COLUMN revoked_at datetime(3) NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
