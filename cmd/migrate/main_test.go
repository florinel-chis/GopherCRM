package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestCheckCommandSupported_SQLiteRejectsDatabaseCommands(t *testing.T) {
	for _, command := range []string{"up", "down", "goto", "drop", "force", "version"} {
		t.Run(command, func(t *testing.T) {
			err := checkCommandSupported(command, config.DriverSQLite)
			require.ErrorIs(t, err, ErrSQLiteUnsupported)
			assert.Equal(t,
				"SQL migrations are MySQL-only; SQLite schemas are created by auto-migration at startup",
				err.Error())
		})
	}
}

func TestCheckCommandSupported_SQLiteAllowsCreate(t *testing.T) {
	assert.NoError(t, checkCommandSupported(commandCreate, config.DriverSQLite))
}

func TestCheckCommandSupported_MySQLAllowsEveryCommand(t *testing.T) {
	for _, command := range []string{"up", "down", "goto", "drop", "force", "version", commandCreate, "bogus"} {
		assert.NoError(t, checkCommandSupported(command, config.DriverMySQL), "command %q", command)
	}
}

func TestMigrationDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "appends with ampersand when query parameters exist",
			dsn:  "root:secret@tcp(localhost:3306)/gocrm?charset=utf8mb4&parseTime=True&loc=Local",
			want: "root:secret@tcp(localhost:3306)/gocrm?charset=utf8mb4&parseTime=True&loc=Local&multiStatements=true",
		},
		{
			name: "appends with question mark when no query parameters exist",
			dsn:  "root:secret@tcp(localhost:3306)/gocrm",
			want: "root:secret@tcp(localhost:3306)/gocrm?multiStatements=true",
		},
		{
			name: "leaves DSN untouched if multiStatements is already present",
			dsn:  "root:secret@tcp(localhost:3306)/gocrm?multiStatements=true&charset=utf8mb4",
			want: "root:secret@tcp(localhost:3306)/gocrm?multiStatements=true&charset=utf8mb4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, migrationDSN(tt.dsn))
		})
	}
}

func TestMigrationFiles_PairsAndCompleteness(t *testing.T) {
	migrateDir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(migrateDir)
	require.NoError(t, err)

	upFiles := make(map[string]bool)
	downFiles := make(map[string]bool)
	allSQL := strings.Builder{}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		path := filepath.Join(migrateDir, entry.Name())
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.NotEmpty(t, strings.TrimSpace(string(data)), "%s must not be empty", entry.Name())

		if strings.HasSuffix(entry.Name(), ".up.sql") {
			base := strings.TrimSuffix(entry.Name(), ".up.sql")
			upFiles[base] = true
			allSQL.Write(data)
			allSQL.WriteString("\n")
		} else if strings.HasSuffix(entry.Name(), ".down.sql") {
			base := strings.TrimSuffix(entry.Name(), ".down.sql")
			downFiles[base] = true
		}
	}

	// Every up migration must have a matching down migration and vice versa
	for up := range upFiles {
		assert.True(t, downFiles[up], "missing down migration for %s.up.sql", up)
	}
	for down := range downFiles {
		assert.True(t, upFiles[down], "missing up migration for %s.down.sql", down)
	}

	// Verify all expected tables from models.MigrateDatabase() are created across the up migrations
	expectedTables := []string{
		"users", "customers", "leads", "tickets", "tasks", "api_keys",
		"configurations", "refresh_tokens", "companies", "deals", "deal_stage_changes",
		"labels", "task_labels", "password_reset_tokens", "bulk_operations",
		"bulk_operation_items", "aeo_profiles", "aeo_prompts", "aeo_runs",
		"aeo_answers", "aeo_citations", "forms", "form_submissions", "form_confirmation_tokens", "form_mail_deliveries",
		"products", "quotes", "quote_lines", "quote_status_changes",
		"automation_rules", "automation_steps", "automation_events", "automation_runs", "automation_run_steps", "notifications",
		"customer_external_ids", "product_external_ids", "ticket_mail_deliveries", "form_ticket_keys",
		"roles", "role_permissions", "role_audit_log",
		"webhook_endpoints", "webhook_deliveries",
		"quote_response_tokens",
		"user_identities", "oidc_login_attempts", "auth_audit_log", "scim_tokens",
		"scim_group_members",
	}

	combinedSQL := strings.ToLower(allSQL.String())
	for _, table := range expectedTables {
		pattern1 := "create table " + table + " "
		pattern2 := "create table if not exists " + table + " "
		assert.True(t, strings.Contains(combinedSQL, pattern1) || strings.Contains(combinedSQL, pattern2),
			"table %q should be created in migrations", table)
	}
}

