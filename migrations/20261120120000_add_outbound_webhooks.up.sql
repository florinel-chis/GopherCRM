-- Migration: Outbound webhooks
-- #345: the admin endpoint registry and the delivery outbox/log. Both tables
-- hold ids, event types, statuses and timestamps only (no personal data); the
-- endpoint secret is stored sealed. No foreign keys: an endpoint delete
-- removes its delivery rows in the same transaction. Each table is created
-- only when missing, with the shape models.MigrateDatabase builds.

CREATE TABLE IF NOT EXISTS webhook_endpoints (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    url varchar(2048) NOT NULL,
    event_types varchar(255) NOT NULL,
    secret_sealed varchar(255) NOT NULL,
    is_active tinyint(1) NOT NULL,
    PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    endpoint_id bigint unsigned NOT NULL,
    event_id varchar(64) NOT NULL,
    event_type varchar(32) NOT NULL,
    record_id bigint unsigned NOT NULL,
    occurred_at datetime(3) NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'pending',
    attempts int NOT NULL DEFAULT 0,
    next_attempt_at datetime(3) NULL,
    last_attempt_at datetime(3) NULL,
    lease_until datetime(3) NULL,
    last_http_status int NULL,
    error_code varchar(50) NOT NULL DEFAULT '',
    PRIMARY KEY (id),
    UNIQUE KEY idx_webhook_deliveries_endpoint_event (endpoint_id, event_id),
    KEY idx_webhook_deliveries_status_next (status, next_attempt_at),
    KEY idx_webhook_deliveries_updated_at (updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
