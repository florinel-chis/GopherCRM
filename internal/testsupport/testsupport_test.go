package testsupport

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type supportRow struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

// recordingTB stands in for a testing.TB so a test can watch an opener skip
// or fail without that skip or failure reaching the real test. Skip* and
// Fatal*/FailNow stop the goroutine with runtime.Goexit, as testing does.
type recordingTB struct {
	testing.TB

	mu       sync.Mutex
	skipped  []string
	errors   []string
	failed   bool
	cleanups []func()
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) record(list *[]string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*list = append(*list, strings.TrimSuffix(fmt.Sprintln(args...), "\n"))
}

func (r *recordingTB) Skip(args ...any)                 { r.record(&r.skipped, args...); runtime.Goexit() }
func (r *recordingTB) Skipf(format string, args ...any) { r.Skip(fmt.Sprintf(format, args...)) }
func (r *recordingTB) SkipNow()                         { r.Skip() }
func (r *recordingTB) Error(args ...any)                { r.record(&r.errors, args...); r.Fail() }
func (r *recordingTB) Errorf(format string, args ...any) {
	r.Error(fmt.Sprintf(format, args...))
}
func (r *recordingTB) Fatal(args ...any)                 { r.Error(args...); r.FailNow() }
func (r *recordingTB) Fatalf(format string, args ...any) { r.Fatal(fmt.Sprintf(format, args...)) }
func (r *recordingTB) Fail()                             { r.mu.Lock(); r.failed = true; r.mu.Unlock() }
func (r *recordingTB) FailNow()                          { r.Fail(); runtime.Goexit() }
func (r *recordingTB) Failed() bool                      { r.mu.Lock(); defer r.mu.Unlock(); return r.failed }
func (r *recordingTB) Cleanup(f func())                  { r.cleanups = append(r.cleanups, f) }

// runRecorded runs fn against a recordingTB on its own goroutine, then runs
// the cleanups fn registered, last first.
func runRecorded(t *testing.T, fn func(tb testing.TB)) *recordingTB {
	t.Helper()
	rec := &recordingTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(rec)
	}()
	<-done
	for i := len(rec.cleanups) - 1; i >= 0; i-- {
		rec.cleanups[i]()
	}
	return rec
}

func poolOf(t *testing.T, db *gorm.DB) *sql.DB {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	return sqlDB
}

// idleAfter holds n connections at once, releases them and reports how many
// the pool kept idle.
func idleAfter(t *testing.T, sqlDB *sql.DB, n int) int {
	t.Helper()
	ctx := context.Background()
	conns := make([]*sql.Conn, 0, n)
	for i := 0; i < n; i++ {
		conn, err := sqlDB.Conn(ctx)
		require.NoError(t, err)
		conns = append(conns, conn)
	}
	for _, conn := range conns {
		require.NoError(t, conn.Close())
	}
	return sqlDB.Stats().Idle
}

func foreignKeysPragma(t *testing.T, db *gorm.DB) int {
	t.Helper()
	var on int
	require.NoError(t, db.Raw("PRAGMA foreign_keys").Scan(&on).Error)
	return on
}

func TestOpenSQLiteMigratesWithOneConnection(t *testing.T) {
	db := OpenSQLite(t, Models(&supportRow{}))
	assert.True(t, db.Migrator().HasTable(&supportRow{}), "the models are migrated")
	assert.Equal(t, 1, poolOf(t, db).Stats().MaxOpenConnections, "one open connection by default")
	require.NoError(t, db.Create(&supportRow{Name: "a"}).Error)
}

