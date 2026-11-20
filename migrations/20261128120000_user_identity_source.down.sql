-- Migration: Remove user identity source (#505)
-- Date: 2026-11-28
-- Description: Drops the column source of user_identities, only when present, and nothing else. Every identity row is kept. After a rollback the rows SCIM create wrote cannot be told apart from the rows a sign-in wrote, so a later Entra ID first sign-in no longer finds a SCIM-created account by its oid and falls back to provisioning or refusal as before #505. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'user_identities' AND COLUMN_NAME = 'source'), 'ALTER TABLE user_identities DROP COLUMN source', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
