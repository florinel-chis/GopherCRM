package testsupport

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var discardLogger = logger.Discard

// seq makes every database and schema name unique within the process, even
// for two opens in the same nanosecond.
var seq atomic.Uint64

func uniqueSuffix() string {
	return fmt.Sprintf("%d_%d", time.Now().UnixNano(), seq.Add(1))
}

// OpenSQLite opens a SQLite database private to this call and AutoMigrates
// the Models option into it.
//
// By default the database lives in memory under a unique name
// (file:<unique>?mode=memory&cache=shared, so every pooled connection sees the
// same database), with foreign_keys(1), one open connection and a discarding
// logger. File, WithoutForeignKeys, MaxOpenConns, MaxIdleConns, the
// DriverDefault* options and GormConfig change that. The pool is closed in
// t.Cleanup.
func OpenSQLite(t testing.TB, opts ...Option) *gorm.DB {
	t.Helper()
	return openSQLite(t, "", newSettings(SQLite, opts))
}

func openSQLite(t testing.TB, name string, s *settings) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(sqliteDSN(t, name, s)), s.gormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	applyPool(sqlDB, s)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if len(s.models) > 0 {
		require.NoError(t, db.AutoMigrate(s.models...))
	}
	return db
}

func sqliteDSN(t testing.TB, name string, s *settings) string {
	var pragmas []string
	if s.file {
		for _, p := range s.pragmas {
			pragmas = append(pragmas, "_pragma="+string(p))
		}
	}
	if s.foreignKeys {
		pragmas = append(pragmas, "_pragma=foreign_keys(1)")
	}

	if !s.file {
		query := append([]string{"mode=memory", "cache=shared"}, pragmas...)
		return "file:testsupport_" + uniqueSuffix() + "?" + strings.Join(query, "&")
	}
	fileName := s.fileName
	if fileName == "" {
		fileName = "test.db"
		if name != "" {
			fileName = name + ".db"
		}
	}
	dsn := "file:" + filepath.Join(t.TempDir(), fileName)
	if len(pragmas) > 0 {
		dsn += "?" + strings.Join(pragmas, "&")
	}
	return dsn
}

func applyPool(sqlDB *sql.DB, s *settings) {
	if s.maxOpen != unset {
		sqlDB.SetMaxOpenConns(s.maxOpen)
	}
	if s.maxIdle != unset {
		sqlDB.SetMaxIdleConns(s.maxIdle)
	}
}
