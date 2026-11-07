-- Migration: Remove the user token version
-- Date: 2026-11-09
-- Description: Drops users.token_version only when present. The accounts stay;
-- the counters are lost. The application's AutoMigrate adds the column back on
-- its next start with every account at 0, so an access token signed with a
-- higher value is refused and one that carried 0 (or no claim) is accepted
-- again until it expires, even if a logout had ended it.

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND COLUMN_NAME = 'token_version'
  ),
  'ALTER TABLE users DROP COLUMN token_version',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
