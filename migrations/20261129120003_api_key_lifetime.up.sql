-- Migration: API key lifetime (#532)
-- Date: 2026-11-29
-- Description: Every API key expires within one year, and customer accounts hold no API keys. Gives every key without an expiry, or with one later than one year from now, the expiry NOW(3) + 1 year, and revokes every key of a customer account that is not revoked yet (is_active 0, revoked_at NOW(3)); a key already revoked keeps its revoked_at. Soft-deleted rows are included. NOW(3) is used because stored times are in the application's local zone (loc=Local); this assumes the database server runs in the same zone as the application. Both statements are plain UPDATEs, so updated_at is not touched and running this again changes nothing. No schema change. The application's startup step does the same on every driver. MySQL 8 and MariaDB 10.11 accept this as written.

UPDATE api_keys SET expires_at = NOW(3) + INTERVAL 1 YEAR WHERE expires_at IS NULL OR expires_at > NOW(3) + INTERVAL 1 YEAR;
UPDATE api_keys SET is_active = 0, revoked_at = NOW(3) WHERE revoked_at IS NULL AND user_id IN (SELECT id FROM users WHERE role = 'customer');
