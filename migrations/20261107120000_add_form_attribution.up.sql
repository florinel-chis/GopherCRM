-- Migration: Form attribution and the form-submitted event
-- Date: 2026-11-07
-- Description: Six attribution columns on form_submissions (utm_source,
-- utm_medium, utm_campaign, utm_term, utm_content, referrer_host), each NOT NULL
-- DEFAULT '' so existing rows read as having no attribution, and the nullable
-- automation_events.form_id and submission_id carried by lead_form_submitted
-- (#220). No index, no constraint and no backfill: existing submissions and
-- events keep empty values. The column names and types match what GORM's
-- AutoMigrate produces, and each column is added only when missing, so this
-- migration is safe on a database whose application startup already added
-- them. MySQL 8 and MariaDB 10.11 both accept this as written.

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'form_submissions' AND COLUMN_NAME = 'utm_source'
  ),
  'SELECT 1',
  'ALTER TABLE form_submissions ADD COLUMN utm_source varchar(100) NOT NULL DEFAULT \'\''
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'form_submissions' AND COLUMN_NAME = 'utm_medium'
  ),
  'SELECT 1',
  'ALTER TABLE form_submissions ADD COLUMN utm_medium varchar(100) NOT NULL DEFAULT \'\''
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'form_submissions' AND COLUMN_NAME = 'utm_campaign'
  ),
  'SELECT 1',
  'ALTER TABLE form_submissions ADD COLUMN utm_campaign varchar(100) NOT NULL DEFAULT \'\''
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'form_submissions' AND COLUMN_NAME = 'utm_term'
  ),
  'SELECT 1',
  'ALTER TABLE form_submissions ADD COLUMN utm_term varchar(100) NOT NULL DEFAULT \'\''
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'form_submissions' AND COLUMN_NAME = 'utm_content'
  ),
  'SELECT 1',
  'ALTER TABLE form_submissions ADD COLUMN utm_content varchar(100) NOT NULL DEFAULT \'\''
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'form_submissions' AND COLUMN_NAME = 'referrer_host'
  ),
  'SELECT 1',
  'ALTER TABLE form_submissions ADD COLUMN referrer_host varchar(255) NOT NULL DEFAULT \'\''
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'automation_events' AND COLUMN_NAME = 'form_id'
  ),
  'SELECT 1',
  'ALTER TABLE automation_events ADD COLUMN form_id bigint unsigned NULL'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'automation_events' AND COLUMN_NAME = 'submission_id'
  ),
  'SELECT 1',
  'ALTER TABLE automation_events ADD COLUMN submission_id bigint unsigned NULL'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
