-- Migration: Remove SCIM provider tokens
-- Date: 2026-11-26
-- Description: Rollback of 20261126120000_add_scim_tokens. Drops scim_tokens
-- and nothing else; every SCIM provider token is lost.

DROP TABLE IF EXISTS scim_tokens;
