-- Fails while any HMAC hash (69 characters) is stored: such keys cannot be
-- represented in the old column and must be revoked first.
ALTER TABLE api_keys MODIFY key_hash varchar(64) NOT NULL;
