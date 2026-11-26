-- Migration: Remove form ticket title field (#804)
-- Date: 2026-11-29
-- Description: Drops the column ticket_title_field of forms, only when present, and nothing else. Every form row is kept; only the configured ticket title fields are lost, so tickets opened after the rollback carry the default title "Form: <form name>". MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'forms' AND COLUMN_NAME = 'ticket_title_field'), 'ALTER TABLE forms DROP COLUMN ticket_title_field', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
