package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	_ "github.com/go-sql-driver/mysql"
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
		"aeo_answers", "aeo_citations", "forms", "form_submissions", "form_confirmation_tokens",
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

func resolveTestMySQLBaseDSN() string {
	candidates := []string{
		os.Getenv("TEST_MYSQL_DSN"),
	}
	if cfg, err := config.Load(); err == nil && cfg.Database.Driver == config.DriverMySQL {
		candidates = append(candidates, fmt.Sprintf("%s:%s@tcp(%s:%d)/",
			cfg.Database.User, cfg.Database.Password, cfg.Database.Host, cfg.Database.Port))
	}
	user := os.Getenv("USER")
	if user != "" {
		candidates = append(candidates,
			fmt.Sprintf("%s@unix(/tmp/mysql.sock)/", user),
			fmt.Sprintf("%s:@unix(/tmp/mysql.sock)/", user),
			fmt.Sprintf("%s:@tcp(127.0.0.1:3306)/", user),
			fmt.Sprintf("%s@tcp(127.0.0.1:3306)/", user),
		)
	}
	candidates = append(candidates,
		"root@unix(/tmp/mysql.sock)/",
		"root:@unix(/tmp/mysql.sock)/",
		"root:@tcp(127.0.0.1:3306)/",
		"root:root@tcp(127.0.0.1:3306)/",
		"root:e2e-root-password@tcp(127.0.0.1:3306)/",
		"gophercrm:e2e-password@tcp(127.0.0.1:3306)/",
	)

	for _, dsn := range candidates {
		if dsn == "" {
			continue
		}
		cleanDSN := dsn
		if !strings.HasSuffix(cleanDSN, "/") {
			cleanDSN += "/"
		}
		connectDSN := migrationDSN(cleanDSN)
		db, err := sql.Open("mysql", connectDSN)
		if err != nil {
			continue
		}
		if err := db.Ping(); err == nil {
			probeDB := fmt.Sprintf("gocrm_probe_%d", time.Now().UnixNano()%1000000)
			if _, execErr := db.Exec("CREATE DATABASE " + probeDB); execErr == nil {
				_, _ = db.Exec("DROP DATABASE " + probeDB)
				db.Close()
				return cleanDSN
			}
		}
		db.Close()
	}
	return ""
}

func TestMigrationUpgrade_FromMainAutoMigratedSchema(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("skipping database upgrade test: no reachable MySQL/MariaDB server")
	}

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

	testDSN := baseDSN + testDBName + "?multiStatements=true&parseTime=true"
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
	assert.Equal(t, uint(20261019120000), ver)

	// 5. Verify models.MigrateDatabase() on top continues to succeed without error
	models.DB = gormDB
	err = models.MigrateDatabase()
	assert.NoError(t, err)

	// 6. Verify down migration also runs cleanly in reverse
	err = m.Down()
	assert.NoError(t, err)
}

func TestMigrationFresh_FullUpAndDown(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("skipping database fresh migration test: no reachable MySQL/MariaDB server")
	}

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

	testDSN := baseDSN + testDBName + "?multiStatements=true&parseTime=true"
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
	assert.Equal(t, uint(20261019120000), ver)

	// Full rollback to 0
	err = m.Down()
	require.NoError(t, err)

	_, _, err = m.Version()
	assert.ErrorIs(t, err, migrate.ErrNilVersion)
}



