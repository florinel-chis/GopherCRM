-- Migration: Form ticket title field (#804)
-- Date: 2026-11-29
-- Description: A form that creates tickets may name one text or select field whose submitted value becomes the ticket title "<form name>: <value>". Adds ticket_title_field varchar(50) NOT NULL DEFAULT '' (the field name; '' keeps the default title "Form: <form name>") to forms, only when missing, so this is safe after the application's AutoMigrate already added it; the name, type, nullability and default match what AutoMigrate produces. Existing rows keep ''; nothing is filled. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'forms' AND COLUMN_NAME = 'ticket_title_field'), 'SELECT 1', 'ALTER TABLE forms ADD COLUMN ticket_title_field varchar(50) NOT NULL DEFAULT '''''));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
