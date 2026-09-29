-- Migration: Remove companies
-- Date: 2026-09-29
-- Description: Drops the company links from customers and leads, then the
-- companies table. The links are business data, not personal data, and they
-- are lost by this rollback; the free-text company column is unaffected.

ALTER TABLE customers DROP FOREIGN KEY fk_customers_company_record;
ALTER TABLE customers DROP KEY idx_customers_company_id;
ALTER TABLE customers DROP COLUMN company_id;

ALTER TABLE leads DROP FOREIGN KEY fk_leads_company_record;
ALTER TABLE leads DROP KEY idx_leads_company_id;
ALTER TABLE leads DROP COLUMN company_id;

DROP TABLE companies;
