-- Migration: Add follow-up automation rules, events, runs and notifications
-- Date: 2026-10-28
-- Description: The automation subsystem of issue #164: admin-defined rules
-- with ordered steps (automation_rules, automation_steps), the outbox events a
-- lead or deal write records in its own transaction (automation_events), the
-- persisted runs the worker executes with their per-step log
-- (automation_runs, automation_run_steps) and the in-app notifications a
-- notify step creates (notifications). Six plain CREATE TABLEs: no existing
-- table is altered and no foreign key is added to one. CREATE TABLE IF NOT
-- EXISTS so that a database whose new binary already ran AutoMigrate still
-- upgrades cleanly. Column names, index names and constraint names match what
-- GORM's AutoMigrate produces, so a database created either way ends up the
-- same. Timestamps are datetime(3) in UTC: the worker compares resume_at with
-- a UTC parameter on both engines. MySQL 8 and MariaDB 10.11 both accept this
-- as written.

CREATE TABLE IF NOT EXISTS automation_rules (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    name varchar(200) NOT NULL,
    description text NULL,
    entity_type varchar(20) NOT NULL,
    trigger_type varchar(40) NOT NULL,
    trigger_config text NULL,
    enabled tinyint(1) NOT NULL DEFAULT 0,
    version bigint NOT NULL DEFAULT 1,
    schedule_pass_on varchar(10) NULL,
    schedule_cursor bigint unsigned NOT NULL DEFAULT 0,
    schedule_completed_on varchar(10) NULL,
    created_by_id bigint unsigned NULL,
    PRIMARY KEY (id),
    KEY idx_automation_rules_deleted_at (deleted_at),
    CONSTRAINT fk_automation_rules_created_by FOREIGN KEY (created_by_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS automation_steps (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    rule_id bigint unsigned NOT NULL,
    position bigint NOT NULL,
    step_type varchar(20) NOT NULL,
    config text NULL,
    created_at datetime(3) NULL,
    PRIMARY KEY (id),
    KEY idx_automation_steps_rule_id (rule_id),
    CONSTRAINT fk_automation_rules_steps FOREIGN KEY (rule_id) REFERENCES automation_rules(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS automation_events (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    entity_type varchar(20) NOT NULL,
    entity_id bigint unsigned NOT NULL,
    event_type varchar(40) NOT NULL,
    from_value varchar(50) NULL,
    to_value varchar(50) NULL,
    origin varchar(20) NOT NULL,
    actor_id bigint unsigned NULL,
    created_at datetime(3) NOT NULL,
    processed_at datetime(3) NULL,
    PRIMARY KEY (id),
    KEY idx_automation_events_entity (entity_type, entity_id),
    KEY idx_automation_events_processed_at (processed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS automation_runs (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    rule_id bigint unsigned NOT NULL,
    rule_version bigint NOT NULL,
    entity_type varchar(20) NOT NULL,
    entity_id bigint unsigned NOT NULL,
    event_id bigint unsigned NULL,
    dedupe_key varchar(100) NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'pending',
    next_position bigint NOT NULL DEFAULT 1,
    attempts bigint NOT NULL DEFAULT 0,
    resume_at datetime(3) NULL,
    claimed_at datetime(3) NULL,
    finished_at datetime(3) NULL,
    cancel_reason varchar(30) NULL,
    error_code varchar(40) NULL,
    PRIMARY KEY (id),
    KEY idx_automation_runs_rule_id (rule_id),
    KEY idx_automation_runs_entity (entity_type, entity_id),
    UNIQUE KEY idx_automation_runs_dedupe_key (dedupe_key),
    KEY idx_automation_runs_status (status),
    KEY idx_automation_runs_resume_at (resume_at),
    CONSTRAINT fk_automation_runs_rule FOREIGN KEY (rule_id) REFERENCES automation_rules(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS automation_run_steps (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    run_id bigint unsigned NOT NULL,
    position bigint NOT NULL,
    step_type varchar(20) NOT NULL,
    outcome varchar(20) NOT NULL,
    error_code varchar(40) NULL,
    detail text NULL,
    executed_at datetime(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_automation_run_steps_run_position (run_id, position),
    CONSTRAINT fk_automation_runs_steps FOREIGN KEY (run_id) REFERENCES automation_runs(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS notifications (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    user_id bigint unsigned NOT NULL,
    title varchar(255) NOT NULL,
    body text NULL,
    entity_type varchar(20) NOT NULL,
    entity_id bigint unsigned NOT NULL,
    run_id bigint unsigned NULL,
    read_at datetime(3) NULL,
    PRIMARY KEY (id),
    KEY idx_notifications_user_id (user_id),
    KEY idx_notifications_entity (entity_type, entity_id),
    CONSTRAINT fk_notifications_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