func TestOpenSQLiteCallsArePrivate(t *testing.T) {
	first := OpenSQLite(t, Models(&supportRow{}))
	second := OpenSQLite(t, Models(&supportRow{}))
	require.NoError(t, first.Create(&supportRow{Name: "only in first"}).Error)

	var count int64
	require.NoError(t, second.Model(&supportRow{}).Count(&count).Error)
	assert.Zero(t, count, "a second call must not see the first call's rows")

	// The same holds when the pool hands out several connections: they all
	// see their own call's database, and only that one.
	wide := OpenSQLite(t, Models(&supportRow{}), DriverDefaultMaxOpenConns())
	require.NoError(t, wide.Create(&supportRow{Name: "wide"}).Error)
	require.NoError(t, first.Model(&supportRow{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestOpenSQLiteForeignKeys(t *testing.T) {
	assert.Equal(t, 1, foreignKeysPragma(t, OpenSQLite(t)), "on by default in memory")
	assert.Equal(t, 0, foreignKeysPragma(t, OpenSQLite(t, WithoutForeignKeys())), "off with the option in memory")
	assert.Equal(t, 1, foreignKeysPragma(t, OpenSQLite(t, File("fk.db"))), "on by default in a file")
	assert.Equal(t, 0, foreignKeysPragma(t, OpenSQLite(t, File("nofk.db", BusyTimeout), WithoutForeignKeys())), "off with the option in a file")
}

func TestOpenSQLiteFilePragmas(t *testing.T) {
	db := OpenSQLite(t, File("pragmas.db", BusyTimeout, WAL))
	var busy int
	require.NoError(t, db.Raw("PRAGMA busy_timeout").Scan(&busy).Error)
	assert.Equal(t, 5000, busy)
	var journal string
	require.NoError(t, db.Raw("PRAGMA journal_mode").Scan(&journal).Error)
	assert.Equal(t, "wal", strings.ToLower(journal))

	var file string
	require.NoError(t, db.Raw("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&file).Error)
	assert.True(t, strings.HasSuffix(file, "pragmas.db"), "the database is the named file, got %q", file)

	plain := OpenSQLite(t, File("plain.db"))
	require.NoError(t, plain.Raw("PRAGMA journal_mode").Scan(&journal).Error)
	assert.NotEqual(t, "wal", strings.ToLower(journal), "WAL only when asked for")

	// The driver's own busy timeout is also 5000 ms, so the pragma set is
	// checked on the DSN rather than read back.
	dsn := func(opts ...Option) string { return sqliteDSN(t, "", newSettings(SQLite, opts)) }
	assert.True(t, strings.HasSuffix(dsn(File("a.db")), "a.db?_pragma=foreign_keys(1)"))
	assert.True(t, strings.HasSuffix(dsn(File("b.db", BusyTimeout), WithoutForeignKeys()), "b.db?_pragma=busy_timeout(5000)"))
	assert.True(t, strings.HasSuffix(dsn(File("c.db", BusyTimeout, WAL)), "c.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"))
	assert.True(t, strings.HasSuffix(dsn(File("d.db"), WithoutForeignKeys()), "d.db"))
	memory := dsn()
	assert.True(t, strings.HasPrefix(memory, "file:testsupport_"), memory)
	assert.True(t, strings.HasSuffix(memory, "?mode=memory&cache=shared&_pragma=foreign_keys(1)"), memory)
	assert.True(t, strings.HasSuffix(dsn(WithoutForeignKeys()), "?mode=memory&cache=shared"))
}

func TestOpenSQLitePoolSizesAreIndependent(t *testing.T) {
	// Neither given beyond lifting the one-connection default: database/sql
	// defaults, unlimited open and two idle.
	defaults := poolOf(t, OpenSQLite(t, DriverDefaultMaxOpenConns()))
	assert.Equal(t, 0, defaults.Stats().MaxOpenConnections)
	assert.Equal(t, 2, idleAfter(t, defaults, 4))

	openOnly := poolOf(t, OpenSQLite(t, MaxOpenConns(5)))
	assert.Equal(t, 5, openOnly.Stats().MaxOpenConnections)
	assert.Equal(t, 2, idleAfter(t, openOnly, 5), "max idle stays at the driver default")

	idleOnly := poolOf(t, OpenSQLite(t, DriverDefaultMaxOpenConns(), MaxIdleConns(3)))
	assert.Equal(t, 0, idleOnly.Stats().MaxOpenConnections, "max open stays at the driver default")
	assert.Equal(t, 3, idleAfter(t, idleOnly, 5))

	both := poolOf(t, OpenSQLite(t, MaxOpenConns(1), MaxIdleConns(1)))
	assert.Equal(t, 1, both.Stats().MaxOpenConnections)
	assert.Equal(t, 1, idleAfter(t, both, 1))
}

func TestOpenSQLiteGormConfig(t *testing.T) {
	fixed := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	db := OpenSQLite(t, GormConfig(func(cfg *gorm.Config) {
		cfg.NowFunc = func() time.Time { return fixed }
		cfg.PrepareStmt = true
	}))
	assert.Equal(t, fixed, db.Config.NowFunc())
	assert.True(t, db.Config.PrepareStmt)

	utc := OpenSQLite(t, NowUTC())
	assert.Equal(t, time.UTC, utc.Config.NowFunc().Location())
}

func TestOpenSQLiteClosesThePoolAfterTheTest(t *testing.T) {
	var memory, file *sql.DB
	t.Run("open", func(t *testing.T) {
		memory = poolOf(t, OpenSQLite(t))
		file = poolOf(t, OpenSQLite(t, File("closed.db")))
		require.NoError(t, memory.Ping())
		require.NoError(t, file.Ping())
	})
	assert.ErrorContains(t, memory.Ping(), "database is closed")
	assert.ErrorContains(t, file.Ping(), "database is closed")
}

func TestDialectFor(t *testing.T) {
	assert.Equal(t, MySQL, DialectFor(true))
	assert.Equal(t, SQLite, DialectFor(false))
}

func TestGroupAndGormSwitches(t *testing.T) {
	db := OpenSQLite(t, Group(PrepareStmt(), DisableForeignKeyConstraintWhenMigrating(), MaxIdleConns(1)))
	assert.True(t, db.Config.PrepareStmt)
	assert.True(t, db.Config.DisableForeignKeyConstraintWhenMigrating)
	s := newSettings(SQLite, []Option{Group(OnMySQL(MaxOpenConns(4)), OnSQLite(File("x.db", BusyTimeout)))})
	assert.Equal(t, 1, s.maxOpen)
	assert.True(t, s.file)
	assert.Equal(t, []Pragma{BusyTimeout}, s.pragmas)
}

func TestDialectOptionsApplyToTheirDialectOnly(t *testing.T) {
	s := newSettings(SQLite, []Option{OnMySQL(MaxOpenConns(7)), OnSQLite(MaxIdleConns(3))})
	assert.Equal(t, 1, s.maxOpen)
	assert.Equal(t, 3, s.maxIdle)

	s = newSettings(MySQL, []Option{OnMySQL(MaxOpenConns(7)), OnSQLite(MaxIdleConns(3))})
	assert.Equal(t, 7, s.maxOpen)
	assert.Equal(t, unset, s.maxIdle, "MySQL leaves the pool at the driver default")
	assert.Equal(t, 30*time.Second, s.readTimeout)

	s = newSettings(MySQL, []Option{Timeouts(60*time.Second, 60*time.Second)})
	assert.Equal(t, 60*time.Second, s.readTimeout)
	assert.Equal(t, 60*time.Second, s.writeTimeout)
}

func TestMySQLFromEnvSkipsWithoutTheVariable(t *testing.T) {
	rec := runRecorded(t, func(tb testing.TB) {
		mysqlFromDSN(tb, "")
		tb.Error("mysqlFromDSN returned instead of skipping")
	})
	assert.Equal(t, []string{"TEST_MYSQL_DSN is required for the disposable MySQL regression suite"}, rec.skipped)
	assert.Empty(t, rec.errors)
	assert.False(t, rec.Failed())
}

func TestMySQLFromEnvParsesTheServer(t *testing.T) {
	var got string
	var parseTime bool
	var timeouts [3]time.Duration
	var loc *time.Location
	rec := runRecorded(t, func(tb testing.TB) {
		cfg := mysqlFromDSN(tb, "u:p@tcp(127.0.0.1:1)/named?readTimeout=1s")
		got, parseTime, loc = cfg.DBName, cfg.ParseTime, cfg.Loc
		timeouts = [3]time.Duration{cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout}
	})
	require.Empty(t, rec.skipped)
	require.False(t, rec.Failed(), "%v", rec.errors)
	assert.Empty(t, got, "the database name is dropped")
	assert.True(t, parseTime)
	assert.Equal(t, time.UTC, loc)
	assert.Equal(t, [3]time.Duration{5 * time.Second, 30 * time.Second, 30 * time.Second}, timeouts)
}

func TestOpenMySQLFailsOnAnUnreachableServer(t *testing.T) {
	var returned bool
	rec := runRecorded(t, func(tb testing.TB) {
		server := mysqlFromDSN(tb, "u:p@tcp(127.0.0.1:1)/")
		server.Timeout = 2 * time.Second
		openMySQL(tb, server, "unreachable", newSettings(MySQL, nil))
		returned = true
	})
	assert.False(t, returned, "the open must stop the test")
	assert.Empty(t, rec.skipped, "an unreachable server fails, it does not skip")
	assert.True(t, rec.Failed())
	require.NotEmpty(t, rec.errors)
	assert.Contains(t, strings.Join(rec.errors, "\n"), "TEST_MYSQL_DSN is configured but the server is unavailable")
}

func TestSchemaNameIsUniqueAndBounded(t *testing.T) {
	a, b := schemaName("pagination"), schemaName("pagination")
	assert.NotEqual(t, a, b)
	assert.True(t, strings.HasPrefix(a, "gocrm_pagination_"), a)
	long := schemaName(strings.Repeat("x", 80))
	assert.LessOrEqual(t, len(long), 64)
	assert.True(t, strings.HasPrefix(long, "gocrm_xxx"), long)
}

func TestForEachDialectRunsSQLiteAndMySQLSubtests(t *testing.T) {
	var ran []string
	var checked []string
	forEachDialect(t, func(tb testing.TB) { checked = append(checked, tb.Name()) }, func(t *testing.T, d Dialect) {
		ran = append(ran, t.Name()+"="+d.String())
	})
	assert.Equal(t, []string{
		"TestForEachDialectRunsSQLiteAndMySQLSubtests/sqlite=sqlite",
		"TestForEachDialectRunsSQLiteAndMySQLSubtests/mysql=mysql",
	}, ran)
	assert.Equal(t, []string{"TestForEachDialectRunsSQLiteAndMySQLSubtests/mysql"}, checked, "only the mysql subtest needs the server")
}

func TestOpenDispatchesToSQLite(t *testing.T) {
	db := Open(t, SQLite, "dispatch", File(""), Models(&supportRow{}))
	var file string
	require.NoError(t, db.Raw("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&file).Error)
	assert.True(t, strings.HasSuffix(file, "dispatch.db"), "Open names the file after the name, got %q", file)
	assert.True(t, db.Migrator().HasTable(&supportRow{}))
}

// The roots below need a real server and skip without TEST_MYSQL_DSN.

func schemaExists(t *testing.T, schema string) bool {
	t.Helper()
	server, err := sql.Open("mysql", MySQLFromEnv(t).FormatDSN())
	require.NoError(t, err)
	defer server.Close()
	var n int
	require.NoError(t, server.QueryRow("SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", schema).Scan(&n))
	return n == 1
}

func TestOpenMySQLDropsSchemaMySQL(t *testing.T) {
	MySQLFromEnv(t)
	var schema string
	var pool *sql.DB
	t.Run("open", func(t *testing.T) {
		db := OpenMySQL(t, "testsupport_drop", Models(&supportRow{}), MaxOpenConns(3), MaxIdleConns(2))
		schema = db.Migrator().CurrentDatabase()
		pool = poolOf(t, db)
		assert.True(t, strings.HasPrefix(schema, "gocrm_testsupport_drop_"), schema)
		assert.Equal(t, 3, pool.Stats().MaxOpenConnections)
		assert.True(t, db.Migrator().HasTable(&supportRow{}))
		require.NoError(t, db.Create(&supportRow{Name: "a"}).Error)
		require.True(t, schemaExists(t, schema))
	})
	require.NotEmpty(t, schema)
	assert.False(t, schemaExists(t, schema), "the schema is dropped after the test")
	assert.ErrorContains(t, pool.Ping(), "database is closed")
}

func TestOpenMySQLSchemaKeepsSchemaMySQL(t *testing.T) {
	db := OpenMySQL(t, "testsupport_keep", Models(&supportRow{}))
	schema := db.Migrator().CurrentDatabase()
	require.NoError(t, db.Create(&supportRow{Name: "written by the first handle"}).Error)

	var pool *sql.DB
	t.Run("second handle", func(t *testing.T) {
		second := OpenMySQLSchema(t, schema, MaxOpenConns(4))
		pool = poolOf(t, second)
		assert.Equal(t, 4, pool.Stats().MaxOpenConnections)
		var row supportRow
		require.NoError(t, second.First(&row).Error)
		assert.Equal(t, "written by the first handle", row.Name)
	})
	assert.ErrorContains(t, pool.Ping(), "database is closed", "the second handle is closed after its test")
	assert.True(t, schemaExists(t, schema), "the schema outlives the second handle")
	var count int64
	require.NoError(t, db.Model(&supportRow{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "the first handle still works")
}

func TestOpenMySQLSchemaCharsetMySQL(t *testing.T) {
	db := OpenMySQL(t, "testsupport_charset", SchemaCharset("utf8mb4", "utf8mb4_unicode_ci"))
	var collation string
	require.NoError(t, db.Raw("SELECT DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = DATABASE()").Scan(&collation).Error)
	assert.Equal(t, "utf8mb4_unicode_ci", collation)
}

func TestForEachDialectMySQL(t *testing.T) {
	MySQLFromEnv(t)
	var ran []Dialect
	ForEachDialect(t, func(t *testing.T, d Dialect) {
		db := Open(t, d, "testsupport_each", Models(&supportRow{}))
		require.NoError(t, db.Create(&supportRow{Name: d.String()}).Error)
		ran = append(ran, d)
	})
	assert.Equal(t, []Dialect{SQLite, MySQL}, ran)
}
