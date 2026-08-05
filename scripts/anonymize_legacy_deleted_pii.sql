-- =============================================================================
-- MANUAL REMEDIATION SCRIPT — anonymise personal data left behind by legacy
-- soft deletes.  RUN BY HAND.  IRREVERSIBLE.
-- =============================================================================
--
-- WHY THIS EXISTS
-- ---------------
-- Deleting a user or a customer now performs a real GDPR Article 17 erasure:
-- the personal fields are overwritten in place and only then is the row
-- soft-deleted (see userRepository.Delete / customerRepository.Delete).
--
-- Rows that were soft-deleted BEFORE that change were never scrubbed. They are
-- flagged deleted_at IS NOT NULL, they are invisible to the application, and
-- they still contain the person's email address, name, phone number, postal
-- address, free-text notes and — for users — a crackable bcrypt password hash.
-- Marking a row as deleted is not erasure; retaining that data indefinitely
-- also conflicts with the storage-limitation principle in Article 5(1)(e).
--
-- This script brings those legacy rows up to the same standard as new deletions.
--
-- WHAT IT DOES
-- ------------
--   1. Replaces users.email and customers.email on every soft-deleted row with
--      a unique, non-routable placeholder:  deleted-<random>@anonymized.invalid
--      The ".invalid" TLD is reserved by RFC 2606 and can never resolve. The
--      random part is NOT derived from the original address: a hash of an email
--      is still personal data, because anyone holding a candidate address can
--      confirm it by re-hashing.
--   2. Blanks every other personal field on those rows.
--   3. Overwrites the password hash of soft-deleted users with an unusable
--      marker, so the account can never authenticate.
--   4. Hard-deletes API keys and refresh tokens belonging to soft-deleted users
--      — credentials must not outlive the account they belong to.
--
-- The rows themselves are KEPT (still soft-deleted). Tickets, tasks, leads and
-- api_keys reference users and customers by foreign key; destroying those rows
-- would take business history and referential integrity with them, and Article
-- 17 does not ask for that. Anonymisation in place is the correct remedy.
--
-- WHAT IT DOES NOT DO
-- -------------------
--   * It does not touch LIVE rows (deleted_at IS NULL). Every statement below is
--     guarded on deleted_at IS NOT NULL.
--   * It does not alter any index. The unique indexes on users.email and
--     customers.email must stay exactly as they are: a composite
--     UNIQUE(email, deleted_at) looks tempting but is WRONG, because MySQL
--     treats NULLs in a unique index as distinct, so live rows (deleted_at IS
--     NULL) would then be allowed unlimited duplicate emails.
--   * It does not touch the leads table. A converted lead holds its own copy of
--     the person's contact details; erasing those is a separate decision and a
--     separate script.
--
-- THIS SCRIPT IS NOT WIRED INTO ANYTHING
-- --------------------------------------
-- It lives in scripts/ and NOT in migrations/ on purpose. Files under
-- migrations/ are applied by `cmd/migrate` (and by any deployment step that
-- runs it), which would make a destructive, unattended backfill run against
-- production the moment someone deploys. A one-way data-destroying operation
-- must be a deliberate, supervised act. Nothing runs this file automatically —
-- an operator has to invoke it.
--
-- HOW TO RUN
-- ----------
--   1. TAKE A BACKUP. There is no undo, and the data is gone once committed.
--        mysqldump --single-transaction gophercrm > gophercrm-before-erasure.sql
--   2. Run the DRY RUN section and read the counts. If they are not what you
--      expect, stop.
--   3. Run the REMEDIATION section.
--   4. Run the VERIFICATION section; it must return zero rows.
--
--   mysql -u <user> -p <database> < scripts/anonymize_legacy_deleted_pii.sql
--
-- MySQL 8.0+ only (RANDOM_BYTES). If the client runs with safe updates enabled,
-- `SET SQL_SAFE_UPDATES = 0;` for the session first — the WHERE clauses filter
-- on deleted_at, which is not a key column.
-- =============================================================================


-- -----------------------------------------------------------------------------
-- DRY RUN — read-only. How much legacy personal data is still retained?
-- -----------------------------------------------------------------------------
SELECT 'users still holding personal data' AS scope, COUNT(*) AS rows_affected
FROM users
WHERE deleted_at IS NOT NULL
  AND email NOT LIKE '%@anonymized.invalid'
