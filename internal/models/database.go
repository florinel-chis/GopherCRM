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
	return database.Migrate(DB, func(tx *gorm.DB) error {
		if err := autoMigrateModels(tx); err != nil {
			return err
		}
		return normalizeCompanyDomains(tx)
	})
}

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
		&User{},
		&Company{},
		&Lead{},
		&Customer{},
		&Deal{},
		&DealStageChange{},
		// Product and Quote before QuoteLine: the line references both.
		&Product{},
		&Quote{},
		&QuoteLine{},
		&QuoteStatusChange{},
		&Ticket{},
		&TicketComment{},
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
		// Follow-up automation: rules before steps and runs (both reference
		// the rule), runs before run steps, notifications last.
		&AutomationRule{},
		&AutomationStep{},
		&AutomationEvent{},
		&AutomationRun{},
		&AutomationRunStep{},
		&Notification{},
	)
}
