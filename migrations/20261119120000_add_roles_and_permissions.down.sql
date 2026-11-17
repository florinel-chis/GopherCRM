-- Migration: Remove RBAC roles and permissions (#259 S2, #344).
-- Rollback (owner decision 8): every live custom-role user becomes an inactive
-- support user first, so no account keeps a role the older binary does not
-- know; erased (soft-deleted) rows and every other user are left alone. This
-- is irreversible for those users: take a backup before running it.
-- Then fk_users_custom_role, idx_users_role_id, users.role_id, idx_users_role
-- and the three role tables are dropped, each only when present.

UPDATE users SET role = 'support', is_active = 0 WHERE role = 'custom' AND deleted_at IS NULL;

SET @preparedStatement = (SELECT IF((SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND CONSTRAINT_NAME = 'fk_users_custom_role' AND CONSTRAINT_TYPE = 'FOREIGN KEY') > 0, 'ALTER TABLE users DROP FOREIGN KEY fk_users_custom_role', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF((SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND INDEX_NAME = 'idx_users_role_id') > 0, 'DROP INDEX idx_users_role_id ON users', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF((SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND COLUMN_NAME = 'role_id') > 0, 'ALTER TABLE users DROP COLUMN role_id', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF((SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND INDEX_NAME = 'idx_users_role') > 0, 'DROP INDEX idx_users_role ON users', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS role_audit_log;
DROP TABLE IF EXISTS roles;