UNION ALL
SELECT 'customers still holding personal data', COUNT(*)
FROM customers
WHERE deleted_at IS NOT NULL
  AND email NOT LIKE '%@anonymized.invalid'
UNION ALL
SELECT 'api keys belonging to deleted users', COUNT(*)
FROM api_keys k
JOIN users u ON u.id = k.user_id
WHERE u.deleted_at IS NOT NULL
UNION ALL
SELECT 'refresh tokens belonging to deleted users', COUNT(*)
FROM refresh_tokens t
JOIN users u ON u.id = t.user_id
WHERE u.deleted_at IS NOT NULL;


-- -----------------------------------------------------------------------------
-- REMEDIATION — everything below this line destroys data.
-- -----------------------------------------------------------------------------
START TRANSACTION;

-- Credentials first: they must not survive the account. Hard delete, because a
-- soft-deleted API key would still be a stored secret tied to a person.
DELETE k
FROM api_keys k
JOIN users u ON u.id = k.user_id
WHERE u.deleted_at IS NOT NULL;

DELETE t
FROM refresh_tokens t
JOIN users u ON u.id = t.user_id
WHERE u.deleted_at IS NOT NULL;

-- Users. The row id is included in the placeholder purely to guarantee
-- uniqueness against the (unchanged, deleted_at-unaware) unique index; the id
-- is retained by the row anyway and reveals nothing about the person.
UPDATE users
SET email                 = CONCAT('deleted-', id, '-', LOWER(HEX(RANDOM_BYTES(16))), '@anonymized.invalid'),
    password              = '!erased',
    first_name            = '',
    last_name             = '',
    is_active             = 0,
    last_login_at         = NULL,
    failed_login_attempts = 0,
    locked_until          = NULL
WHERE deleted_at IS NOT NULL
  AND email NOT LIKE '%@anonymized.invalid';

-- Customers. notes is free text and is the likeliest place for unstructured
-- personal data, so it is cleared outright.
UPDATE customers
SET email       = CONCAT('deleted-', id, '-', LOWER(HEX(RANDOM_BYTES(16))), '@anonymized.invalid'),
    first_name  = '',
    last_name   = '',
    phone       = '',
    company     = '',
    position    = '',
    address     = '',
    city        = '',
    state       = '',
    country     = '',
    postal_code = '',
    notes       = ''
WHERE deleted_at IS NOT NULL
  AND email NOT LIKE '%@anonymized.invalid';

COMMIT;


-- -----------------------------------------------------------------------------
-- VERIFICATION — every query below must return 0.
-- -----------------------------------------------------------------------------
SELECT 'users NOT erased' AS check_name, COUNT(*) AS must_be_zero
FROM users
WHERE deleted_at IS NOT NULL
  AND (email NOT LIKE '%@anonymized.invalid' OR first_name <> '' OR last_name <> '' OR password <> '!erased')
UNION ALL
SELECT 'customers NOT erased', COUNT(*)
FROM customers
WHERE deleted_at IS NOT NULL
  AND (email NOT LIKE '%@anonymized.invalid' OR first_name <> '' OR last_name <> '' OR notes <> '')
UNION ALL
SELECT 'credentials surviving a deleted user', COUNT(*)
FROM (
    SELECT k.id FROM api_keys k JOIN users u ON u.id = k.user_id WHERE u.deleted_at IS NOT NULL
    UNION ALL
    SELECT t.id FROM refresh_tokens t JOIN users u ON u.id = t.user_id WHERE u.deleted_at IS NOT NULL
) AS surviving_credentials
UNION ALL
-- Live data must be untouched: no live row may have been anonymised, and no two
-- live rows may share an address.
SELECT 'LIVE rows wrongly anonymised', COUNT(*)
FROM (
    SELECT id FROM users     WHERE deleted_at IS NULL AND email LIKE '%@anonymized.invalid'
    UNION ALL
    SELECT id FROM customers WHERE deleted_at IS NULL AND email LIKE '%@anonymized.invalid'
) AS wrongly_anonymised;
