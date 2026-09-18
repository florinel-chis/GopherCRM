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

func MigrateDatabase() error {
	return DB.AutoMigrate(
		&User{},
		&Lead{},
		&Customer{},
		&Ticket{},
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
	)
}
