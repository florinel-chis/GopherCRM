-- Migration: Bulk operation status index
-- Date: 2026-11-10
-- Description: The index idx_bulk_operations_status (not unique) on
-- bulk_operations(status) (#196). The stale-pending sweep, which every replica
-- runs at boot and every 15 minutes, finds the pending rows older than an hour
-- through it with a plain non-locking SELECT and then updates them by primary
-- key, so it takes no lock through this index. The name and the column match
-- what GORM's AutoMigrate creates from the model tag, and the index is created
-- only when missing, so this migration is safe on a database whose application
-- startup already added it. MySQL 8 and MariaDB 10.11 build a secondary index
-- as in-place online DDL at the cost of one scan of the table; the CREATE INDEX
-- takes a brief exclusive metadata lock at its start and end, so run it when no
-- long transaction is open on bulk_operations. No row is changed.

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'bulk_operations' AND INDEX_NAME = 'idx_bulk_operations_status'
  ),
  'SELECT 1',
  'CREATE INDEX idx_bulk_operations_status ON bulk_operations (status)'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
