package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// #212: 20261111120000_add_customer_external_ids creates customer_external_ids
// only when it is missing, with source, scope and external_id in utf8mb4_bin,
// the columns, indexes and foreign key AutoMigrate plus the collation guard of
// models.MigrateDatabase produce; rollback drops that table and nothing else.

const (
	customerExternalIDsVersion         = 20261111120000
	customerExternalIDsPreviousVersion = 20261110120000
	customerExternalIDsName            = "20261111120000_add_customer_external_ids"
	customerExternalIDsTable           = "customer_external_ids"
)

// TestCustomerExternalIDsMigrationFiles needs no database: the pair exists,
// declares the table as the model does, is the newest migration of the
// directory, and latestMigrationVersion names it.
func TestCustomerExternalIDsMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, customerExternalIDsName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, customerExternalIDsName+".down.sql"))
	require.NoError(t, err)
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	assert.Contains(t, upSQL, "create table if not exists customer_external_ids (", "the table is created only when missing")
	for _, column := range []string{
		"id bigint unsigned not null auto_increment",
		"created_at datetime(3) null",
		"updated_at datetime(3) null",
		"customer_id bigint unsigned not null",
		"source varchar(64) character set utf8mb4 collate utf8mb4_bin not null",
		"scope varchar(128) character set utf8mb4 collate utf8mb4_bin not null default ''",
		"external_id varchar(255) character set utf8mb4 collate utf8mb4_bin not null",
		"unique key idx_customer_external_ids_identity (source, scope, external_id)",
		"key idx_customer_external_ids_customer_id (customer_id)",
		"constraint fk_customer_external_ids_customer foreign key (customer_id) references customers (id)",
	} {
		assert.Containsf(t, upSQL, column, "up declares %s", column)
	}
	body := strings.SplitN(upSQL, "create table", 2)[1]
	assert.NotContains(t, body, "deleted_at", "mappings are hard-deleted")
	assert.Equal(t, 1, strings.Count(body, "unique key"), "the identity index is the only unique one")
	assert.NotContains(t, upSQL, "alter table", "no existing table is altered")
	assert.Contains(t, downSQL, "drop table if exists customer_external_ids;")
	assert.Equal(t, 1, strings.Count(downSQL, "drop table"), "rollback drops one table")

	// The newest migration of the directory, and the version the fresh and
	// upgrade roots expect (moved here from TestBulkOperationStatusIndexMigrationFiles).
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	newest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	assert.Equal(t, customerExternalIDsName+".up.sql", newest, "the newest migration")
	version, err := strconv.Atoi(strings.SplitN(newest, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, customerExternalIDsVersion, version)
	assert.Equal(t, customerExternalIDsVersion, latestMigrationVersion)
	assert.Greater(t, customerExternalIDsVersion, customerExternalIDsPreviousVersion)

	source, err := os.ReadFile("main_test.go")
	require.NoError(t, err)
	assert.Contains(t, string(source), `"`+customerExternalIDsTable+`"`, "main_test.go's expected-table list names the table")
}

