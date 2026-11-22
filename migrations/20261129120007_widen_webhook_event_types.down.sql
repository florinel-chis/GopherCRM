-- Migration: Narrow webhook endpoint event types (#515)
-- Date: 2026-11-29
-- Description: Narrows webhook_endpoints.event_types back to varchar(255) NOT NULL. Nothing is truncated: while any endpoint's event_types is longer than 255 bytes, this down migration fails with an unknown-column error naming webhook_event_types_longer_than_255_bytes_block_this_rollback, under any sql_mode, and changes nothing. The operator must first shorten those endpoints' subscriptions (PUT /webhooks/endpoints/{id} with fewer event types) and then run it again. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM webhook_endpoints WHERE LENGTH(event_types) > 255), 'SELECT webhook_event_types_longer_than_255_bytes_block_this_rollback FROM webhook_endpoints', 'ALTER TABLE webhook_endpoints MODIFY event_types varchar(255) NOT NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
