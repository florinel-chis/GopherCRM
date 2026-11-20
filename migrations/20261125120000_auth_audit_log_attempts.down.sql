-- Migration: Remove sign-in audit log attempts (#466)
-- Date: 2026-11-25
-- Description: Drops the unique index idx_auth_audit_log_aggregate_key and the columns aggregate_key and attempts of auth_audit_log, each only when present, and nothing else. Every row is kept; an aggregated API-key row then reads as one refusal, so how many refusals it counted is lost. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'auth_audit_log' AND INDEX_NAME = 'idx_auth_audit_log_aggregate_key'), 'ALTER TABLE auth_audit_log DROP INDEX idx_auth_audit_log_aggregate_key', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'auth_audit_log' AND COLUMN_NAME = 'aggregate_key'), 'ALTER TABLE auth_audit_log DROP COLUMN aggregate_key', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'auth_audit_log' AND COLUMN_NAME = 'attempts'), 'ALTER TABLE auth_audit_log DROP COLUMN attempts', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
