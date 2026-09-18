// Package database owns the single place where a GORM connection is opened.
// Every driver-specific decision — dialector, DSN shape, connection pool — lives
// here so callers only ever deal with a ready *gorm.DB.
package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

// Rejected SQLite paths. config.Load already enforces both rules; repeating
// them here keeps Open safe for any caller that builds a DatabaseConfig by
// hand (tests, tools) rather than through the loader.
var (
	// ErrMissingSQLitePath is returned when DB_PATH is empty or blank: the DSN
	// would then consist of nothing but the pragma query.
	ErrMissingSQLitePath = errors.New("DB_PATH is empty or blank: the SQLite driver needs a database file path")
	// ErrSQLitePathHasQuery is returned when DB_PATH already carries a query
	// string, which would merge into the pragma query the connector appends.
	ErrSQLitePathHasQuery = errors.New(`DB_PATH must not contain "?": the SQLite connector appends its own pragma query string`)
)

// validateSQLitePath rejects paths the DSN builder cannot represent unambiguously.
func validateSQLitePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("invalid DB_PATH %q: %w", path, ErrMissingSQLitePath)
	}
	if strings.Contains(path, "?") {
		return fmt.Errorf("invalid DB_PATH %q: %w", path, ErrSQLitePathHasQuery)
	}
	return nil
}

// sqliteDSN turns a plain file path into the SQLite DSN. journal_mode(WAL)
// lets a reader outside this process — a snapshot or backup tool — read a
// consistent view while the backend writes; in-process it changes nothing,
// because every statement already serializes on the single pooled connection.
// foreign_keys(1) is needed because SQLite disables FK enforcement by default.
// The busy timeout is a parameter so a test can prove the pragma actually
// reaches the driver: 5000 also happens to be the driver's own default, so
// asserting that value alone would hold even if the pragma were dropped from
// the DSN.
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
		if err := validateSQLitePath(cfg.Path); err != nil {
			return nil, err
		}
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
