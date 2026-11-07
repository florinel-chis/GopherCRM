-- Migration: Remove the ticket SLA fields
-- Date: 2026-11-08
-- Description: Drops tickets.first_responded_at and tickets.due_at, each only
-- when present. The tickets stay; their due dates and first-response stamps
-- are lost by this rollback. The application's AutoMigrate adds the columns
-- back (empty) on its next start.

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tickets' AND COLUMN_NAME = 'first_responded_at'
  ),
  'ALTER TABLE tickets DROP COLUMN first_responded_at',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tickets' AND COLUMN_NAME = 'due_at'
  ),
  'ALTER TABLE tickets DROP COLUMN due_at',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
