-- Migration: Remove automation run leases (#237)
-- Drops heartbeat_at and owner_instance, each only when present. Runs keep their status and positions. Roll back the binary as well: the older worker assumes a single replica.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'automation_runs' AND COLUMN_NAME = 'heartbeat_at'), 'ALTER TABLE automation_runs DROP COLUMN heartbeat_at', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'automation_runs' AND COLUMN_NAME = 'owner_instance'), 'ALTER TABLE automation_runs DROP COLUMN owner_instance', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
