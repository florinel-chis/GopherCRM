-- Migration: Remove outbound webhooks
-- #345: drops the two webhook tables and nothing else.

DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_endpoints;
