-- Migration: Remove auth audit log API key id (#551)
-- Date: 2026-11-29
-- Description: Drops the column api_key_id of auth_audit_log only when it is present, and nothing else. Every audit row is kept; the key id of the API key rows is lost. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'auth_audit_log' AND COLUMN_NAME = 'api_key_id'), 'ALTER TABLE auth_audit_log DROP COLUMN api_key_id', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
