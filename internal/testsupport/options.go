// Package testsupport opens the databases Go tests run against: a private
// SQLite database, a disposable schema on the TEST_MYSQL_DSN server, or a
// second handle on a schema a test already has.
//
// It is test-only. Only _test.go files import it, so it is never linked into
// a shipped binary.
//
// Every opener registers its own cleanup with t.Cleanup: the pool is closed
// and, for OpenMySQL, the schema is dropped. Options tune what each site
// needs; an option that does not apply to the dialect being opened is
// ignored, and OnSQLite/OnMySQL scope a group of options to one dialect so a
// single option list can serve a test that runs on both.
package testsupport

import (
	"time"

	"gorm.io/gorm"
)

// Option adjusts how a database is opened.
type Option func(*settings)

// Pragma is an extra SQLite pragma for a file database (see File).
type Pragma string

const (
	// BusyTimeout adds busy_timeout(5000).
	BusyTimeout Pragma = "busy_timeout(5000)"
	// WAL adds journal_mode(WAL).
	WAL Pragma = "journal_mode(WAL)"
)

// unset marks a pool size that is left at the driver default: no
// SetMaxOpenConns or SetMaxIdleConns call is made at all.
const unset = -1

type settings struct {
	dialect Dialect
	models  []any

	// SQLite
	foreignKeys bool
	file        bool
	fileName    string
	pragmas     []Pragma

	// Pool, both dialects. unset leaves the driver default.
	maxOpen int
	maxIdle int

	gormHooks []func(*gorm.Config)

	// MySQL
	readTimeout  time.Duration
	writeTimeout time.Duration
	charset      string
	collation    string
}

func newSettings(d Dialect, opts []Option) *settings {
	s := &settings{
		dialect:      d,
		foreignKeys:  true,
		maxOpen:      unset,
		maxIdle:      unset,
		readTimeout:  30 * time.Second,
		writeTimeout: 30 * time.Second,
	}
	if d == SQLite {
		// One connection, as the production SQLite pool has.
		s.maxOpen = 1
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *settings) gormConfig() *gorm.Config {
	cfg := &gorm.Config{Logger: discardLogger}
	for _, hook := range s.gormHooks {
		hook(cfg)
	}
	return cfg
}

// Models are AutoMigrated after the database is opened (OpenSQLite,
// OpenMySQL). Repeated options append.
func Models(models ...any) Option {
	return func(s *settings) { s.models = append(s.models, models...) }
}

// WithoutForeignKeys opens a SQLite database without the foreign_keys(1)
// pragma, so the driver default (enforcement off) applies. MySQL ignores it.
func WithoutForeignKeys() Option {
	return func(s *settings) { s.foreignKeys = false }
}

// File opens SQLite on the file name under t.TempDir() instead of a private
// in-memory database, with the given pragmas in addition to foreign_keys(1)
// (unless WithoutForeignKeys is given). An empty name uses "<name>.db" for
// Open, or "test.db". A test that cancels a transaction needs a file: an
// in-memory database does not survive the driver discarding its connection.
// MySQL ignores it.
func File(name string, pragmas ...Pragma) Option {
	return func(s *settings) {
		s.file = true
		s.fileName = name
		s.pragmas = append([]Pragma(nil), pragmas...)
	}
}

// MaxOpenConns calls SetMaxOpenConns(n) on the pool. SQLite defaults to 1,
// MySQL to the driver default.
func MaxOpenConns(n int) Option {
	return func(s *settings) { s.maxOpen = n }
}

// MaxIdleConns calls SetMaxIdleConns(n) on the pool. Both dialects default to
// the driver default.
func MaxIdleConns(n int) Option {
	return func(s *settings) { s.maxIdle = n }
}

// DriverDefaultMaxOpenConns makes no SetMaxOpenConns call, leaving the
// database/sql default (unlimited). It undoes SQLite's one-connection default.
func DriverDefaultMaxOpenConns() Option {
	return func(s *settings) { s.maxOpen = unset }
}

// DriverDefaultMaxIdleConns makes no SetMaxIdleConns call, leaving the
// database/sql default.
func DriverDefaultMaxIdleConns() Option {
	return func(s *settings) { s.maxIdle = unset }
}

// GormConfig adjusts the *gorm.Config before the open (NowFunc,
// DisableForeignKeyConstraintWhenMigrating, PrepareStmt, ...). The logger
// starts as a discarding logger.
func GormConfig(hook func(*gorm.Config)) Option {
	return func(s *settings) { s.gormHooks = append(s.gormHooks, hook) }
}

// NowUTC sets gorm's NowFunc to time.Now().UTC().
func NowUTC() Option {
	return GormConfig(func(cfg *gorm.Config) {
		cfg.NowFunc = func() time.Time { return time.Now().UTC() }
	})
}

// Timeouts overrides the MySQL read and write timeouts (30s each by
// default). SQLite ignores it.
func Timeouts(read, write time.Duration) Option {
	return func(s *settings) { s.readTimeout, s.writeTimeout = read, write }
}

// SchemaCharset creates the OpenMySQL schema with this character set and
// collation instead of the server default. SQLite ignores it.
func SchemaCharset(charset, collation string) Option {
	return func(s *settings) { s.charset, s.collation = charset, collation }
}

// Group bundles options into one, so a test file can keep a site's settings
// in one package-level value and still add to them at a call site.
func Group(opts ...Option) Option {
	return func(s *settings) {
		for _, opt := range opts {
			opt(s)
		}
	}
}

// PrepareStmt turns on gorm's PrepareStmt.
func PrepareStmt() Option {
	return GormConfig(func(cfg *gorm.Config) { cfg.PrepareStmt = true })
}

// DisableForeignKeyConstraintWhenMigrating turns on the gorm setting of that
// name, so AutoMigrate creates no foreign keys.
func DisableForeignKeyConstraintWhenMigrating() Option {
	return GormConfig(func(cfg *gorm.Config) { cfg.DisableForeignKeyConstraintWhenMigrating = true })
}

// OnSQLite applies the options only when the database is SQLite.
func OnSQLite(opts ...Option) Option {
	return onDialect(SQLite, opts)
}

// OnMySQL applies the options only when the database is MySQL.
func OnMySQL(opts ...Option) Option {
	return onDialect(MySQL, opts)
}

func onDialect(d Dialect, opts []Option) Option {
	return func(s *settings) {
		if s.dialect != d {
			return
		}
		for _, opt := range opts {
			opt(s)
		}
	}
}
