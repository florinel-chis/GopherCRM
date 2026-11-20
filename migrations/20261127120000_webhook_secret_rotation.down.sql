-- Migration: Remove webhook signing-secret rotation (#503)
-- Date: 2026-11-27
-- Description: Drops the columns previous_secret_expires_at and previous_secret_sealed of webhook_endpoints, each only when present, and nothing else. Every endpoint row is kept with its current secret; a previous secret still inside its overlap window is lost, so requests then carry the current signature only. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'webhook_endpoints' AND COLUMN_NAME = 'previous_secret_expires_at'), 'ALTER TABLE webhook_endpoints DROP COLUMN previous_secret_expires_at', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'webhook_endpoints' AND COLUMN_NAME = 'previous_secret_sealed'), 'ALTER TABLE webhook_endpoints DROP COLUMN previous_secret_sealed', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
