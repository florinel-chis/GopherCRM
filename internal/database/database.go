// Package database owns the single place where a GORM connection is opened.
// Every driver-specific decision — dialector, DSN shape, connection pool — lives
// here so callers only ever deal with a ready *gorm.DB.
package database

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sqliteBusyTimeoutMS is how long a blocked writer waits for the database lock
// before giving up, instead of failing immediately.
const sqliteBusyTimeoutMS = 5000

// sqliteDSN turns a plain file path into the SQLite DSN. journal_mode(WAL)
// allows readers to proceed during a write, and foreign_keys(1) is needed
// because SQLite disables FK enforcement by default. The busy timeout is a
// parameter so a test can prove the pragma actually reaches the driver: 5000
// also happens to be the driver's own default, so asserting that value alone
// would hold even if the pragma were dropped from the DSN.
func sqliteDSN(path string, busyTimeoutMS int) string {
	return fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)",
		path, busyTimeoutMS)
}

// Open connects to the configured database and returns the GORM handle.
func Open(cfg *config.DatabaseConfig) (*gorm.DB, error) {
	gormConfig := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	}

	var (
		dialector gorm.Dialector
		tunePool  func(*sql.DB)
	)

	switch cfg.Driver {
	case config.DriverMySQL:
		dialector = mysql.Open(cfg.DSN())
		tunePool = func(sqlDB *sql.DB) {
			sqlDB.SetMaxIdleConns(10)
			sqlDB.SetMaxOpenConns(100)
			sqlDB.SetConnMaxLifetime(time.Hour)
		}
	case config.DriverSQLite:
		dialector = sqlite.Open(sqliteDSN(cfg.Path, sqliteBusyTimeoutMS))
		tunePool = func(sqlDB *sql.DB) {
			// SQLite takes a database-wide write lock, so extra connections
			// buy lock contention rather than throughput.
			sqlDB.SetMaxOpenConns(1)
			sqlDB.SetMaxIdleConns(1)
		}
	default:
		return nil, fmt.Errorf("unsupported database driver %q: valid values are mysql, sqlite", cfg.Driver)
	}

	db, err := gorm.Open(dialector, gormConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get database instance: %w", err)
	}
	tunePool(sqlDB)

	return db, nil
}
