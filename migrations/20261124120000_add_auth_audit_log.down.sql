-- Migration: Remove sign-in audit log
-- Date: 2026-11-24
-- Description: Reverts 20261124120000_add_auth_audit_log: drops the
-- auth_audit_log table and its rows. Nothing else is touched.

DROP TABLE IF EXISTS auth_audit_log;
