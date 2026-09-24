-- Migration: Widen api_keys.key_hash
-- Date: 2026-09-23
-- Description: API key hashes are "hmac$" plus 64 hex characters (69 in total).
-- varchar(64) rejected every new key on MySQL/MariaDB with Error 1406. The
-- unique index idx_api_keys_key_hash is kept by MODIFY.

ALTER TABLE api_keys MODIFY key_hash varchar(128) NOT NULL;
