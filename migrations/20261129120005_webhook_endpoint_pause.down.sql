-- Migration: Remove webhook endpoint pause (#569)
-- Date: 2026-11-29
-- Description: Drops the columns paused_until and consecutive_failures of webhook_endpoints, each only when present, and nothing else. Every endpoint row is kept; a pause in progress and the failure count are lost, so every endpoint is sent to again on the next pass. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'webhook_endpoints' AND COLUMN_NAME = 'paused_until'), 'ALTER TABLE webhook_endpoints DROP COLUMN paused_until', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'webhook_endpoints' AND COLUMN_NAME = 'consecutive_failures'), 'ALTER TABLE webhook_endpoints DROP COLUMN consecutive_failures', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