func TestMigrationSequence_UpgradePathFromExistingMain(t *testing.T) {
	migrateDir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(migrateDir)
	require.NoError(t, err)

	type migrationEntry struct {
		version string
		name    string
	}
	var upMigrations []migrationEntry

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		require.Len(t, parts, 2, "migration file must follow <version>_<name>.up.sql format")
		upMigrations = append(upMigrations, migrationEntry{
			version: parts[0],
			name:    entry.Name(),
		})
	}

	require.NotEmpty(t, upMigrations)

	// Ensure strictly ascending order
	for i := 1; i < len(upMigrations); i++ {
		assert.True(t, upMigrations[i-1].version < upMigrations[i].version,
			"migrations must be strictly sorted: %s should be before %s",
			upMigrations[i-1].name, upMigrations[i].name)
	}

	// The last migration present on main before this branch was 20260930120000_add_deals.
	const mainHeadVersion = "20260930120000"
	var pendingMigrations []migrationEntry
	var foundMainHead bool

	for _, m := range upMigrations {
		if m.version == mainHeadVersion {
			foundMainHead = true
			continue
		}
		if m.version > mainHeadVersion {
			pendingMigrations = append(pendingMigrations, m)
		}
	}

	require.True(t, foundMainHead, "main head migration %s must be present in migrations directory", mainHeadVersion)
	require.GreaterOrEqual(t, len(pendingMigrations), 2, "expected at least 2 pending migrations after main head")

	// First pending migration MUST be schema parity so existing databases don't skip it
	assert.Contains(t, pendingMigrations[0].name, "v1_2_0_schema_parity",
		"first migration after main must apply schema parity, got %s", pendingMigrations[0].name)
	assert.Equal(t, "20261001120000", pendingMigrations[0].version)

	// Second pending migration is ticket comments
	assert.Contains(t, pendingMigrations[1].name, "add_ticket_comments",
		"ticket comments migration should follow schema parity, got %s", pendingMigrations[1].name)
}

// mysqlBaseDSNFromEnv is the decision behind testMySQLBaseDSN: it reads only
// TEST_MYSQL_DSN and reports ok=false for an unset or blank value. A value that
// names an unreachable server is returned as is, so the calling test fails at
// its first statement instead of falling back to another server. A query string
// is kept unchanged; a param-free DSN without a trailing slash gets one
// appended.
func mysqlBaseDSNFromEnv() (dsn string, ok bool) {
	dsn = strings.TrimSpace(os.Getenv("TEST_MYSQL_DSN"))
	if dsn == "" {
		return "", false
	}
	if !strings.Contains(dsn, "?") && !strings.HasSuffix(dsn, "/") {
		dsn += "/"
	}
	return dsn, true
}

// testMySQLBaseDSN returns the server-level DSN (no database name) the
// MySQL/MariaDB migration roots run against, as mysqlBaseDSNFromEnv decides
// it; without one it skips the calling test.
func testMySQLBaseDSN(t testing.TB) string {
	t.Helper()
	dsn, ok := mysqlBaseDSNFromEnv()
	if !ok {
		t.Skip("TEST_MYSQL_DSN is not set: skipping the MySQL/MariaDB migration test")
	}
	return dsn
}

