-- Migration: Admin-only single sign-on and SCIM permissions (#524)
-- Date: 2026-11-29
-- Description: settings.sso_manage and settings.scim_manage are held by the
-- admin system role only. This removes both keys from every custom role
-- (system_key IS NULL) and writes one role_audit_log row per role it changes:
-- action role_updated, auth_method migration, actor_user_id and
-- target_user_id NULL, the role's id and name, and before_state/after_state
-- holding the name, the description and the stored permission rows sorted by
-- key (byte order, as the application sorts them) before and after. The
-- application does the same on every start (models.MigrateDatabase, step
-- stripAdminOnlyRolePermissions), so this migration is safe on a database the
-- new release has already booted against: a role without the keys gets no
-- row. created_at is UTC_TIMESTAMP(3), the UTC clock the application writes
-- audit rows with, so GET /roles/audit orders these rows next to its own.
-- group_concat_max_len is raised for the session: the 1024 default would
-- truncate the state of a role holding most of the catalogue. MySQL 8 and
-- MariaDB 10.11 both accept this as written.

SET SESSION group_concat_max_len = 1048576;

START TRANSACTION;

INSERT INTO role_audit_log
    (created_at, actor_user_id, auth_method, action, role_id, role_name, target_user_id, before_state, after_state)
SELECT
    UTC_TIMESTAMP(3), NULL, 'migration', 'role_updated', r.id, r.name, NULL,
    JSON_OBJECT('name', r.name, 'description', r.description, 'permissions', JSON_EXTRACT(COALESCE(CONCAT('[', (
        SELECT GROUP_CONCAT(JSON_OBJECT('key', b.permission_key, 'scope', b.scope)
            ORDER BY b.permission_key COLLATE utf8mb4_bin SEPARATOR ',')
        FROM role_permissions b
        WHERE b.role_id = r.id
    ), ']'), '[]'), '$')),
    JSON_OBJECT('name', r.name, 'description', r.description, 'permissions', JSON_EXTRACT(COALESCE(CONCAT('[', (
        SELECT GROUP_CONCAT(JSON_OBJECT('key', a.permission_key, 'scope', a.scope)
            ORDER BY a.permission_key COLLATE utf8mb4_bin SEPARATOR ',')
        FROM role_permissions a
        WHERE a.role_id = r.id AND a.permission_key NOT IN ('settings.scim_manage', 'settings.sso_manage')
    ), ']'), '[]'), '$'))
FROM roles r
WHERE r.system_key IS NULL
  AND EXISTS (
    SELECT 1 FROM role_permissions k
    WHERE k.role_id = r.id AND k.permission_key IN ('settings.scim_manage', 'settings.sso_manage')
  )
ORDER BY r.id;

DELETE rp FROM role_permissions rp
JOIN roles r ON r.id = rp.role_id
WHERE r.system_key IS NULL
  AND rp.permission_key IN ('settings.scim_manage', 'settings.sso_manage');

COMMIT;
