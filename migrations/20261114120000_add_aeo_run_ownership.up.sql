-- Migration: AEO run ownership (#229)
-- Adds owner_instance, heartbeat_at, running_lock and schedule_slot to aeo_runs and the unique indexes that make admission and the daily slot atomic across replicas. Each column and index is added only when missing (safe after AutoMigrate). No backfill: a legacy running row has a NULL heartbeat and is failed by the next boot reconciliation. Upgrade by stopping every replica before starting the new version: an old binary neither sets running_lock nor spares live runs. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'aeo_runs' AND COLUMN_NAME = 'owner_instance'), 'SELECT 1', 'ALTER TABLE aeo_runs ADD COLUMN owner_instance varchar(64) NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'aeo_runs' AND COLUMN_NAME = 'heartbeat_at'), 'SELECT 1', 'ALTER TABLE aeo_runs ADD COLUMN heartbeat_at datetime(3) NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'aeo_runs' AND COLUMN_NAME = 'running_lock'), 'SELECT 1', 'ALTER TABLE aeo_runs ADD COLUMN running_lock tinyint unsigned NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'aeo_runs' AND COLUMN_NAME = 'schedule_slot'), 'SELECT 1', 'ALTER TABLE aeo_runs ADD COLUMN schedule_slot varchar(20) NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'aeo_runs' AND INDEX_NAME = 'idx_aeo_runs_running_lock'), 'SELECT 1', 'CREATE UNIQUE INDEX idx_aeo_runs_running_lock ON aeo_runs (running_lock)'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'aeo_runs' AND INDEX_NAME = 'idx_aeo_runs_schedule_slot'), 'SELECT 1', 'CREATE UNIQUE INDEX idx_aeo_runs_schedule_slot ON aeo_runs (schedule_slot)'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
