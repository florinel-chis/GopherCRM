-- Migration: Remove refresh token provider session (#634)
-- Date: 2026-11-29
-- Description: Drops the index idx_refresh_tokens_provider_session_hash and the column provider_session_hash of refresh_tokens, each only when present, and nothing else. Every refresh token row is kept; only the recorded provider session references are lost, so a front-channel sign-out no longer finds the sessions that started before the rollback. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'refresh_tokens' AND INDEX_NAME = 'idx_refresh_tokens_provider_session_hash'), 'ALTER TABLE refresh_tokens DROP INDEX idx_refresh_tokens_provider_session_hash', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'refresh_tokens' AND COLUMN_NAME = 'provider_session_hash'), 'ALTER TABLE refresh_tokens DROP COLUMN provider_session_hash', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
