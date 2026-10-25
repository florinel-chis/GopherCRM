-- Migration: Add products, quotes, quote lines and quote status history
-- Date: 2026-10-26
-- Description: The product catalogue (products), the quotes priced from it
-- (quotes, quote_lines) and the append-only quote_status_changes table that
-- records every status a quote has been through, the create included
-- (from_status NULL). Four plain CREATE TABLEs: no existing table is altered
-- and no foreign key is added to one. CREATE TABLE IF NOT EXISTS so that a
-- database whose new binary already ran AutoMigrate still upgrades cleanly.
-- Money is an integer amount in minor units next to a three-letter currency
-- code; the bound columns (prices, quantities, discounts, totals, position,
-- is_active) carry no default because an explicit 0 or false is stored as
-- such. Column names, index names and constraint names match what GORM's
-- AutoMigrate produces, so a database created either way ends up the same.
-- MySQL 8 and MariaDB 10.11 both accept this as written.

CREATE TABLE IF NOT EXISTS products (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    name varchar(200) NOT NULL,
    sku varchar(64) NULL,
    description text NULL,
    unit_price_cents bigint NOT NULL,
    currency char(3) NOT NULL,
    is_active tinyint(1) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_products_deleted_at (deleted_at),
    KEY idx_products_sku (sku)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS quotes (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    title varchar(200) NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'draft',
    currency char(3) NOT NULL,
    subtotal_cents bigint NOT NULL,
    discount_cents bigint NOT NULL,
    total_cents bigint NOT NULL,
    valid_until date NULL,
    notes text NULL,
    sent_at datetime(3) NULL,
    closed_at datetime(3) NULL,
    last_emailed_at datetime(3) NULL,
    deal_id bigint unsigned NULL,
    customer_id bigint unsigned NULL,
    owner_id bigint unsigned NOT NULL,
    PRIMARY KEY (id),
    KEY idx_quotes_deleted_at (deleted_at),
    KEY idx_quotes_status (status),
    KEY idx_quotes_deal_id (deal_id),
    KEY idx_quotes_customer_id (customer_id),
    KEY idx_quotes_owner_id (owner_id),
    CONSTRAINT fk_quotes_deal FOREIGN KEY (deal_id) REFERENCES deals(id),
    CONSTRAINT fk_quotes_customer FOREIGN KEY (customer_id) REFERENCES customers(id),
    CONSTRAINT fk_quotes_owner FOREIGN KEY (owner_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS quote_lines (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    quote_id bigint unsigned NOT NULL,
    product_id bigint unsigned NOT NULL,
    position bigint NOT NULL,
    description varchar(255) NOT NULL,
    quantity bigint NOT NULL,
    unit_price_cents bigint NOT NULL,
    discount_bp bigint NOT NULL,
    line_total_cents bigint NOT NULL,
    PRIMARY KEY (id),
    KEY idx_quote_lines_quote_id (quote_id),
    KEY idx_quote_lines_product_id (product_id),
    CONSTRAINT fk_quotes_lines FOREIGN KEY (quote_id) REFERENCES quotes(id),
    CONSTRAINT fk_quote_lines_product FOREIGN KEY (product_id) REFERENCES products(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS quote_status_changes (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    quote_id bigint unsigned NOT NULL,
    from_status varchar(20) NULL,
    to_status varchar(20) NOT NULL,
    changed_by_id bigint unsigned NOT NULL,
    changed_at datetime(3) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_quote_status_changes_quote_id (quote_id),
    CONSTRAINT fk_quote_status_changes_changed_by FOREIGN KEY (changed_by_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