// mysqlTestDSN builds the DSN of database dbName on the server named by base
// (dbName "" gives the server-level form). It parses base with the driver, so
// a base carrying a query string works. The root's own parameters
// (multiStatements=true, parseTime=true) win over the caller's values for the
// same keys; every other caller parameter (timeout, charset, loc, tls, ...) is
// kept, and a database named in base is replaced. The error never carries the
// DSN, which holds the password.
func mysqlTestDSN(base, dbName string) (string, error) {
	cfg, err := mysqldriver.ParseDSN(base)
	if err != nil {
		return "", fmt.Errorf("parse base MySQL DSN: %w", err)
	}
	cfg.DBName = dbName
	cfg.MultiStatements = true
	cfg.ParseTime = true
	return cfg.FormatDSN(), nil
}

// requireMySQLTestDSN is mysqlTestDSN for a test: a parse error fails it.
func requireMySQLTestDSN(t *testing.T, base, dbName string) string {
	t.Helper()
	dsn, err := mysqlTestDSN(base, dbName)
	require.NoError(t, err)
	return dsn
}

func TestMySQLTestDSN(t *testing.T) {
	parse := func(t *testing.T, dsn string) *mysqldriver.Config {
		t.Helper()
		cfg, err := mysqldriver.ParseDSN(dsn)
		require.NoError(t, err)
		return cfg
	}
	const paramBase = "u:p@tcp(127.0.0.1:3306)/?parseTime=false&multiStatements=false&timeout=7s"

	t.Run("param-free base", func(t *testing.T) {
		got, err := mysqlTestDSN("u:p@tcp(127.0.0.1:3306)/", "gocrm_x")
		require.NoError(t, err)
		cfg := parse(t, got)
		assert.Equal(t, "gocrm_x", cfg.DBName)
		assert.True(t, cfg.MultiStatements)
		assert.True(t, cfg.ParseTime)
		assert.Equal(t, "127.0.0.1:3306", cfg.Addr)
		assert.Equal(t, "tcp", cfg.Net)
		assert.Equal(t, "u", cfg.User)
		assert.Equal(t, "p", cfg.Passwd)
		assert.Empty(t, cfg.Params)
	})
	t.Run("root parameters win and caller parameters are kept", func(t *testing.T) {
		got, err := mysqlTestDSN(paramBase, "gocrm_x")
		require.NoError(t, err)
		cfg := parse(t, got)
		assert.Equal(t, "gocrm_x", cfg.DBName)
		assert.True(t, cfg.ParseTime)
		assert.True(t, cfg.MultiStatements)
		assert.Equal(t, 7*time.Second, cfg.Timeout)
		assert.Equal(t, "127.0.0.1:3306", cfg.Addr)
	})
	t.Run("database named in base is replaced", func(t *testing.T) {
		got, err := mysqlTestDSN("u:p@tcp(127.0.0.1:3306)/other?timeout=7s", "gocrm_x")
		require.NoError(t, err)
		cfg := parse(t, got)
		assert.Equal(t, "gocrm_x", cfg.DBName)
		assert.Equal(t, 7*time.Second, cfg.Timeout)
	})
	t.Run("server form has no database", func(t *testing.T) {
		got, err := mysqlTestDSN(paramBase, "")
		require.NoError(t, err)
		cfg := parse(t, got)
		assert.Equal(t, "", cfg.DBName)
		assert.True(t, cfg.MultiStatements)
		assert.True(t, cfg.ParseTime)
		assert.Equal(t, 7*time.Second, cfg.Timeout)
	})
	t.Run("unparseable base returns an error", func(t *testing.T) {
		got, err := mysqlTestDSN("u:p@tcp(127.0.0.1:3306)/?parseTime=true/", "gocrm_x")
		require.Error(t, err)
		assert.Empty(t, got)
		assert.NotContains(t, err.Error(), "u:p@")
	})
}

