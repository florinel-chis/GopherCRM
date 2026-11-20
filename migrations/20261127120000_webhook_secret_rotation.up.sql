-- Migration: Webhook signing-secret rotation (#503)
-- Date: 2026-11-27
-- Description: POST /webhooks/endpoints/{id}/rotate-secret keeps the replaced signing secret, sealed, for a 24-hour overlap window in which every request carries a signature under each secret. Adds previous_secret_sealed varchar(255) NULL and previous_secret_expires_at datetime(3) NULL to webhook_endpoints, each only when missing, so this is safe after the application's AutoMigrate already added them; names and types match what AutoMigrate produces. No backfill, no index: every existing endpoint reads NULL in both and signs with its one secret. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'webhook_endpoints' AND COLUMN_NAME = 'previous_secret_sealed'), 'SELECT 1', 'ALTER TABLE webhook_endpoints ADD COLUMN previous_secret_sealed varchar(255) NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'webhook_endpoints' AND COLUMN_NAME = 'previous_secret_expires_at'), 'SELECT 1', 'ALTER TABLE webhook_endpoints ADD COLUMN previous_secret_expires_at datetime(3) NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
