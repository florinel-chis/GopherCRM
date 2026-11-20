-- Migration: User identity source (#505)
-- Date: 2026-11-28
-- Description: An Entra ID first sign-in whose (iss, sub) is unknown finds the account SCIM created for that person through the identity SCIM create wrote as (issuer, externalId), when the token's tid equals the configured issuer's tenant segment and its oid equals that externalId. Adds source varchar(8) NOT NULL DEFAULT 'oidc' to user_identities only when missing, so this is safe after the application's AutoMigrate already added it; name, type, nullability and default match what AutoMigrate produces. Existing rows read oidc through the column default; SCIM create writes scim from now on. No index, no other change. MySQL 8 and MariaDB 10.11 accept this as written.

SET @preparedStatement = (SELECT IF(EXISTS(SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'user_identities' AND COLUMN_NAME = 'source'), 'SELECT 1', 'ALTER TABLE user_identities ADD COLUMN source varchar(8) NOT NULL DEFAULT ''oidc'''));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