// skipRecorder is a testing.TB for testMySQLBaseDSN that records a requested
// skip instead of skipping a real test, so the skip path is checked without
// emitting a skip event (the release SQL gate rejects every skip). Only Helper
// and Skip are implemented; any other method hits the nil embedded TB.
type skipRecorder struct {
	testing.TB
	skipped bool
	message string
}

func (r *skipRecorder) Helper() {}

func (r *skipRecorder) Skip(args ...any) {
	r.skipped = true
	r.message = fmt.Sprint(args...)
	runtime.Goexit()
}

// callTestMySQLBaseDSN runs testMySQLBaseDSN against a skipRecorder in its own
// goroutine, so Skip's Goexit stops only the helper. returned reports whether
// the helper came back normally.
func callTestMySQLBaseDSN() (rec *skipRecorder, got string, returned bool) {
	rec = &skipRecorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		got = testMySQLBaseDSN(rec)
		returned = true
	}()
	<-done
	return rec, got, returned
}

func TestMySQLBaseDSNFromEnvironmentOnly(t *testing.T) {
	const skipMessage = "TEST_MYSQL_DSN is not set: skipping the MySQL/MariaDB migration test"
	assertSkips := func(t *testing.T) {
		t.Helper()
		dsn, ok := mysqlBaseDSNFromEnv()
		assert.False(t, ok)
		assert.Empty(t, dsn)
		rec, got, returned := callTestMySQLBaseDSN()
		assert.True(t, rec.skipped, "testMySQLBaseDSN must request a skip")
		assert.Equal(t, skipMessage, rec.message)
		assert.False(t, returned, "testMySQLBaseDSN must not return after requesting a skip")
		assert.Empty(t, got)
	}
	assertReturns := func(t *testing.T, want string) {
		t.Helper()
		dsn, ok := mysqlBaseDSNFromEnv()
		assert.True(t, ok)
		assert.Equal(t, want, dsn)
		rec, got, returned := callTestMySQLBaseDSN()
		assert.False(t, rec.skipped)
		assert.True(t, returned)
		assert.Equal(t, want, got)
	}

	t.Run("unset skips", func(t *testing.T) {
		t.Setenv("TEST_MYSQL_DSN", "")
		require.NoError(t, os.Unsetenv("TEST_MYSQL_DSN"))
		assertSkips(t)
	})
	t.Run("empty skips", func(t *testing.T) {
		t.Setenv("TEST_MYSQL_DSN", "")
		assertSkips(t)
	})
	t.Run("whitespace skips", func(t *testing.T) {
		t.Setenv("TEST_MYSQL_DSN", " \t\n ")
		assertSkips(t)
	})
	t.Run("set value is returned without connecting", func(t *testing.T) {
		t.Setenv("TEST_MYSQL_DSN", "nobody:x@tcp(127.0.0.1:1)/")
		start := time.Now()
		assertReturns(t, "nobody:x@tcp(127.0.0.1:1)/")
		assert.Less(t, time.Since(start), time.Second)
	})
	t.Run("missing trailing slash is appended", func(t *testing.T) {
		t.Setenv("TEST_MYSQL_DSN", "nobody:x@tcp(127.0.0.1:1)")
		assertReturns(t, "nobody:x@tcp(127.0.0.1:1)/")
	})
	t.Run("query string is kept without appending a slash", func(t *testing.T) {
		t.Setenv("TEST_MYSQL_DSN", "nobody:x@tcp(127.0.0.1:1)/?parseTime=true")
		assertReturns(t, "nobody:x@tcp(127.0.0.1:1)/?parseTime=true")
	})
}

