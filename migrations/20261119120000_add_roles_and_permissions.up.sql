-- Migration: RBAC roles and permissions (#259 S2, #344).
-- Creates roles, role_permissions and role_audit_log, seeds the four system
-- roles with the names and descriptions of models.SystemRoleSeeds, and adds
-- users.role_id with idx_users_role, idx_users_role_id and
-- fk_users_custom_role (ON DELETE RESTRICT). Names match GORM AutoMigrate, so
-- a schema the application already migrated reaches the same objects; every
-- ALTER is guarded by an INFORMATION_SCHEMA probe, and the index is created
-- before the foreign key so MySQL does not add one of its own.
-- The timestamp moved from #259's 20261101120000, which add_task_deal_link holds.

CREATE TABLE IF NOT EXISTS roles (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    system_key varchar(20) NULL,
    name varchar(100) NOT NULL,
    name_key varchar(100) NOT NULL,
    description varchar(500) NOT NULL DEFAULT '',
    PRIMARY KEY (id),
    UNIQUE KEY idx_roles_system_key (system_key),
    UNIQUE KEY idx_roles_name_key (name_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS role_permissions (
    role_id bigint unsigned NOT NULL,
    permission_key varchar(100) NOT NULL,
    scope varchar(10) NOT NULL DEFAULT 'all',
    PRIMARY KEY (role_id, permission_key),
    CONSTRAINT fk_roles_permissions FOREIGN KEY (role_id) REFERENCES roles (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS role_audit_log (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NOT NULL,
    actor_user_id bigint unsigned NULL,
    auth_method varchar(20) NOT NULL DEFAULT '',
    action varchar(40) NOT NULL,
    role_id bigint unsigned NULL,
    role_name varchar(100) NOT NULL DEFAULT '',
    target_user_id bigint unsigned NULL,
    before_state text NULL,
    after_state text NULL,
    PRIMARY KEY (id),
    KEY idx_role_audit_log_created_at (created_at),
    KEY idx_role_audit_log_role_id (role_id),
    KEY idx_role_audit_log_target_user_id (target_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT IGNORE INTO roles (created_at, updated_at, system_key, name, name_key, description) VALUES
    (UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), 'admin', 'Admin', 'admin', 'Full access. Cannot be edited or deleted.'),
    (UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), 'sales', 'Sales', 'sales', 'Built-in sales access. Cannot be edited or deleted.'),
    (UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), 'support', 'Support', 'support', 'Built-in support access. Cannot be edited or deleted.'),
    (UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), 'customer', 'Customer', 'customer', 'A customer''s own login. Cannot be edited, deleted or cloned into portal access.');

SET @preparedStatement = (SELECT IF((SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND COLUMN_NAME = 'role_id') = 0, 'ALTER TABLE users ADD COLUMN role_id bigint unsigned NULL', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF((SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND INDEX_NAME = 'idx_users_role') = 0, 'CREATE INDEX idx_users_role ON users (role)', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF((SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND INDEX_NAME = 'idx_users_role_id') = 0, 'CREATE INDEX idx_users_role_id ON users (role_id)', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF((SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND CONSTRAINT_NAME = 'fk_users_custom_role' AND CONSTRAINT_TYPE = 'FOREIGN KEY') = 0, 'ALTER TABLE users ADD CONSTRAINT fk_users_custom_role FOREIGN KEY (role_id) REFERENCES roles (id) ON DELETE RESTRICT', 'SELECT 1'));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
