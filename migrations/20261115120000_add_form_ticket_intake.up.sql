-- Migration: Form ticket intake (#213). Adds forms.create_ticket, three form_submissions columns plus an index, and the form_ticket_keys table, each only when missing; names match GORM AutoMigrate. MySQL 8 and MariaDB 10.11.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'forms' AND COLUMN_NAME = 'create_ticket'), 'SELECT 1', 'ALTER TABLE forms ADD COLUMN create_ticket tinyint(1) NOT NULL DEFAULT 0'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'form_submissions' AND COLUMN_NAME = 'idempotency_key'), 'SELECT 1', 'ALTER TABLE form_submissions ADD COLUMN idempotency_key varchar(128) NOT NULL DEFAULT '''''));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'form_submissions' AND COLUMN_NAME = 'ticket_id'), 'SELECT 1', 'ALTER TABLE form_submissions ADD COLUMN ticket_id bigint unsigned NULL'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'form_submissions' AND COLUMN_NAME = 'ticket_outcome'), 'SELECT 1', 'ALTER TABLE form_submissions ADD COLUMN ticket_outcome varchar(20) NOT NULL DEFAULT '''''));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'form_submissions' AND INDEX_NAME = 'idx_form_submissions_ticket_id'), 'SELECT 1', 'CREATE INDEX idx_form_submissions_ticket_id ON form_submissions (ticket_id)'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

CREATE TABLE IF NOT EXISTS form_ticket_keys (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    form_id bigint unsigned NOT NULL,
    idempotency_key varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    ticket_id bigint unsigned NOT NULL,
    submission_id bigint unsigned NOT NULL,
    customer_id bigint unsigned NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_form_ticket_keys_identity (form_id, idempotency_key),
    KEY idx_form_ticket_keys_ticket_id (ticket_id),
    KEY idx_form_ticket_keys_submission_id (submission_id),
    KEY idx_form_ticket_keys_customer_id (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