func TestMigrationUpgrade_FromMainAutoMigratedSchema(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)

	serverDB, err := sql.Open("mysql", migrationDSN(baseDSN))
	require.NoError(t, err)
	defer serverDB.Close()

	testDBName := fmt.Sprintf("gocrm_upg_%d", time.Now().UnixNano()%10000000)
	_, err = serverDB.Exec("CREATE DATABASE " + testDBName)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupDB, err := sql.Open("mysql", migrationDSN(baseDSN))
		if err == nil {
			_, _ = cleanupDB.Exec("DROP DATABASE IF EXISTS " + testDBName)
			cleanupDB.Close()
		}
	})

	testDSN := requireMySQLTestDSN(t, baseDSN, testDBName)
	appDB, err := sql.Open("mysql", testDSN)
	require.NoError(t, err)
	defer appDB.Close()

	driver, err := mysql.WithInstance(appDB, &mysql.Config{})
	require.NoError(t, err)

	migrationsPath, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)

	m, err := migrate.NewWithDatabaseInstance("file://"+migrationsPath, "mysql", driver)
	require.NoError(t, err)
	defer m.Close()

	// 1. Migrate up to main's head version: 20260930120000
	err = m.Migrate(20260930120000)
	require.NoError(t, err)

	ver, dirty, err := m.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(20260930120000), ver)

	// 2. Simulate running main app server with AutoMigrate on main models
	// (on main, MigrateDatabase ran AutoMigrate on these models, creating columns like
	// users.failed_login_attempts, customers.assigned_to_id, leads.classification, etc.)
	gormDB, err := gorm.Open(gormmysql.Open(testDSN), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)

	err = gormDB.AutoMigrate(
		&models.User{},
		&models.Company{},
		&models.Lead{},
		&models.Customer{},
		&models.Deal{},
		&models.DealStageChange{},
		&models.Ticket{},
		&models.Label{},
		&models.Task{},
		&models.APIKey{},
		&models.Configuration{},
		&models.RefreshToken{},
		&models.PasswordResetToken{},
		&models.BulkOperation{},
		&models.BulkOperationItem{},
		&models.AEOProfile{},
		&models.AEOPrompt{},
		&models.AEORun{},
		&models.AEOAnswer{},
		&models.AEOCitation{},
		&models.Form{},
		&models.FormSubmission{},
		&models.FormConfirmationToken{},
	)
	require.NoError(t, err)

	// Verify that main's AutoMigrate created failed_login_attempts in users
	var count int
	err = appDB.QueryRow(
		"SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'users' AND COLUMN_NAME = 'failed_login_attempts'",
		testDBName,
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "users.failed_login_attempts must already exist from main AutoMigrate")

	// 3. Now upgrade with migrate.Up() — this MUST NOT fail with ERROR 1060 Duplicate column name
	err = m.Up()
	require.NoError(t, err, "migrating up from a database initialized by main must succeed idempotently")

	// 4. Verify version is now clean at the latest migration
	ver, dirty, err = m.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(latestMigrationVersion), ver)

	// 5. Verify models.MigrateDatabase() on top continues to succeed without error
	models.DB = gormDB
	err = models.MigrateDatabase()
	assert.NoError(t, err)
	assertCustomerExternalIDsBinary(t, appDB, testDBName)
	assertProductExternalIDsBinary(t, appDB, testDBName)
	assertFormTicketKeysBinary(t, appDB, testDBName)
	assertUserIdentitiesBinary(t, appDB, testDBName)

	// 6. Verify down migration also runs cleanly in reverse
	err = m.Down()
	assert.NoError(t, err)
}

