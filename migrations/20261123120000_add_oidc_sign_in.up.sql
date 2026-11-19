-- Migration: OIDC sign-in
-- Date: 2026-11-23
-- Description: OpenID Connect sign-in (issue #435). user_identities links the
-- exact (issuer, subject) pair of an ID token to a user: the pair is unique and
-- both columns carry utf8mb4_bin, so values differing only in letter case are
-- distinct. user_id references users (ON DELETE RESTRICT); there is no
-- deleted_at, and erasure keeps the row linked to its erased user.
-- oidc_login_attempts holds one row per started sign-in: the HMAC of the
-- state (unique), the nonce, the PKCE verifier, the issuer it was started
-- against, the expiry and the single-use stamp; no foreign key. Columns, index
-- names and the foreign-key name match what GORM's AutoMigrate produces plus
-- the collation guard of models.MigrateDatabase, and both tables are created
-- only when missing, so this migration is safe on a database whose application
-- startup already created them. No existing table is altered. MySQL 8 and
-- MariaDB 10.11 both accept this as written.

CREATE TABLE IF NOT EXISTS user_identities (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    user_id bigint unsigned NOT NULL,
    issuer varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    subject varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_user_identities_issuer_subject (issuer, subject),
    KEY idx_user_identities_user_id (user_id),
    CONSTRAINT fk_user_identities_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS oidc_login_attempts (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    state_hash varchar(64) NOT NULL,
    nonce varchar(64) NOT NULL,
    code_verifier varchar(128) NOT NULL,
    issuer varchar(2048) NOT NULL,
    expires_at datetime(3) NOT NULL,
    used_at datetime(3) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_oidc_login_attempts_state_hash (state_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
