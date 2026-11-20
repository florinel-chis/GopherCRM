-- Migration: SCIM group members (#506)
-- Date: 2026-11-29
-- Description: SCIM 2.0 Groups (issue #506). scim_group_members holds one
-- row per member of a SCIM Group; a group is a distinct groups-claim value of
-- the single sign-on role mapping. group_value carries utf8mb4_bin, so values
-- differing only in letter case are distinct groups, and the pair
-- (group_value, user_id) is unique. user_id references users (ON DELETE
-- RESTRICT); there is no deleted_at, and erasure keeps the row. Columns, index
-- names and the foreign-key name match what GORM's AutoMigrate produces plus
-- the collation guard of models.MigrateDatabase, and the table is created only
-- when missing, so this migration is safe on a database whose application
-- startup already created it. No existing table is altered. MySQL 8 and
-- MariaDB 10.11 both accept this as written.

CREATE TABLE IF NOT EXISTS scim_group_members (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    group_value varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    user_id bigint unsigned NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_scim_group_members_group_user (group_value, user_id),
    KEY idx_scim_group_members_user_id (user_id),
    CONSTRAINT fk_scim_group_members_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