// customerExternalIDCollations returns the collation of the three identity
// columns.
func customerExternalIDCollations(t *testing.T, db *sql.DB, schema string) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT COLUMN_NAME, COALESCE(COLLATION_NAME, '') FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'customer_external_ids' AND COLUMN_NAME IN ('source', 'scope', 'external_id')`, schema)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, collation string
		require.NoError(t, rows.Scan(&name, &collation))
		out[name] = collation
	}
	require.NoError(t, rows.Err())
	return out
}

var binaryIdentityColumns = map[string]string{"source": "utf8mb4_bin", "scope": "utf8mb4_bin", "external_id": "utf8mb4_bin"}

// assertCustomerExternalIDsBinary is used by the fresh and upgrade roots too.
func assertCustomerExternalIDsBinary(t *testing.T, db *sql.DB, schema string) {
	t.Helper()
	assert.Equal(t, binaryIdentityColumns, customerExternalIDCollations(t, db, schema),
		"source, scope and external_id compare exactly")
}

// ddlRecorder keeps every statement GORM renders on the handle.
type ddlRecorder struct {
	gormlogger.Interface
	mu         sync.Mutex
	statements []string
}

func (r *ddlRecorder) LogMode(gormlogger.LogLevel) gormlogger.Interface { return r }

func (r *ddlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	statement, _ := fc()
	r.mu.Lock()
	r.statements = append(r.statements, statement)
	r.mu.Unlock()
}

func (r *ddlRecorder) reset() {
	r.mu.Lock()
	r.statements = nil
	r.mu.Unlock()
}

// ddlOn returns the recorded DDL statements that name the table.
func (r *ddlRecorder) ddlOn(table string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, statement := range r.statements {
		lower := strings.ToLower(strings.TrimSpace(statement))
		if !strings.Contains(lower, table) {
			continue
		}
		for _, prefix := range []string{"alter ", "create ", "drop ", "rename "} {
			if strings.HasPrefix(lower, prefix) {
				out = append(out, statement)
				break
			}
		}
	}
	return out
}

func insertMappingCustomer(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()
	res, err := db.Exec("INSERT INTO customers (first_name, last_name, email) VALUES ('M', 'C', ?)", email)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// assertMappingsCaseSensitive inserts two triples that differ only in letter
// case (both stored) and the same triple twice (refused).
func assertMappingsCaseSensitive(t *testing.T, db *sql.DB, customerID int64) {
	t.Helper()
	insert := "INSERT INTO customer_external_ids (customer_id, source, scope, external_id) VALUES (?, ?, ?, ?)"
	_, err := db.Exec(insert, customerID, "Mig-Shop", "", "X1")
	require.NoError(t, err)
	_, err = db.Exec(insert, customerID, "mig-shop", "", "x1")
	require.NoError(t, err, "a triple differing only in case is another identity")
	_, err = db.Exec(insert, customerID, "Mig-Shop", "", "X1")
	require.Error(t, err, "the same triple twice is refused")
	_, err = db.Exec("DELETE FROM customer_external_ids WHERE source IN ('Mig-Shop', 'mig-shop')")
	require.NoError(t, err)
}

func TestCustomerExternalIDsMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the table as the application's MigrateDatabase
	// (AutoMigrate plus the collation guard) creates it on top of the previous
	// SQL head.
	reference := openMigrationTestDB(t, baseDSN, "cust_extid_ref")
	require.NoError(t, reference.m.Migrate(customerExternalIDsPreviousVersion))
	assert.NotContains(t, listTables(t, reference.sql, reference.name), customerExternalIDsTable)
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	models.DB = refDB
	require.NoError(t, models.MigrateDatabase())
	want := snapshotTable(t, reference, customerExternalIDsTable)
	require.NotEmpty(t, want.Columns)
	assert.Contains(t, want.Indexes, "idx_customer_external_ids_identity (source,scope,external_id) non_unique=0")
	assert.Contains(t, want.Indexes, "idx_customer_external_ids_customer_id (customer_id) non_unique=1")
	assert.Equal(t, []string{"fk_customer_external_ids_customer customer_id -> customers.id"}, want.ForeignKeys)
	for _, column := range want.Columns {
		assert.False(t, strings.HasPrefix(column, "deleted_at "), "no deleted_at column")
	}
	assertCustomerExternalIDsBinary(t, reference.sql, reference.name)

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "cust_extid_fresh")
		require.NoError(t, tdb.m.Up())
		assert.Equal(t, want, snapshotTable(t, tdb, customerExternalIDsTable), "a fresh SQL schema carries what MigrateDatabase creates")
		assertCustomerExternalIDsBinary(t, tdb.sql, tdb.name)
		assertMappingsCaseSensitive(t, tdb.sql, insertMappingCustomer(t, tdb.sql, "fresh-mapping@example.com"))
		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
		assert.NotContains(t, listTables(t, tdb.sql, tdb.name), customerExternalIDsTable)
	})

	t.Run("MigrateDatabaseOnly", func(t *testing.T) {
		// A MySQL database that only ever ran the application: the guard
		// converts the AutoMigrated columns, and a second start issues no DDL
		// on the table.
		tdb := openMigrationTestDB(t, baseDSN, "cust_extid_app")
		recorder := &ddlRecorder{Interface: gormlogger.Discard}
		appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: recorder})
		require.NoError(t, err)
		pool, err := appDB.DB()
		require.NoError(t, err)
		defer pool.Close()

		models.DB = appDB
		require.NoError(t, models.MigrateDatabase())
		assertCustomerExternalIDsBinary(t, tdb.sql, tdb.name)
		assert.NotEmpty(t, recorder.ddlOn(customerExternalIDsTable), "the first start creates the table")
		assertMappingsCaseSensitive(t, tdb.sql, insertMappingCustomer(t, tdb.sql, "app-mapping@example.com"))

		recorder.reset()
		require.NoError(t, models.MigrateDatabase())
		assert.Empty(t, recorder.ddlOn(customerExternalIDsTable), "a second MigrateDatabase issues no DDL on the table")
		assertCustomerExternalIDsBinary(t, tdb.sql, tdb.name)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "cust_extid_mig")
			require.NoError(t, tdb.m.Migrate(customerExternalIDsPreviousVersion))
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			customerID := insertMappingCustomer(t, tdb.sql, fmt.Sprintf("mig-%t@example.com", autoMigrateFirst))
			if autoMigrateFirst {
				// The application's AutoMigrate alone, without the guard,
				// leaves the database's default collation.
				require.NoError(t, appDB.AutoMigrate(&models.CustomerExternalID{}))
				_, err = tdb.sql.Exec("INSERT INTO customer_external_ids (customer_id, source, scope, external_id, created_at, updated_at) VALUES (?, 'kept', '', 'row-1', NOW(), NOW())", customerID)
				require.NoError(t, err)
			}

			require.NoError(t, tdb.m.Migrate(customerExternalIDsVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(customerExternalIDsVersion), version)
			assert.Equal(t, want, snapshotTable(t, tdb, customerExternalIDsTable), "the SQL migration and AutoMigrate must produce the same table")

			// The application's startup on top converts what AutoMigrate
			// created, leaves the migrated table alone, and the migration is
			// not applied twice.
			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			got := snapshotTable(t, tdb, customerExternalIDsTable)
			assert.Equal(t, want, got, "MigrateDatabase must not change the table's shape")
			assertCustomerExternalIDsBinary(t, tdb.sql, tdb.name)
			assertMappingsCaseSensitive(t, tdb.sql, customerID)
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM customer_external_ids WHERE external_id = 'row-1'").Scan(&count))
				assert.Equal(t, 1, count, "a table AutoMigrate created first is kept with its rows")
			}
			require.ErrorIs(t, tdb.m.Migrate(customerExternalIDsVersion), migrate.ErrNoChange)

			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(customerExternalIDsPreviousVersion), version)
			assert.Equal(t, without(tablesBefore, []string{customerExternalIDsTable}), listTables(t, tdb.sql, tdb.name),
				"rollback drops customer_external_ids and nothing else")

			require.NoError(t, tdb.m.Migrate(customerExternalIDsVersion))
			assert.Equal(t, want, snapshotTable(t, tdb, customerExternalIDsTable))
			assertCustomerExternalIDsBinary(t, tdb.sql, tdb.name)
		})
	}
}
