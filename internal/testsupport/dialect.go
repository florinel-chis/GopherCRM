package testsupport

import (
	"testing"

	"gorm.io/gorm"
)

// Dialect is the database a test opens.
type Dialect int

const (
	// SQLite is a private SQLite database (OpenSQLite).
	SQLite Dialect = iota
	// MySQL is a disposable schema on the TEST_MYSQL_DSN server (OpenMySQL).
	MySQL
)

// DialectFor is MySQL when useMySQL is true and SQLite otherwise, for the
// tests that pick the database with a boolean.
func DialectFor(useMySQL bool) Dialect {
	if useMySQL {
		return MySQL
	}
	return SQLite
}

// String is the subtest name ForEachDialect uses.
func (d Dialect) String() string {
	if d == MySQL {
		return "mysql"
	}
	return "sqlite"
}

// Open opens d: OpenSQLite for SQLite (name is the default File name),
// OpenMySQL for MySQL (name goes into the schema name).
func Open(t testing.TB, d Dialect, name string, opts ...Option) *gorm.DB {
	t.Helper()
	s := newSettings(d, opts)
	if d == MySQL {
		return openMySQL(t, MySQLFromEnv(t), name, s)
	}
	return openSQLite(t, name, s)
}

// ForEachDialect runs body in the subtests "sqlite" and "mysql". The mysql
// subtest skips through MySQLFromEnv when TEST_MYSQL_DSN is empty.
func ForEachDialect(t *testing.T, body func(t *testing.T, d Dialect)) {
	t.Helper()
	forEachDialect(t, func(t testing.TB) { MySQLFromEnv(t) }, body)
}

func forEachDialect(t *testing.T, requireMySQL func(testing.TB), body func(t *testing.T, d Dialect)) {
	t.Helper()
	for _, d := range []Dialect{SQLite, MySQL} {
		t.Run(d.String(), func(t *testing.T) {
			if d == MySQL {
				requireMySQL(t)
			}
			body(t, d)
		})
	}
}
