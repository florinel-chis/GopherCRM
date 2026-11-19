-- Migration: Remove OIDC sign-in
-- Rollback of issue #435: drops user_identities and oidc_login_attempts and
-- nothing else. The identity links and any unfinished sign-in attempts are
-- lost; no other table references either one. Accounts provisioned by a
-- sign-in stay as ordinary users without a usable password.

DROP TABLE IF EXISTS user_identities;
DROP TABLE IF EXISTS oidc_login_attempts;
