-- Migration: Narrow api_keys.key_hash back to varchar(64)
-- Date: 2026-09-23
-- Description: Only safe while no HMAC hash (69 characters) is stored. Under a
-- strict sql_mode (the default on MySQL 5.7+, MySQL 8 and MariaDB 10.2.4+) the
-- ALTER fails if any such row exists; with strict mode off the server would
-- silently truncate those hashes and break the keys. Revoke HMAC keys first.

ALTER TABLE api_keys MODIFY key_hash varchar(64) NOT NULL;
