-- Lookup indexes for leads and companies (#109).
--
--   idx_leads_email           GetLatestByEmail (form double opt-in lead match)
--   idx_leads_classification  GET /leads?classification= and its count
--   idx_leads_external_id     GetByExternalID
--   idx_companies_domain      the live-row duplicate-domain check
--
-- All four are NON-unique on purpose: the same person may be captured as a
-- lead more than once, and a soft-deleted company keeps its domain while a
-- live company may reuse it. The names match what GORM AutoMigrate creates
-- from the model tags, and each index is created only when missing, so this
-- migration is safe on a database whose application startup already added
-- them. Both MySQL 8 and MariaDB 10.11 build a secondary index in place
-- without blocking reads or writes; the cost is one scan of the table.
--
-- The domain lookup compares the plain column (no LOWER()), which is what lets
-- it use the index. The application has always stored domains lower-cased; the
-- UPDATE below normalises any row written another way so the lookup cannot
-- miss it. The comparison is binary so only rows that actually change are
-- written.
UPDATE companies
SET domain = LOWER(domain)
WHERE domain IS NOT NULL AND CAST(domain AS BINARY) <> CAST(LOWER(domain) AS BINARY);

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'leads' AND INDEX_NAME = 'idx_leads_email'
  ),
  'SELECT 1',
  'CREATE INDEX idx_leads_email ON leads (email)'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'leads' AND INDEX_NAME = 'idx_leads_classification'
  ),
  'SELECT 1',
  'CREATE INDEX idx_leads_classification ON leads (classification)'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'leads' AND INDEX_NAME = 'idx_leads_external_id'
  ),
  'SELECT 1',
  'CREATE INDEX idx_leads_external_id ON leads (external_id)'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @preparedStatement = (SELECT IF(
  EXISTS(
    SELECT 1 FROM INFORMATION_SCHEMA.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'companies' AND INDEX_NAME = 'idx_companies_domain'
  ),
  'SELECT 1',
  'CREATE INDEX idx_companies_domain ON companies (domain)'
));
PREPARE stmt FROM @preparedStatement;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
