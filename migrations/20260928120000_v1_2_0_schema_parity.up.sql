-- Migration: v1.2.0 Schema Parity
-- Date: 2026-09-28
-- Description: Brings migrations into parity with models.MigrateDatabase() for v1.2.0.
-- Adds columns to users, customers, leads, and configurations, plus missing tables:
-- labels, task_labels, password_reset_tokens, bulk_operations, bulk_operation_items,
-- aeo_profiles, aeo_prompts, aeo_runs, aeo_answers, aeo_citations, forms,
-- form_submissions, and form_confirmation_tokens.

-- 1. Alter existing tables
ALTER TABLE users ADD COLUMN failed_login_attempts int NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN locked_until datetime(3) NULL;

ALTER TABLE customers ADD COLUMN assigned_to_id bigint unsigned NULL;
ALTER TABLE customers ADD KEY idx_customers_assigned_to_id (assigned_to_id);
ALTER TABLE customers ADD CONSTRAINT fk_customers_assigned_to FOREIGN KEY (assigned_to_id) REFERENCES users(id);

ALTER TABLE leads ADD COLUMN classification varchar(20) DEFAULT 'unclassified';
ALTER TABLE leads ADD COLUMN external_id varchar(255) NULL;

ALTER TABLE configurations ADD COLUMN is_sensitive tinyint(1) NOT NULL DEFAULT '0';

