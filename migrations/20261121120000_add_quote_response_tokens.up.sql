-- Migration: Quote response tokens
-- #382: the single-use tokens behind the client response link of a quote
-- email. A row holds the HMAC-SHA256 hash of the token, the quote id, the
-- expiry, when it was spent and the client's decision (no personal data). No
-- referential constraint, like quote_status_changes. The table is created
-- only when missing, with the shape models.MigrateDatabase builds.

CREATE TABLE IF NOT EXISTS quote_response_tokens (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    quote_id bigint unsigned NOT NULL,
    token_hash varchar(255) NOT NULL,
    expires_at datetime(3) NULL,
    used_at datetime(3) NULL,
    decision varchar(20) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_quote_response_tokens_token_hash (token_hash),
    KEY idx_quote_response_tokens_quote_id (quote_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
