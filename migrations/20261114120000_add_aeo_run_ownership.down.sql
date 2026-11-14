-- Migration: Remove AEO run ownership
-- Drops the two unique indexes, then the four columns, each only when present. Runs keep their status and counters. The single-process overlap guard is not restored by SQL: roll back the binary as well. AutoMigrate of a newer binary adds everything back.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'aeo_runs' AND INDEX_NAME = 'idx_aeo_runs_schedule_slot'), 'DROP INDEX idx_aeo_runs_schedule_slot ON aeo_runs', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'aeo_runs' AND INDEX_NAME = 'idx_aeo_runs_running_lock'), 'DROP INDEX idx_aeo_runs_running_lock ON aeo_runs', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'aeo_runs' AND COLUMN_NAME = 'schedule_slot'), 'ALTER TABLE aeo_runs DROP COLUMN schedule_slot', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'aeo_runs' AND COLUMN_NAME = 'running_lock'), 'ALTER TABLE aeo_runs DROP COLUMN running_lock', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'aeo_runs' AND COLUMN_NAME = 'heartbeat_at'), 'ALTER TABLE aeo_runs DROP COLUMN heartbeat_at', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'aeo_runs' AND COLUMN_NAME = 'owner_instance'), 'ALTER TABLE aeo_runs DROP COLUMN owner_instance', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
