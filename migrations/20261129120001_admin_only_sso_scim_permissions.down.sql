-- Migration: Keep admin-only single sign-on and SCIM permissions (#524)
-- The up half is a deliberately one-way data step: it restores no permission
-- to a custom role and leaves role_permissions and role_audit_log as they are.
-- A custom role can no longer hold settings.sso_manage or settings.scim_manage,
-- and the application would remove them again on its next start.

DO 0;
