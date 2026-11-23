-- Migration: Refresh token provider session (#634)
-- Date: 2026-11-29
-- Description: Each refresh token of a single sign-on session records the identity provider's session it started in, so a front-channel sign-out from the provider can end it. Adds provider_session_hash varchar(64) NOT NULL DEFAULT '' (the keyed hash of the issuer and the ID token's sid claim; '' for password sessions and for sign-ins without a sid) to refresh_tokens, plus the index idx_refresh_tokens_provider_session_hash, each only when missing, so this is safe after the application's AutoMigrate already added them; the name, type, nullability, default and index match what AutoMigrate produces. Existing rows keep ''; nothing is filled. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'refresh_tokens' AND COLUMN_NAME = 'provider_session_hash'), 'SELECT 1', 'ALTER TABLE refresh_tokens ADD COLUMN provider_session_hash varchar(64) NOT NULL DEFAULT '''''));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'refresh_tokens' AND INDEX_NAME = 'idx_refresh_tokens_provider_session_hash'), 'SELECT 1', 'ALTER TABLE refresh_tokens ADD INDEX idx_refresh_tokens_provider_session_hash (provider_session_hash)'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
