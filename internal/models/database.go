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
// It runs on every start and is idempotent: only rows whose bytes differ from
// their lower-case form are written, so a normalised table costs one scan and
// no writes. The comparison is binary on MySQL/MariaDB, whose case-insensitive
// collation would otherwise call 'Acme.Example' equal to 'acme.example'.
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
	if err := tx.Exec("UPDATE companies SET domain = LOWER(domain) WHERE domain IS NOT NULL AND " + predicate).Error; err != nil {
		return fmt.Errorf("failed to normalise company domains: %w", err)
	}
	return nil
}

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
