-- Migration: Sign-in audit log attempts (#466)
-- Date: 2026-11-25
-- Description: A refused API key no longer writes one auth_audit_log row per request. It adds to one row per reason, user id, client /24 or /48 network and UTC minute, through one INSERT ... ON DUPLICATE KEY UPDATE attempts = attempts + 1 on a unique aggregate_key. Adds attempts bigint unsigned NOT NULL DEFAULT 1, aggregate_key varchar(191) NULL and the unique index idx_auth_audit_log_aggregate_key (aggregate_key), each only when missing, so this is safe after the application's AutoMigrate already added them; names and types match what AutoMigrate produces. No backfill: every existing row reads attempts 1 and aggregate_key NULL, and NULLs never collide under the unique index. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'auth_audit_log' AND COLUMN_NAME = 'attempts'), 'SELECT 1', 'ALTER TABLE auth_audit_log ADD COLUMN attempts bigint unsigned NOT NULL DEFAULT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'auth_audit_log' AND COLUMN_NAME = 'aggregate_key'), 'SELECT 1', 'ALTER TABLE auth_audit_log ADD COLUMN aggregate_key varchar(191) NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'auth_audit_log' AND INDEX_NAME = 'idx_auth_audit_log_aggregate_key'), 'SELECT 1', 'ALTER TABLE auth_audit_log ADD UNIQUE KEY idx_auth_audit_log_aggregate_key (aggregate_key)'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
