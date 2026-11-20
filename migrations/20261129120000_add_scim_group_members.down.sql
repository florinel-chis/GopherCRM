-- Migration: Remove SCIM group members (#506)
-- Rollback of issue #506: drops scim_group_members and nothing else. The
-- SCIM group memberships are lost; no other table references them. The roles
-- they mapped stay on the users rows as they are.

DROP TABLE IF EXISTS scim_group_members;
