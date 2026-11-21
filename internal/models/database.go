package models

import (
	"fmt"
	"log"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/database"
	"gorm.io/gorm"
)

var DB *gorm.DB

func InitDatabase(cfg *config.DatabaseConfig) error {
	db, err := database.Open(cfg)
	if err != nil {
		return err
	}

	DB = db
	log.Println("Database connection established successfully")
	return nil
}

// CloseDatabase releases the pooled connections. It is safe to call when no
// database was ever opened and safe to call twice: a successful close clears
// the global handle, so a later call is a no-op instead of using a closed pool.
// Closing matters beyond tidiness on SQLite: the final close checkpoints the
// write-ahead log into the database file.
func CloseDatabase() error {
	if DB == nil {
		return nil
	}

	sqlDB, err := DB.DB()
	if err != nil {
		return fmt.Errorf("failed to get database instance: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return err
	}

	DB = nil
	return nil
}

// MigrateDatabase brings the schema up to date. database.Migrate wraps the
// call for SQLite, where adding a foreign key rebuilds the table and has to run
// with enforcement off; on MySQL and MariaDB it is a plain AutoMigrate.
func MigrateDatabase() error {
	return MigrateDatabaseWith(DB)
}

// MigrateDatabaseWith is MigrateDatabase on an explicit handle instead of the
// global DB, so two pools (two replicas booting at once) can be exercised in
// one test process.
func MigrateDatabaseWith(db *gorm.DB) error {
	return database.Migrate(db, func(tx *gorm.DB) error {
		if err := autoMigrateModels(tx); err != nil {
			return err
		}
		if err := ensureCustomerExternalIDCollation(tx); err != nil {
			return err
		}
		if err := ensureProductExternalIDCollation(tx); err != nil {
			return err
		}
		if err := ensureFormTicketKeyCollation(tx); err != nil {
			return err
		}
		if err := ensureUserIdentityCollation(tx); err != nil {
			return err
		}
		if err := ensureSCIMGroupMemberCollation(tx); err != nil {
			return err
		}
		if err := normalizeCompanyDomains(tx); err != nil {
			return err
		}
		// The four system roles (#344): inserts only the missing rows.
		if err := seedSystemRoles(tx); err != nil {
			return err
		}
		// No custom role holds an admin-only key (#524).
		if err := stripAdminOnlyRolePermissions(tx); err != nil {
			return err
		}
		// Every API key expires within one year, and customer accounts
		// hold none (#532).
		if err := capAPIKeyLifetimes(tx); err != nil {
			return err
		}
		// Refresh tokens that predate the session columns get their
		// family, sign-in method and session start.
		if err := backfillRefreshTokenSessions(tx); err != nil {
			return err
		}
		if err := migrateLegacyQuoteLineQuantity(tx); err != nil {
			return err
		}
		return backfillQuoteGrossTotals(tx)
	})
}

// migrateLegacyQuoteLineQuantity moves quote line quantities from the legacy
// whole-unit column quote_lines.quantity into quantity_milli (#343), on every
// driver, and then drops the legacy column. AutoMigrate has already added
// quantity_milli (DEFAULT 0, the "not yet converted" marker) and never drops a
// column. When quantity is absent the step issues nothing, so a migrated
// database takes no write and no DDL on startup.
//
// Discovery is a plain SELECT; the rows are converted by primary key in chunks
// of quoteQuantityBackfillChunk, without touching updated_at. The column is
// dropped with the native ALTER TABLE ... DROP COLUMN (SQLite 3.35+ and
// MySQL/MariaDB alike), never with Migrator().DropColumn: with a table name
// the glebarez/sqlite migrator dereferences a nil schema and panics, and with
// a model it rebuilds the table and loses idx_quote_lines_quote_id and
// idx_quote_lines_product_id. The legacy column carries no index, constraint
// or foreign key, so the native drop needs no table rebuild.
func migrateLegacyQuoteLineQuantity(tx *gorm.DB) error {
	if !tx.Migrator().HasColumn("quote_lines", "quantity") {
		return nil
	}
	var ids []uint
	if err := tx.Raw("SELECT id FROM quote_lines WHERE quantity_milli = 0 ORDER BY id").Scan(&ids).Error; err != nil {
		return fmt.Errorf("failed to find quote line quantities to convert: %w", err)
	}
	for start := 0; start < len(ids); start += quoteQuantityBackfillChunk {
		end := start + quoteQuantityBackfillChunk
		if end > len(ids) {
			end = len(ids)
		}
		if err := tx.Exec("UPDATE quote_lines SET quantity_milli = quantity * 1000 WHERE id IN ? AND quantity_milli = 0", ids[start:end]).Error; err != nil {
			return fmt.Errorf("failed to convert quote line quantities: %w", err)
		}
	}
	if err := tx.Exec("ALTER TABLE quote_lines DROP COLUMN quantity").Error; err != nil {
		return fmt.Errorf("failed to drop quote_lines.quantity: %w", err)
	}
	return nil
}

// quoteQuantityBackfillChunk bounds how many rows one quantity conversion
// UPDATE writes.
const quoteQuantityBackfillChunk = 500

// backfillQuoteGrossTotals sets quotes.gross_total_cents to total_cents +
// tax_cents wherever it differs (#324), soft-deleted rows included. AutoMigrate
// adds the column with DEFAULT 0, so every quote written before line tax
// existed would otherwise read a gross total of 0; SQLite installs never run
// the SQL migration that backfills it on MySQL/MariaDB. Like
// normalizeCompanyDomains it discovers the rows with a plain non-locking
// SELECT and updates only those ids, by primary key in chunks, so a backfilled
// database takes no write and no row lock on startup. updated_at is not
// touched: the quote did not change.
func backfillQuoteGrossTotals(tx *gorm.DB) error {
	var ids []uint
	if err := tx.Raw("SELECT id FROM quotes WHERE gross_total_cents <> total_cents + tax_cents ORDER BY id").Scan(&ids).Error; err != nil {
		return fmt.Errorf("failed to find quote gross totals to backfill: %w", err)
	}
	for start := 0; start < len(ids); start += quoteGrossBackfillChunk {
		end := start + quoteGrossBackfillChunk
		if end > len(ids) {
			end = len(ids)
		}
		if err := tx.Exec("UPDATE quotes SET gross_total_cents = total_cents + tax_cents WHERE id IN ?", ids[start:end]).Error; err != nil {
			return fmt.Errorf("failed to backfill quote gross totals: %w", err)
		}
	}
	return nil
}

// quoteGrossBackfillChunk bounds how many rows one backfill UPDATE writes.
const quoteGrossBackfillChunk = 500

// normalizeCompanyDomains lower-cases every stored company domain, soft-deleted
// rows included. The duplicate-domain check (companyRepository.ExistsByDomain)
// compares the column with plain equality so that idx_companies_domain can
// serve it, which is only correct while every stored domain is lower case.
// The application has always written it lower-cased (NormalizeCompanyDomain);
// this step covers rows written another way, on every driver, because SQLite
// installs never run the SQL migration that does the same on MySQL/MariaDB.
//
// It runs on every start and is idempotent, and it is built so that a
// normalised database takes no row locks at all. Discovery is a plain SELECT,
// a consistent non-locking read on MySQL/MariaDB: one full read of companies
// per start. Only when it returns ids are those rows lower-cased, by primary
// key in chunks of companyDomainNormalizeChunk, so writes and row locks are
// limited to the rows that actually need normalising. A single UPDATE with the
// predicate in its WHERE clause would instead lock every record it scans under
// InnoDB REPEATABLE READ, serialising replicas that start together and blocking
// company writes on every boot. The comparison is binary on MySQL/MariaDB,
// whose case-insensitive collation would otherwise call 'Acme.Example' equal
// to 'acme.example'. NULL domains are left alone; soft-deleted rows are
// normalised like live ones.
//
// Lower-casing cannot create a live duplicate the application did not already
// treat as one: the old check was case-insensitive (LOWER() on both sides), so
// two live rows differing only by case could never be created through the
// application. A database edited by hand into that state keeps both rows, now
// equal, exactly as before; the index is not unique, so nothing fails.
func normalizeCompanyDomains(tx *gorm.DB) error {
	predicate := "domain <> LOWER(domain)"
	if tx.Dialector.Name() != "sqlite" {
		predicate = "CAST(domain AS BINARY) <> CAST(LOWER(domain) AS BINARY)"
	}
	var ids []uint
	if err := tx.Raw("SELECT id FROM companies WHERE domain IS NOT NULL AND " + predicate + " ORDER BY id").Scan(&ids).Error; err != nil {
		return fmt.Errorf("failed to find company domains to normalise: %w", err)
	}
	for start := 0; start < len(ids); start += companyDomainNormalizeChunk {
		end := start + companyDomainNormalizeChunk
		if end > len(ids) {
			end = len(ids)
		}
		if err := tx.Exec("UPDATE companies SET domain = LOWER(domain) WHERE id IN ?", ids[start:end]).Error; err != nil {
			return fmt.Errorf("failed to normalise company domains: %w", err)
		}
	}
	return nil
}

// companyDomainNormalizeChunk bounds how many rows one normalising UPDATE
// writes, and so how many row locks it holds at once.
const companyDomainNormalizeChunk = 500

// autoMigrateModels is the AutoMigrate half of MigrateDatabase.
func autoMigrateModels(tx *gorm.DB) error {
	return tx.AutoMigrate(
		// RBAC roles (#344) before User: users.role_id references roles.
		&Role{},
		&RolePermission{},
		&RoleAuditEntry{},
		&User{},
		&Company{},
		&Lead{},
		&Customer{},
		// External identity mappings (#212) reference customers.
		&CustomerExternalID{},
		&Deal{},
		&DealStageChange{},
		// Product and Quote before QuoteLine: the line references both.
		&Product{},
		// Product external identity mappings (#239) reference products.
		&ProductExternalID{},
		&Quote{},
		&QuoteLine{},
		&QuoteStatusChange{},
		// The single-use tokens of the client response link (#382): a hash and
		// ids only, no foreign key.
		&QuoteResponseToken{},
		// OpenID Connect sign-in (#435): identities reference users; the
		// login attempts hold a hash and no foreign key.
		&UserIdentity{},
		// SCIM group memberships (#506): reference users.
		&SCIMGroupMember{},
		&OIDCLoginAttempt{},
		// Sign-in audit log (#454): ids only, no foreign key.
		&AuthAuditLog{},
		// SCIM provider tokens (#468): a hash only, no foreign key.
		&SCIMToken{},
		&Ticket{},
		&TicketComment{},
		// Ticket comment mail deliveries (#219): ids only, no foreign key.
		&TicketMailDelivery{},
		&Label{},
		&Task{},
		&APIKey{},
		&Configuration{},
		&RefreshToken{},
		&PasswordResetToken{},
		&BulkOperation{},
		&BulkOperationItem{},
		&AEOProfile{},
		&AEOPrompt{},
		&AEORun{},
		&AEOAnswer{},
		&AEOCitation{},
		&Form{},
		&FormSubmission{},
		&FormConfirmationToken{},
		&FormMailDelivery{},
		// Form ticket intake keys (#213): ids only, no foreign key.
		&FormTicketKey{},
		// Follow-up automation: rules before steps and runs (both reference
		// the rule), runs before run steps, notifications last.
		&AutomationRule{},
		&AutomationStep{},
		&AutomationEvent{},
		&AutomationRun{},
		&AutomationRunStep{},
		&Notification{},
		// Outbound webhooks (#345): ids only, no foreign key.
		&WebhookEndpoint{},
		&WebhookDelivery{},
	)
}
