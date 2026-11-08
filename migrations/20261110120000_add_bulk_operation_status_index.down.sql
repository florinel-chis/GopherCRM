-- Migration: Remove the bulk operation status index
-- Date: 2026-11-10
-- Description: Drops idx_bulk_operations_status only when present. No row is
-- changed. The stale-pending sweep keeps working without the index, reading
-- the table in full; the application's AutoMigrate adds the index back on its
-- next start.

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bulk_operations' AND INDEX_NAME = 'idx_bulk_operations_status'
  ),
  'DROP INDEX idx_bulk_operations_status ON bulk_operations',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
