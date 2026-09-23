-- API key hashes are "hmac$" + 64 hex characters (69 in total). varchar(64)
-- rejected every new key on MySQL/MariaDB with Error 1406.
ALTER TABLE api_keys MODIFY key_hash varchar(128) NOT NULL;
