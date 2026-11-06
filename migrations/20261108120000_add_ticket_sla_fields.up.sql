-- Migration: Ticket SLA fields
-- Date: 2026-11-08
-- Description: Two nullable timestamps on tickets (#278): due_at, which admin
-- and support users set through the API, and first_responded_at, which the
-- server stamps with the created_at of the first non-internal staff comment.
-- No index, no constraint and no backfill: existing tickets read NULL for both.
-- The overdue flag is computed at read time and has no column. The column
-- names and types match what GORM's AutoMigrate produces, and each column is
-- added only when missing, so this migration is safe on a database whose
-- application startup already added them. MySQL 8 and MariaDB 10.11 both
-- accept this as written.

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tickets' AND COLUMN_NAME = 'due_at'
  ),
  'SELECT 1',
  'ALTER TABLE tickets ADD COLUMN due_at datetime(3) NULL'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tickets' AND COLUMN_NAME = 'first_responded_at'
  ),
  'SELECT 1',
  'ALTER TABLE tickets ADD COLUMN first_responded_at datetime(3) NULL'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
