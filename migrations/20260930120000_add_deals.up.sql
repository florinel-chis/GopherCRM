-- Migration: Add deals and their stage history
-- Date: 2026-09-30
-- Description: The deals table (one sales opportunity, optionally linked to a
-- company, a customer and a lead, always owned by a user) and the append-only
-- deal_stage_changes table that records every stage the deal has been through,
-- the create included (from_stage NULL). Two plain CREATE TABLEs: no existing
-- table is altered and no foreign key is added to one. Money is an integer
-- amount in minor units next to a three-letter currency code; the probability
-- column carries no default because a lost deal is stored at 0. Column names
-- avoid the reserved words (from, to): from_stage and to_stage. Index and
-- constraint names match what GORM's AutoMigrate produces, so a database
-- created either way ends up the same. MySQL 8 and MariaDB 10.11 both accept
-- this as written.

CREATE TABLE deals (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    title varchar(200) NOT NULL,
    stage varchar(20) NOT NULL DEFAULT 'qualification',
    amount_cents bigint NOT NULL DEFAULT 0,
    currency char(3) NOT NULL,
    probability bigint NOT NULL,
    expected_close_date date NULL,
    closed_at datetime(3) NULL,
    lost_reason varchar(255) NULL,
    source varchar(100) NULL,
    notes text NULL,
    company_id bigint unsigned NULL,
    customer_id bigint unsigned NULL,
    lead_id bigint unsigned NULL,
    owner_id bigint unsigned NOT NULL,
    PRIMARY KEY (id),
    KEY idx_deals_deleted_at (deleted_at),
    KEY idx_deals_company_id (company_id),
    KEY idx_deals_customer_id (customer_id),
    KEY idx_deals_lead_id (lead_id),
    KEY idx_deals_owner_id (owner_id),
    CONSTRAINT fk_deals_company FOREIGN KEY (company_id) REFERENCES companies(id),
    CONSTRAINT fk_deals_customer FOREIGN KEY (customer_id) REFERENCES customers(id),
    CONSTRAINT fk_deals_lead FOREIGN KEY (lead_id) REFERENCES leads(id),
    CONSTRAINT fk_deals_owner FOREIGN KEY (owner_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE deal_stage_changes (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    deal_id bigint unsigned NOT NULL,
    from_stage varchar(20) NULL,
    to_stage varchar(20) NOT NULL,
    changed_by_id bigint unsigned NOT NULL,
    changed_at datetime(3) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_deal_stage_changes_deal_id (deal_id),
    CONSTRAINT fk_deal_stage_changes_changed_by FOREIGN KEY (changed_by_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
