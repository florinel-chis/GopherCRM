-- Migration: Unlink tasks from deals
-- Date: 2026-11-01
-- Description: Removes the foreign key, the index and tasks.deal_id, in that
-- order, each only when present. The task rows stay; the links they held are
-- business data and are lost by this rollback. The application's AutoMigrate
-- adds the three objects back on its next start.

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks'
      AND CONSTRAINT_NAME = 'fk_tasks_deal' AND CONSTRAINT_TYPE = 'FOREIGN KEY'
  ),
  'ALTER TABLE tasks DROP FOREIGN KEY fk_tasks_deal',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND INDEX_NAME = 'idx_tasks_deal_id'
  ),
  'DROP INDEX idx_tasks_deal_id ON tasks',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND COLUMN_NAME = 'deal_id'
  ),
  'ALTER TABLE tasks DROP COLUMN deal_id',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
