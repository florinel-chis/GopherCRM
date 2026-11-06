-- Migration: Record form mail deliveries
-- Date: 2026-11-06
-- Description: One row per form mail (double opt-in confirmation, follow-up,
-- one notification per notify_emails address), written pending in the
-- submission or confirmation transaction and stamped with the outcome after
-- the send (issue #216). The rows hold ids, statuses, error codes and an HMAC
-- of a notification recipient; never an address or message content. Columns
-- and index names match what GORM's AutoMigrate produces, and the table is
-- created only when missing, so this migration is safe on a database whose
-- application startup already created it. No foreign keys: the rows are
-- hard-deleted with their submission by the retention purge and by lead
-- erasure. Existing submissions are not backfilled. MySQL 8 and MariaDB 10.11
-- both accept this as written.

CREATE TABLE IF NOT EXISTS form_mail_deliveries (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    submission_id bigint unsigned NOT NULL,
    form_id bigint unsigned NOT NULL,
    kind varchar(20) NOT NULL,
    status varchar(20) NOT NULL,
    error_code varchar(50) NULL,
    recipient_hash varchar(64) NULL,
    attempts bigint NOT NULL DEFAULT 0,
    last_attempt_at datetime(3) NULL,
    sent_at datetime(3) NULL,
    PRIMARY KEY (id),
    KEY idx_form_mail_deliveries_deleted_at (deleted_at),
    KEY idx_form_mail_deliveries_submission_id (submission_id),
    KEY idx_form_mail_deliveries_form_id (form_id),
    KEY idx_form_mail_deliveries_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
