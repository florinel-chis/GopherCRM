-- Migration: Webhook endpoint pause (#569)
-- Date: 2026-11-29
-- Description: An endpoint whose sent deliveries keep failing is paused with a growing back-off. Adds consecutive_failures int NOT NULL DEFAULT 0 (failed sends since the last success) and paused_until datetime(3) NULL (until when the sender skips the endpoint's due deliveries) to webhook_endpoints, each only when missing, so this is safe after the application's AutoMigrate already added them; names and types match what AutoMigrate produces. No backfill, no index: every existing endpoint reads 0 and NULL, the state of a healthy endpoint. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'webhook_endpoints' AND COLUMN_NAME = 'consecutive_failures'), 'SELECT 1', 'ALTER TABLE webhook_endpoints ADD COLUMN consecutive_failures int NOT NULL DEFAULT 0'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'webhook_endpoints' AND COLUMN_NAME = 'paused_until'), 'SELECT 1', 'ALTER TABLE webhook_endpoints ADD COLUMN paused_until datetime(3) NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
