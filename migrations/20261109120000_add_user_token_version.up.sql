-- Migration: User token version
-- Date: 2026-11-09
-- Description: The per-account session generation users.token_version (#236).
-- Every access JWT carries it as the token_version claim and is refused once
-- the stored value differs; logout, a password change and a password reset
-- increment it. Existing accounts read 0, which is also what a token signed
-- without the claim reads, so no session ends because of this migration. No
-- index and no backfill. The column type matches what GORM's AutoMigrate
-- produces for the uint field, and it is added only when missing, so this
-- migration is safe on a database whose application startup already added it.
-- MySQL 8 and MariaDB 10.11 both accept this as written.

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND COLUMN_NAME = 'token_version'
  ),
  'SELECT 1',
  'ALTER TABLE users ADD COLUMN token_version bigint unsigned NOT NULL DEFAULT 0'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
