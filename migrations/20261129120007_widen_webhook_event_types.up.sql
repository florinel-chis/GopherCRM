-- Migration: Widen webhook endpoint event types (#515)
-- Date: 2026-11-29
-- Description: Deals join the webhook catalogue (deal.created, deal.updated, deal.deleted), so an endpoint may subscribe to twelve event types; all twelve take 173 bytes. Widens webhook_endpoints.event_types from varchar(255) to varchar(512) NOT NULL, the type the application's AutoMigrate produces. The column keeps the table's character set and collation, every row is kept as it is, and running it again changes nothing. MySQL 8 and MariaDB 10.11 accept this as written.

ALTER TABLE webhook_endpoints MODIFY event_types varchar(512) NOT NULL;
