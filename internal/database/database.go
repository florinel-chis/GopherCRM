package database

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Database wraps a GORM database connection and provides transaction support
type Database struct {
	db *gorm.DB
}

// New creates a new Database instance
func New(cfg *config.DatabaseConfig) (*Database, error) {
	dsn := cfg.DSN()
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	})

	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get database instance: %w", err)
	}

	// Configure connection pool
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	log.Println("Database connection established successfully")

	return &Database{db: db}, nil
}

// DB returns the underlying *gorm.DB instance
func (d *Database) DB() *gorm.DB {
	return d.db
}

// WithContext returns a new *gorm.DB instance with the given context
func (d *Database) WithContext(ctx context.Context) *gorm.DB {
	return d.db.WithContext(ctx)
}

// Transaction executes a function within a database transaction
// If the function returns an error, the transaction is rolled back
// Otherwise, the transaction is committed
func (d *Database) Transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	return d.db.WithContext(ctx).Transaction(fn)
}

// Migrate runs auto-migration for all models
func (d *Database) Migrate(models ...interface{}) error {
	return d.db.AutoMigrate(models...)
}

// Close closes the database connection
func (d *Database) Close() error {
	sqlDB, err := d.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Ping checks if the database connection is alive
func (d *Database) Ping(ctx context.Context) error {
	sqlDB, err := d.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}
