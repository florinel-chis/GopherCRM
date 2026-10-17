-- Migration: Rollback v1.2.0 Schema Parity
-- Date: 2026-09-28
-- Description: Drops tables and columns added by the v1.2.0 schema parity migration
-- in reverse dependency order.

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

ALTER TABLE configurations DROP COLUMN is_sensitive;

ALTER TABLE leads DROP COLUMN external_id;
ALTER TABLE leads DROP COLUMN classification;

ALTER TABLE customers DROP FOREIGN KEY fk_customers_assigned_to;
ALTER TABLE customers DROP KEY idx_customers_assigned_to_id;
ALTER TABLE customers DROP COLUMN assigned_to_id;

ALTER TABLE users DROP COLUMN locked_until;
ALTER TABLE users DROP COLUMN failed_login_attempts;
