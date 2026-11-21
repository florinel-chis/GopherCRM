-- Migration: Refresh token session (#546)
-- Date: 2026-11-29
-- Description: Each refresh token records the session it belongs to. Adds family_id varchar(64) NOT NULL DEFAULT '' (shared by every token of one rotation chain), sign_in_method varchar(16) NOT NULL DEFAULT '' (password or oidc) and session_started_at datetime(3) NULL (when that sign-in happened) to refresh_tokens, plus the index idx_refresh_tokens_family_id, each only when missing, so this is safe after the application's AutoMigrate already added them; names, types, nullability and defaults match what AutoMigrate produces. Then fills every row that has no value yet, revoked and soft-deleted rows included: family_id is the row's own id as a decimal string, sign_in_method is oidc when the owner has any user_identities row and password otherwise, and session_started_at is created_at. The fills are plain UPDATEs, so updated_at is not touched and running this again changes nothing. The application's startup step does the same on every driver. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'refresh_tokens' AND COLUMN_NAME = 'family_id'), 'SELECT 1', 'ALTER TABLE refresh_tokens ADD COLUMN family_id varchar(64) NOT NULL DEFAULT '''''));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'refresh_tokens' AND COLUMN_NAME = 'sign_in_method'), 'SELECT 1', 'ALTER TABLE refresh_tokens ADD COLUMN sign_in_method varchar(16) NOT NULL DEFAULT '''''));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'refresh_tokens' AND COLUMN_NAME = 'session_started_at'), 'SELECT 1', 'ALTER TABLE refresh_tokens ADD COLUMN session_started_at datetime(3) NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'refresh_tokens' AND INDEX_NAME = 'idx_refresh_tokens_family_id'), 'SELECT 1', 'ALTER TABLE refresh_tokens ADD INDEX idx_refresh_tokens_family_id (family_id)'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

UPDATE refresh_tokens SET family_id = CAST(id AS CHAR) WHERE family_id = '';
UPDATE refresh_tokens SET sign_in_method = 'oidc' WHERE sign_in_method = '' AND user_id IN (SELECT user_id FROM user_identities);
UPDATE refresh_tokens SET sign_in_method = 'password' WHERE sign_in_method = '';
UPDATE refresh_tokens SET session_started_at = created_at WHERE session_started_at IS NULL;