func TestMigrationFresh_FullUpAndDown(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)

	serverDB, err := sql.Open("mysql", migrationDSN(baseDSN))
	require.NoError(t, err)
	defer serverDB.Close()

	testDBName := fmt.Sprintf("gocrm_fresh_test_%d", time.Now().UnixNano()%10000000)
	_, err = serverDB.Exec("CREATE DATABASE " + testDBName)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupDB, err := sql.Open("mysql", migrationDSN(baseDSN))
		if err == nil {
			_, _ = cleanupDB.Exec("DROP DATABASE IF EXISTS " + testDBName)
			cleanupDB.Close()
		}
	})

	testDSN := requireMySQLTestDSN(t, baseDSN, testDBName)
	appDB, err := sql.Open("mysql", testDSN)
	require.NoError(t, err)
	defer appDB.Close()

	driver, err := mysql.WithInstance(appDB, &mysql.Config{})
	require.NoError(t, err)

	migrationsPath, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)

	m, err := migrate.NewWithDatabaseInstance("file://"+migrationsPath, "mysql", driver)
	require.NoError(t, err)
	defer m.Close()

	// Full up on fresh database
	err = m.Up()
	require.NoError(t, err)

	ver, dirty, err := m.Version()
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, uint(latestMigrationVersion), ver)
	assertRetiredConfigurations(t, appDB, 0)
	assertCustomerExternalIDsBinary(t, appDB, testDBName)
	assertProductExternalIDsBinary(t, appDB, testDBName)
	assertFormTicketKeysBinary(t, appDB, testDBName)
	assertUserIdentitiesBinary(t, appDB, testDBName)

	// The retirement steps back to the two rows the 2025 seed wrote. It is
	// addressed by the version before it: newer migrations sort after it.
	require.NoError(t, m.Migrate(20261029140000))
	assertRetiredConfigurations(t, appDB, len(models.RetiredConfigurationKeys))
	require.NoError(t, m.Up())
	assertRetiredConfigurations(t, appDB, 0)

	// Full rollback to 0
	err = m.Down()
	require.NoError(t, err)

	_, _, err = m.Version()
	assert.ErrorIs(t, err, migrate.ErrNilVersion)
}

// writeMigrationFiles creates empty-bodied SQL files with the given names in dir.
func writeMigrationFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;\n"), 0644))
	}
}

// writeMigrationPairs creates the up and down file for each <version>_<name> base.
func writeMigrationPairs(t *testing.T, dir string, bases ...string) {
	t.Helper()
	for _, base := range bases {
		writeMigrationFiles(t, dir, base+".up.sql", base+".down.sql")
	}
}

// listDir returns the sorted file names in dir.
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func migrationProblems(t *testing.T, dir string) []string {
	t.Helper()
	_, problems, err := readMigrationVersions(dir)
	require.NoError(t, err)
	return problems
}

