-- Migration: Add companies
-- Date: 2026-09-29
-- Description: The companies table, and a nullable company_id on leads and
-- customers that links a record to a curated company. The free-text company
-- column on both tables is untouched: it stays the contract of the public
-- forms and of imports. companies.domain has no unique index on purpose:
-- uniqueness is checked in the service among live rows, because a
-- soft-deleted company must not reserve its domain forever. Index and
-- constraint names match what GORM's AutoMigrate produces, so a database
-- created either way ends up the same. Plain ADD COLUMN, no IF NOT EXISTS:
-- MySQL 8 and MariaDB 10.11 both accept this as written.

CREATE TABLE companies (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    created_at datetime(3) NULL,
    updated_at datetime(3) NULL,
    deleted_at datetime(3) NULL,
    name varchar(200) NOT NULL,
    domain varchar(255) NULL,
    website varchar(255) NULL,
    industry varchar(100) NULL,
    employee_range varchar(20) NULL,
    phone varchar(50) NULL,
    address varchar(255) NULL,
    city varchar(100) NULL,
    state varchar(100) NULL,
    country varchar(100) NULL,
    postal_code varchar(20) NULL,
    notes text NULL,
    owner_id bigint unsigned NULL,
    PRIMARY KEY (id),
    KEY idx_companies_deleted_at (deleted_at),
    KEY idx_companies_owner_id (owner_id),
    CONSTRAINT fk_companies_owner FOREIGN KEY (owner_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE leads ADD COLUMN company_id bigint unsigned NULL;
ALTER TABLE leads ADD KEY idx_leads_company_id (company_id);
ALTER TABLE leads ADD CONSTRAINT fk_leads_company_record FOREIGN KEY (company_id) REFERENCES companies(id);

ALTER TABLE customers ADD COLUMN company_id bigint unsigned NULL;
ALTER TABLE customers ADD KEY idx_customers_company_id (company_id);
ALTER TABLE customers ADD CONSTRAINT fk_customers_company_record FOREIGN KEY (company_id) REFERENCES companies(id);
