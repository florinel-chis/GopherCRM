-- Migration: Remove quote history source (#400)
-- Restores quote_status_changes.changed_by_id NOT NULL and drops source. First every row without an acting user (a client answer) gets the owner_id of its quote, soft-deleted quotes included, which is what such a row named before #400; then changed_by_id goes back to NOT NULL only when it is nullable, and source is dropped only when present. Every row is kept; which rows were client answers is lost. A row without a user whose quote row no longer exists stops the rollback at the NOT NULL step, before source is dropped.

UPDATE quote_status_changes c JOIN quotes q ON q.id = c.quote_id SET c.changed_by_id = q.owner_id WHERE c.changed_by_id IS NULL;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_status_changes' AND COLUMN_NAME = 'changed_by_id' AND IS_NULLABLE = 'YES'), 'ALTER TABLE quote_status_changes MODIFY COLUMN changed_by_id bigint unsigned NOT NULL', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_status_changes' AND COLUMN_NAME = 'source'), 'ALTER TABLE quote_status_changes DROP COLUMN source', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