func TestCreateNewMigration(t *testing.T) {
	t.Run("a_takes_newest_plus_one", func(t *testing.T) {
		dir := t.TempDir()
		writeMigrationPairs(t, dir, "20250101000000_old", "20261129120000_new")
		before := listDir(t, dir)

		upPath, downPath, err := createNewMigration("add_x", dir)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "20261129120001_add_x.up.sql"), upPath)
		assert.Equal(t, filepath.Join(dir, "20261129120001_add_x.down.sql"), downPath)

		after := listDir(t, dir)
		expected := append(append([]string{}, before...), "20261129120001_add_x.down.sql", "20261129120001_add_x.up.sql")
		assert.ElementsMatch(t, expected, after)
		for _, name := range before {
			data, err := os.ReadFile(filepath.Join(dir, name))
			require.NoError(t, err)
			assert.Equal(t, "SELECT 1;\n", string(data), "%s must be unchanged", name)
		}
		assert.Empty(t, migrationProblems(t, dir))
	})

	t.Run("b_second_call_takes_the_next", func(t *testing.T) {
		dir := t.TempDir()
		writeMigrationPairs(t, dir, "20250101000000_old", "20261129120000_new")
		_, _, err := createNewMigration("add_x", dir)
		require.NoError(t, err)

		upPath, downPath, err := createNewMigration("add_y", dir)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "20261129120002_add_y.up.sql"), upPath)
		assert.Equal(t, filepath.Join(dir, "20261129120002_add_y.down.sql"), downPath)
		assert.Empty(t, migrationProblems(t, dir))
	})

	t.Run("c_newest_in_the_past_does_not_use_the_clock", func(t *testing.T) {
		dir := t.TempDir()
		writeMigrationPairs(t, dir, "20250101000000_old")

		upPath, _, err := createNewMigration("add_x", dir)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "20250101000001_add_x.up.sql"), upPath)
	})

	t.Run("d_empty_directory_uses_a_clock_version", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			dir  func(t *testing.T) string
		}{
			{"empty", func(t *testing.T) string { return t.TempDir() }},
			{"missing", func(t *testing.T) string { return filepath.Join(t.TempDir(), "migrations") }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dir := tc.dir(t)
				upPath, downPath, err := createNewMigration("add_x", dir)
				require.NoError(t, err)

				base := strings.TrimSuffix(filepath.Base(upPath), ".up.sql")
				assert.Regexp(t, `^[0-9]{14}_add_x$`, base)
				assert.Equal(t, filepath.Join(dir, base+".down.sql"), downPath)
				assert.Empty(t, migrationProblems(t, dir))
			})
		}
	})

	t.Run("e_invalid_directory_writes_nothing", func(t *testing.T) {
		dir := t.TempDir()
		writeMigrationFiles(t, dir, "20250101000000_old.up.sql")
		before := listDir(t, dir)

		_, _, err := createNewMigration("add_x", dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "version 20250101000000 has no .down.sql file")
		assert.Equal(t, before, listDir(t, dir))
	})

	t.Run("f_invalid_name_writes_nothing", func(t *testing.T) {
		dir := t.TempDir()
		writeMigrationPairs(t, dir, "20250101000000_old")
		before := listDir(t, dir)

		for _, name := range []string{"add-x", "add x", "../add_x", ""} {
			_, _, err := createNewMigration(name, dir)
			require.Error(t, err, "name %q", name)
		}
		assert.Equal(t, before, listDir(t, dir))
	})
}

func TestMigrationFiles_VersionsUniquePairedIncreasing(t *testing.T) {
	t.Run("repository", func(t *testing.T) {
		dir := filepath.Join("..", "..", "migrations")
		versions, problems, err := readMigrationVersions(dir)
		require.NoError(t, err)
		assert.Empty(t, problems)
		assert.NotEmpty(t, versions, "the repository must hold migrations")
	})

	t.Run("valid", func(t *testing.T) {
		dir := t.TempDir()
		writeMigrationPairs(t, dir, "20250101000000_old", "20261129120000_new")
		writeMigrationFiles(t, dir, "README.txt")
		require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0755))
		assert.Empty(t, migrationProblems(t, dir))
	})

	failures := []struct {
		name  string
		files []string
		want  string
	}{
		{
			name:  "duplicate_version",
			files: []string{"5_a.up.sql", "5_a.down.sql", "5_b.up.sql", "5_b.down.sql"},
			want:  "version 5 is used by more than one name",
		},
		{
			name:  "missing_down",
			files: []string{"5_a.up.sql", "6_b.up.sql", "6_b.down.sql"},
			want:  "version 5 has no .down.sql file",
		},
		{
			name:  "missing_up",
			files: []string{"5_a.down.sql", "6_b.up.sql", "6_b.down.sql"},
			want:  "version 5 has no .up.sql file",
		},
		{
			name:  "up_down_name_mismatch",
			files: []string{"5_a.up.sql", "5_b.down.sql"},
			want:  "version 5 is used by more than one name: a, b",
		},
		{
			name:  "not_increasing",
			files: []string{"9_a.up.sql", "9_a.down.sql", "10_b.up.sql", "10_b.down.sql"},
			want:  "version 9 is listed after version 10",
		},
		{
			name:  "malformed_name",
			files: []string{"add_x.up.sql", "5_a.up.sql", "5_a.down.sql"},
			want:  "add_x.up.sql: name does not match",
		},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMigrationFiles(t, dir, tc.files...)
			problems := migrationProblems(t, dir)
			require.NotEmpty(t, problems)
			assert.Contains(t, strings.Join(problems, "\n"), tc.want)
		})
	}
}
