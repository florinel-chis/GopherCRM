-- Migration: Add automation run leases (#237)
-- Adds owner_instance and heartbeat_at to automation_runs, each only when missing (safe after AutoMigrate). No backfill: a legacy running row has a NULL heartbeat and is recovered by the next sweep. Upgrade by stopping every replica before starting the new version: an old binary's boot reconciliation resets live runs. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'automation_runs' AND COLUMN_NAME = 'owner_instance'), 'SELECT 1', 'ALTER TABLE automation_runs ADD COLUMN owner_instance varchar(64) NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'automation_runs' AND COLUMN_NAME = 'heartbeat_at'), 'SELECT 1', 'ALTER TABLE automation_runs ADD COLUMN heartbeat_at datetime(3) NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
