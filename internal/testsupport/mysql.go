package testsupport

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// MySQLDSNEnv names the variable that points the MySQL-backed tests at a
// server: user:pass@tcp(host:port)/, without a database name.
const MySQLDSNEnv = "TEST_MYSQL_DSN"

const (
	skipMessage        = "TEST_MYSQL_DSN is required for the disposable MySQL regression suite"
	unavailableMessage = "TEST_MYSQL_DSN is configured but the server is unavailable"
	maxSchemaName      = 64 // MySQL's identifier limit
)

// MySQLFromEnv returns the server configuration TEST_MYSQL_DSN names, or
// skips the test when the variable is empty.
//
// The copy has no database name, ParseTime on, and a 5s connect and 30s
// read/write timeout. Loc stays the driver default, time.UTC, which is what
// the tests that set it explicitly asked for. The DSN is never printed.
func MySQLFromEnv(t testing.TB) *mysqldriver.Config {
	t.Helper()
	return mysqlFromDSN(t, os.Getenv(MySQLDSNEnv))
}

func mysqlFromDSN(t testing.TB, raw string) *mysqldriver.Config {
	t.Helper()
	if raw == "" {
		t.Skip(skipMessage)
	}
	cfg, err := mysqldriver.ParseDSN(raw)
	// The parse error can quote the DSN; it is not passed on.
	require.True(t, err == nil, "TEST_MYSQL_DSN is not a valid MySQL DSN")
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
	return cfg
}

// OpenMySQL creates a uniquely named schema gocrm_<name>_<unique> on the
// TEST_MYSQL_DSN server, opens gorm on it and AutoMigrates the Models option.
// It skips without the variable and fails, never skips, when the server is
// unreachable. In t.Cleanup the pool is closed and the schema dropped.
//
// Pool sizes stay at the driver default unless MaxOpenConns/MaxIdleConns are
// given; Timeouts overrides the 30s read/write timeouts and SchemaCharset the
// server's default character set.
func OpenMySQL(t testing.TB, name string, opts ...Option) *gorm.DB {
	t.Helper()
	return openMySQL(t, MySQLFromEnv(t), name, newSettings(MySQL, opts))
}

func openMySQL(t testing.TB, server *mysqldriver.Config, name string, s *settings) *gorm.DB {
	t.Helper()
	cfg := server.Clone()
	cfg.DBName = ""
	cfg.ReadTimeout, cfg.WriteTimeout = s.readTimeout, s.writeTimeout
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	schema := schemaName(name)
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS `" + schema + "`")
		_ = admin.Close()
	})
	create := "CREATE DATABASE `" + schema + "`"
	if s.charset != "" {
		create += " CHARACTER SET " + s.charset
	}
	if s.collation != "" {
		create += " COLLATE " + s.collation
	}
	_, err = admin.Exec(create)
	require.NoError(t, err, unavailableMessage)

	cfg.DBName = schema
	db := openMySQLHandle(t, cfg, s)
	if len(s.models) > 0 {
		require.NoError(t, db.AutoMigrate(s.models...))
	}
	return db
}

// OpenMySQLSchema opens another handle on the existing schema on the
// TEST_MYSQL_DSN server. It creates, migrates and drops nothing; its own pool
// is closed in t.Cleanup. Pool sizes, timeouts and GormConfig apply as for
// OpenMySQL; Models is ignored.
func OpenMySQLSchema(t testing.TB, schema string, opts ...Option) *gorm.DB {
	t.Helper()
	require.NotEmpty(t, schema, "OpenMySQLSchema needs the schema name")
	s := newSettings(MySQL, opts)
	cfg := MySQLFromEnv(t)
	cfg.DBName = schema
	cfg.ReadTimeout, cfg.WriteTimeout = s.readTimeout, s.writeTimeout
	return openMySQLHandle(t, cfg, s)
}

func openMySQLHandle(t testing.TB, cfg *mysqldriver.Config, s *settings) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), s.gormConfig())
	require.NoError(t, err, unavailableMessage)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	applyPool(sqlDB, s)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// schemaName is gocrm_<name>_<nanoseconds>_<counter>, the name shortened so
// the whole stays within MySQL's 64-character limit.
func schemaName(name string) string {
	suffix := "_" + uniqueSuffix()
	prefix := "gocrm_" + name
	if room := maxSchemaName - len(suffix); len(prefix) > room {
		prefix = prefix[:room]
	}
	return fmt.Sprintf("%s%s", prefix, suffix)
}
