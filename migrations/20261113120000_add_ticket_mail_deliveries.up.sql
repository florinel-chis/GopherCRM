-- Migration: Ticket comment mail deliveries
-- Date: 2026-11-13
-- Description: One row per ticket comment a staff user asked to email to the
-- ticket's customer (issue #219). Holds ids, a status, an error code and
-- timestamps; no address and no content. Hard-deleted with its ticket and when
-- the customer is erased, so there is no deleted_at, and no foreign key.
-- Columns and index names match GORM's AutoMigrate; the table is created only
-- when missing, so this migration is safe on a database whose application
-- startup already created it. MySQL 8 and MariaDB 10.11 accept this as written.

CREATE TABLE IF NOT EXISTS ticket_mail_deliveries (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    ticket_id bigint unsigned NOT NULL,
    comment_id bigint unsigned NOT NULL,
    customer_id bigint unsigned NOT NULL,
    status varchar(20) NOT NULL,
    error_code varchar(50) NOT NULL DEFAULT '',
    sent_at datetime(3) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_ticket_mail_deliveries_comment_id (comment_id),
    KEY idx_ticket_mail_deliveries_ticket_id (ticket_id),
    KEY idx_ticket_mail_deliveries_customer_id (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
