-- Migration: Auth audit log API key id (#551)
-- Date: 2026-11-29
-- Description: Creating, revoking and a refused re-enable of an API key each write one auth_audit_log row that names the key. Adds api_key_id bigint unsigned NULL (the api_keys row such a row concerns) to auth_audit_log only when it is missing, so this is safe after the application's AutoMigrate already added it; the name and type match what AutoMigrate produces. No backfill, no index, no foreign key: every existing row reads NULL. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'auth_audit_log' AND COLUMN_NAME = 'api_key_id'), 'SELECT 1', 'ALTER TABLE auth_audit_log ADD COLUMN api_key_id bigint unsigned NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
