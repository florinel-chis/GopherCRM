package models

import (
	"fmt"
	"log"
	"time"

	"github.com/florinel-chis/gocrm/internal/config"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func InitDatabase(cfg *config.DatabaseConfig) error {
	var err error
	
	dsn := cfg.DSN()
	DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	})
	
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	sqlDB, err := DB.DB()
	if err != nil {
		return fmt.Errorf("failed to get database instance: %w", err)
	}

	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	log.Println("Database connection established successfully")
	return nil
}

func MigrateDatabase() error {
	return DB.AutoMigrate(
		&User{},
		&Lead{},
		&Customer{},
		&Ticket{},
		&Task{},
		&APIKey{},
		&Configuration{},
	)
}