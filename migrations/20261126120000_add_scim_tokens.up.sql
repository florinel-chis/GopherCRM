-- Migration: SCIM provider tokens
-- Date: 2026-11-26
-- Description: SCIM 2.0 provider tokens (issue #468). scim_tokens holds one
-- row per bearer token an identity provider uses on /scim/v2: a label, the
-- HMAC-SHA256 of the token (keyed like API keys, never the token itself), when
-- it was created, last used and revoked. No foreign key and no soft delete: a
-- token is revoked, never deleted. Columns and index names match what GORM's
-- AutoMigrate produces, and the table is created only when missing, so this
-- migration is safe on a database whose application startup already created
-- it. No existing table is altered. MySQL 8 and MariaDB 10.11 both accept this
-- as written.

CREATE TABLE IF NOT EXISTS scim_tokens (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    name varchar(100) NOT NULL,
    token_hash varchar(128) NOT NULL,
    created_at datetime(3) NOT NULL,
    last_used_at datetime(3) NULL,
    revoked_at datetime(3) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_scim_tokens_token_hash (token_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
