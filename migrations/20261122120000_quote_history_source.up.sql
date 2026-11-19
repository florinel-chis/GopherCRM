-- Migration: Quote history source (#400)
-- A client's answer through the response link of a quote email is written to quote_status_changes without an acting user. Adds source varchar(20) NOT NULL DEFAULT 'staff' (staff or client) only when missing, and makes changed_by_id nullable only when it is still NOT NULL (safe after AutoMigrate); the foreign key fk_quote_status_changes_changed_by is kept, so a non-NULL value must still be a user. No backfill: every existing row keeps its changed_by_id and reads source staff, including a client answer recorded before this migration, which keeps naming the quote owner. No index. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_status_changes' AND COLUMN_NAME = 'source'), 'SELECT 1', 'ALTER TABLE quote_status_changes ADD COLUMN source varchar(20) NOT NULL DEFAULT ''staff'''));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quote_status_changes' AND COLUMN_NAME = 'changed_by_id' AND IS_NULLABLE = 'NO'), 'ALTER TABLE quote_status_changes MODIFY COLUMN changed_by_id bigint unsigned NULL', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
