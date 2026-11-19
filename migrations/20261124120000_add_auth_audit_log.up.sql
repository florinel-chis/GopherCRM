-- Migration: Sign-in audit log
-- Date: 2026-11-24
-- Description: Sign-in audit log (issue #454). auth_audit_log holds one
-- pseudonymous row per password sign-in, single sign-on callback and refresh,
-- and per failed API-key authentication: the method, the outcome, a stable
-- reason code, the user id when known, the sign-in attempt and request ids,
-- an HMAC reference of a refused single sign-on identity, the role and group
-- claim values, the client's /24 or /48 network and a user-agent family from a
-- fixed vocabulary. No foreign key and no soft delete: rows age out after
-- AUTH_AUDIT_RETENTION_DAYS. Columns and index names match what GORM's
-- AutoMigrate produces, and the table is created only when missing, so this
-- migration is safe on a database whose application startup already created
-- it. No existing table is altered. MySQL 8 and MariaDB 10.11 both accept this
-- as written.

CREATE TABLE IF NOT EXISTS auth_audit_log (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NOT NULL,
    method varchar(16) NOT NULL,
    outcome varchar(16) NOT NULL,
    reason varchar(64) NOT NULL,
    user_id bigint unsigned NULL,
    attempt_id bigint unsigned NULL,
    request_id varchar(64) NOT NULL,
    subject_ref varchar(64) NULL,
    claim_roles text NULL,
    claim_groups text NULL,
    client_network varchar(64) NOT NULL,
    user_agent_family varchar(64) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_auth_audit_log_created_at (created_at),
    KEY idx_auth_audit_log_user_id (user_id),
    KEY idx_auth_audit_log_subject_ref (subject_ref)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