-- 2. Labels and task_labels
CREATE TABLE labels (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    name varchar(50) NOT NULL,
    color varchar(7) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_labels_name (name),
    KEY idx_labels_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE task_labels (
    task_id bigint unsigned NOT NULL,
    label_id bigint unsigned NOT NULL,
    PRIMARY KEY (task_id, label_id),
    KEY idx_task_labels_label_id (label_id),
    CONSTRAINT fk_task_labels_task FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    CONSTRAINT fk_task_labels_label FOREIGN KEY (label_id) REFERENCES labels(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 3. Password reset tokens
CREATE TABLE password_reset_tokens (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    user_id bigint unsigned NOT NULL,
    token_hash varchar(255) NOT NULL,
    expires_at datetime(3) NOT NULL,
    used_at datetime(3) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_password_reset_tokens_token_hash (token_hash),
    KEY idx_password_reset_tokens_deleted_at (deleted_at),
    KEY idx_password_reset_tokens_user_id (user_id),
    CONSTRAINT fk_password_reset_tokens_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 4. Bulk operations
CREATE TABLE bulk_operations (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    user_id bigint unsigned NOT NULL,
    resource_type varchar(50) NOT NULL,
    type varchar(20) NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'pending',
    total_items bigint NOT NULL,
    success_count bigint DEFAULT 0,
    failure_count bigint DEFAULT 0,
    PRIMARY KEY (id),
    KEY idx_bulk_operations_deleted_at (deleted_at),
    KEY idx_bulk_operations_user_id (user_id),
    CONSTRAINT fk_bulk_operations_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE bulk_operation_items (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    operation_id bigint unsigned NOT NULL,
    resource_id bigint unsigned NULL,
    status varchar(20) NOT NULL DEFAULT 'pending',
    error text NULL,
    data text NULL,
    PRIMARY KEY (id),
    KEY idx_bulk_operation_items_deleted_at (deleted_at),
    KEY idx_bulk_operation_items_operation_id (operation_id),
    CONSTRAINT fk_bulk_operations_items FOREIGN KEY (operation_id) REFERENCES bulk_operations(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 5. AEO module tables
CREATE TABLE aeo_profiles (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    brand_name varchar(120) NOT NULL,
    description text NULL,
    brand_aliases text NULL,
    owned_domains text NULL,
    competitors text NULL,
    PRIMARY KEY (id),
    KEY idx_aeo_profiles_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE aeo_prompts (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    text varchar(500) NOT NULL,
    is_active tinyint(1) NOT NULL DEFAULT '1',
    created_by_id bigint unsigned NULL,
    PRIMARY KEY (id),
    KEY idx_aeo_prompts_deleted_at (deleted_at),
    KEY idx_aeo_prompts_is_active (is_active),
    KEY idx_aeo_prompts_created_by_id (created_by_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE aeo_runs (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    `trigger` varchar(20) NOT NULL,
    status varchar(20) NOT NULL,
    started_at datetime(3) NOT NULL,
    completed_at datetime(3) NULL,
    total_queries bigint NOT NULL DEFAULT 0,
    failed_queries bigint NOT NULL DEFAULT 0,
    triggered_by_id bigint unsigned NULL,
    PRIMARY KEY (id),
    KEY idx_aeo_runs_deleted_at (deleted_at),
    KEY idx_aeo_runs_trigger (`trigger`),
    KEY idx_aeo_runs_status (status),
    KEY idx_aeo_runs_triggered_by_id (triggered_by_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE aeo_answers (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    run_id bigint unsigned NOT NULL,
    prompt_id bigint unsigned NOT NULL,
    provider varchar(40) NOT NULL,
    model varchar(120) NOT NULL,
    attempt bigint NOT NULL DEFAULT 1,
    answer_text longtext NULL,
    brand_mentioned tinyint(1) NOT NULL DEFAULT '0',
    first_mention_pos bigint NOT NULL,
    competitor_mentions text NULL,
    latency_ms bigint NOT NULL DEFAULT 0,
    error text NULL,
    PRIMARY KEY (id),
    KEY idx_aeo_answers_deleted_at (deleted_at),
    KEY idx_aeo_answers_run_id (run_id),
    KEY idx_aeo_answers_prompt_id (prompt_id),
    KEY idx_aeo_answers_provider (provider),
    KEY idx_aeo_answers_brand_mentioned (brand_mentioned)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE aeo_citations (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    answer_id bigint unsigned NOT NULL,
    url varchar(1024) NOT NULL,
    domain varchar(255) NOT NULL,
    is_owned tinyint(1) NOT NULL DEFAULT '0',
    competitor_name varchar(120) NULL,
    PRIMARY KEY (id),
    KEY idx_aeo_citations_deleted_at (deleted_at),
    KEY idx_aeo_citations_answer_id (answer_id),
    KEY idx_aeo_citations_domain (domain),
    CONSTRAINT fk_aeo_answers_citations FOREIGN KEY (answer_id) REFERENCES aeo_answers(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 6. Forms module tables
CREATE TABLE forms (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    name varchar(255) NOT NULL,
    description text NULL,
    public_id varchar(32) NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'draft',
    fields text NULL,
    submit_action varchar(20) NOT NULL DEFAULT 'message',
    thank_you_message text NULL,
    redirect_url varchar(512) NULL,
    consent_text text NULL,
    notify_emails text NULL,
    double_opt_in tinyint(1) NOT NULL DEFAULT '0',
    confirmation_subject varchar(255) NULL,
    confirmation_body text NULL,
    follow_up_subject varchar(255) NULL,
    follow_up_body text NULL,
    content_url varchar(512) NULL,
    captcha_enabled tinyint(1) NOT NULL DEFAULT '0',
    create_lead tinyint(1) NOT NULL,
    default_owner_id bigint unsigned NULL,
    allowed_domains text NULL,
    created_by_id bigint unsigned NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_forms_public_id (public_id),
    KEY idx_forms_deleted_at (deleted_at),
    KEY idx_forms_default_owner_id (default_owner_id),
    KEY idx_forms_created_by_id (created_by_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE form_submissions (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    form_id bigint unsigned NOT NULL,
    data mediumtext NULL,
    email varchar(255) NULL,
    status varchar(20) NOT NULL,
    spam_reason varchar(100) NULL,
    lead_id bigint unsigned NULL,
    ip_address varchar(45) NULL,
    user_agent varchar(255) NULL,
    referrer varchar(512) NULL,
    confirmed_at datetime(3) NULL,
    PRIMARY KEY (id),
    KEY idx_form_submissions_deleted_at (deleted_at),
    KEY idx_form_submissions_form_id (form_id),
    KEY idx_form_submissions_email (email),
    KEY idx_form_submissions_status (status),
    KEY idx_form_submissions_lead_id (lead_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE form_confirmation_tokens (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    submission_id bigint unsigned NOT NULL,
    token_hash varchar(64) NOT NULL,
    expires_at datetime(3) NOT NULL,
    used_at datetime(3) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_form_confirmation_tokens_token_hash (token_hash),
    KEY idx_form_confirmation_tokens_deleted_at (deleted_at),
    KEY idx_form_confirmation_tokens_submission_id (submission_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
