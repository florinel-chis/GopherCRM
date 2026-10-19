-- Migration: Rollback v1.2.0 Schema Parity
-- Date: 2026-10-01
-- Description: Drops tables and columns added by the v1.2.0 schema parity migration
-- in reverse dependency order (idempotent: safe if already absent).

DROP TABLE IF EXISTS form_confirmation_tokens;
DROP TABLE IF EXISTS form_submissions;
DROP TABLE IF EXISTS forms;
DROP TABLE IF EXISTS aeo_citations;
DROP TABLE IF EXISTS aeo_answers;
DROP TABLE IF EXISTS aeo_runs;
DROP TABLE IF EXISTS aeo_prompts;
DROP TABLE IF EXISTS aeo_profiles;
DROP TABLE IF EXISTS bulk_operation_items;
DROP TABLE IF EXISTS bulk_operations;
DROP TABLE IF EXISTS password_reset_tokens;
DROP TABLE IF EXISTS task_labels;
DROP TABLE IF EXISTS labels;

SET @dbname = DATABASE();

-- Drop fk_customers_assigned_to if it exists
SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
    WHERE TABLE_SCHEMA = @dbname AND TABLE_NAME = 'customers' AND CONSTRAINT_NAME = 'fk_customers_assigned_to'
  ),
  'ALTER TABLE customers DROP FOREIGN KEY fk_customers_assigned_to',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Drop idx_customers_assigned_to_id if it exists
SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = @dbname AND TABLE_NAME = 'customers' AND INDEX_NAME = 'idx_customers_assigned_to_id'
  ),
  'ALTER TABLE customers DROP KEY idx_customers_assigned_to_id',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Drop customers.assigned_to_id if it exists
SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = @dbname AND TABLE_NAME = 'customers' AND COLUMN_NAME = 'assigned_to_id'
  ),
  'ALTER TABLE customers DROP COLUMN assigned_to_id',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Drop configurations.is_sensitive if it exists
SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = @dbname AND TABLE_NAME = 'configurations' AND COLUMN_NAME = 'is_sensitive'
  ),
  'ALTER TABLE configurations DROP COLUMN is_sensitive',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Drop leads.external_id if it exists
SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = @dbname AND TABLE_NAME = 'leads' AND COLUMN_NAME = 'external_id'
  ),
  'ALTER TABLE leads DROP COLUMN external_id',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Drop leads.classification if it exists
SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = @dbname AND TABLE_NAME = 'leads' AND COLUMN_NAME = 'classification'
  ),
  'ALTER TABLE leads DROP COLUMN classification',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Drop users.locked_until if it exists
SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = @dbname AND TABLE_NAME = 'users' AND COLUMN_NAME = 'locked_until'
  ),
  'ALTER TABLE users DROP COLUMN locked_until',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- Drop users.failed_login_attempts if it exists
SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.COLUMNS
    WHERE TABLE_SCHEMA = @dbname AND TABLE_NAME = 'users' AND COLUMN_NAME = 'failed_login_attempts'
  ),
  'ALTER TABLE users DROP COLUMN failed_login_attempts',
  'SELECT 1'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
