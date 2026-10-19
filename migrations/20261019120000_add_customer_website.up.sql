-- Safe when application startup has already added the column via AutoMigrate.
SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'customers' AND COLUMN_NAME = 'website'
  ),
  'SELECT 1',
  'ALTER TABLE customers ADD COLUMN website varchar(255) NOT NULL DEFAULT \'\''
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
