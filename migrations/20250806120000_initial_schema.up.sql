-- Initial schema migration for GopherCRM
-- Creates all tables based on the current models

-- Users table
CREATE TABLE IF NOT EXISTS users (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    email varchar(255) NOT NULL,
    password varchar(255) NOT NULL,
    first_name varchar(100) NOT NULL,
    last_name varchar(100) NOT NULL,
    role varchar(20) NOT NULL DEFAULT 'customer',
    is_active tinyint(1) NOT NULL DEFAULT '1',
    last_login_at datetime(3) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_users_email (email),
    KEY idx_users_deleted_at (deleted_at),
    KEY idx_users_active_role (is_active, role),
    KEY idx_users_last_login_at (last_login_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Customers table
CREATE TABLE IF NOT EXISTS customers (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    first_name varchar(100) NOT NULL,
    last_name varchar(100) NOT NULL,
    email varchar(255) NOT NULL,
    phone varchar(50) NULL,
    company varchar(200) NULL,
    position varchar(100) NULL,
    address varchar(255) NULL,
    city varchar(100) NULL,
    state varchar(100) NULL,
    country varchar(100) NULL,
    postal_code varchar(20) NULL,
    notes text NULL,
    user_id bigint unsigned NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_customers_email (email),
    KEY idx_customers_deleted_at (deleted_at),
    KEY idx_customers_user_id (user_id),
    CONSTRAINT fk_customers_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Leads table
CREATE TABLE IF NOT EXISTS leads (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    first_name varchar(100) NOT NULL,
    last_name varchar(100) NOT NULL,
    email varchar(255) NOT NULL,
    phone varchar(50) NULL,
    company varchar(200) NULL,
    position varchar(100) NULL,
    source varchar(100) NULL,
    status varchar(20) NOT NULL DEFAULT 'new',
    notes text NULL,
    owner_id bigint unsigned NOT NULL,
    customer_id bigint unsigned NULL,
    PRIMARY KEY (id),
    KEY idx_leads_deleted_at (deleted_at),
    KEY idx_leads_owner_id (owner_id),
    KEY idx_leads_owner_status (owner_id, status),
    KEY idx_leads_customer_id (customer_id),
    CONSTRAINT fk_leads_owner FOREIGN KEY (owner_id) REFERENCES users(id),
    CONSTRAINT fk_leads_customer FOREIGN KEY (customer_id) REFERENCES customers(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Tickets table
CREATE TABLE IF NOT EXISTS tickets (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    title varchar(255) NOT NULL,
    description text NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'open',
    priority varchar(20) NOT NULL DEFAULT 'medium',
    customer_id bigint unsigned NOT NULL,
    assigned_to_id bigint unsigned NULL,
    resolution text NULL,
    PRIMARY KEY (id),
    KEY idx_tickets_deleted_at (deleted_at),
    KEY idx_tickets_customer_id (customer_id),
    KEY idx_tickets_assigned_to_id (assigned_to_id),
    KEY idx_tickets_customer_status (customer_id, status),
    KEY idx_tickets_assigned_status (assigned_to_id, status),
    CONSTRAINT fk_tickets_customer FOREIGN KEY (customer_id) REFERENCES customers(id),
    CONSTRAINT fk_tickets_assigned_to FOREIGN KEY (assigned_to_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Tasks table
CREATE TABLE IF NOT EXISTS tasks (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    title varchar(255) NOT NULL,
    description text NULL,
    status varchar(20) NOT NULL DEFAULT 'pending',
    priority varchar(20) NOT NULL DEFAULT 'medium',
    due_date datetime(3) NULL,
    assigned_to_id bigint unsigned NOT NULL,
    lead_id bigint unsigned NULL,
    customer_id bigint unsigned NULL,
    PRIMARY KEY (id),
    KEY idx_tasks_deleted_at (deleted_at),
    KEY idx_tasks_assigned_to_id (assigned_to_id),
    KEY idx_tasks_assigned_status (assigned_to_id, status),
    KEY idx_tasks_lead_id (lead_id),
    KEY idx_tasks_customer_id (customer_id),
    CONSTRAINT fk_tasks_assigned_to FOREIGN KEY (assigned_to_id) REFERENCES users(id),
    CONSTRAINT fk_tasks_lead FOREIGN KEY (lead_id) REFERENCES leads(id),
    CONSTRAINT fk_tasks_customer FOREIGN KEY (customer_id) REFERENCES customers(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- API Keys table
CREATE TABLE IF NOT EXISTS api_keys (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    name varchar(100) NOT NULL,
    key_hash varchar(64) NOT NULL,
    prefix varchar(8) NOT NULL,
    user_id bigint unsigned NOT NULL,
    last_used_at datetime(3) NULL,
    expires_at datetime(3) NULL,
    is_active tinyint(1) NOT NULL DEFAULT '1',
    PRIMARY KEY (id),
    UNIQUE KEY idx_api_keys_key_hash (key_hash),
    KEY idx_api_keys_deleted_at (deleted_at),
    KEY idx_api_keys_user_id (user_id),
    KEY idx_api_keys_active_user (is_active, user_id),
    CONSTRAINT fk_api_keys_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Configurations table
CREATE TABLE IF NOT EXISTS configurations (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    config_key varchar(255) NOT NULL,
    value text NULL,
    type varchar(20) NOT NULL,
    category varchar(50) NOT NULL,
    description varchar(500) NULL,
    default_value text NULL,
    is_system tinyint(1) NOT NULL DEFAULT '0',
    is_read_only tinyint(1) NOT NULL DEFAULT '0',
    valid_values text NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_configurations_config_key (config_key),
    KEY idx_configurations_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Refresh Tokens table
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    user_id bigint unsigned NOT NULL,
    token_hash varchar(255) NOT NULL,
    expires_at datetime(3) NOT NULL,
    is_revoked tinyint(1) NOT NULL DEFAULT '0',
    PRIMARY KEY (id),
    UNIQUE KEY idx_refresh_tokens_token_hash (token_hash),
    KEY idx_refresh_tokens_deleted_at (deleted_at),
    KEY idx_refresh_tokens_user_id (user_id),
    CONSTRAINT fk_refresh_tokens_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

