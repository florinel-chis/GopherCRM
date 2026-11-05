-- Migration: Link tasks to deals
-- Date: 2026-11-01
-- Description: A nullable tasks.deal_id with its index and foreign key, additive
-- to the lead-or-customer link. Every existing task keeps deal_id NULL: tasks
-- created by earlier automation runs are not backfilled. The column, index and
-- constraint names match what GORM's AutoMigrate produces, and each object is
-- added only when missing, so this migration is safe on a database whose
-- application startup already added them. The index is created before the
-- foreign key so that InnoDB does not create an implicit one under the
-- constraint's name. MySQL 8 and MariaDB 10.11 both accept this as written.

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND COLUMN_NAME = 'deal_id'
  ),
  'SELECT 1',
  'ALTER TABLE tasks ADD COLUMN deal_id bigint unsigned NULL'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND INDEX_NAME = 'idx_tasks_deal_id'
  ),
  'SELECT 1',
  'CREATE INDEX idx_tasks_deal_id ON tasks (deal_id)'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks'
      AND CONSTRAINT_NAME = 'fk_tasks_deal' AND CONSTRAINT_TYPE = 'FOREIGN KEY'
  ),
  'SELECT 1',
  'ALTER TABLE tasks ADD CONSTRAINT fk_tasks_deal FOREIGN KEY (deal_id) REFERENCES deals (id)'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
