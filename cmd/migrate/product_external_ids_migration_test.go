package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// #239: 20261112120000_add_product_external_ids creates product_external_ids
// only when it is missing, with source, scope and external_id in utf8mb4_bin,
// the columns, indexes and foreign key AutoMigrate plus the collation guard of
// models.MigrateDatabase produce; rollback drops that table and nothing else.

const (
	productExternalIDsVersion         = 20261112120000
	productExternalIDsPreviousVersion = customerExternalIDsVersion
	productExternalIDsName            = "20261112120000_add_product_external_ids"
	productExternalIDsTable           = "product_external_ids"
)

// TestProductExternalIDsMigrationFiles needs no database: the pair exists,
// declares the table as the model does, is the newest migration of the
// directory, and latestMigrationVersion names it.
func TestProductExternalIDsMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, productExternalIDsName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, productExternalIDsName+".down.sql"))
	require.NoError(t, err)
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	assert.Contains(t, upSQL, "create table if not exists product_external_ids (", "the table is created only when missing")
	for _, column := range []string{
		"id bigint unsigned not null auto_increment",
		"created_at datetime(3) null",
		"updated_at datetime(3) null",
		"product_id bigint unsigned not null",
		"source varchar(64) character set utf8mb4 collate utf8mb4_bin not null",
		"scope varchar(128) character set utf8mb4 collate utf8mb4_bin not null default ''",
		"external_id varchar(255) character set utf8mb4 collate utf8mb4_bin not null",
		"unique key idx_product_external_ids_identity (source, scope, external_id)",
		"key idx_product_external_ids_product_id (product_id)",
		"constraint fk_product_external_ids_product foreign key (product_id) references products (id)",
	} {
		assert.Containsf(t, upSQL, column, "up declares %s", column)
	}
	body := strings.SplitN(upSQL, "create table", 2)[1]
	assert.NotContains(t, body, "deleted_at", "mappings are hard-deleted")
	assert.Equal(t, 1, strings.Count(body, "unique key"), "the identity index is the only unique one")
	assert.NotContains(t, upSQL, "alter table", "no existing table is altered")
	assert.NotContains(t, body, "sku", "products.sku is not touched")
	assert.Contains(t, downSQL, "drop table if exists product_external_ids;")
	assert.Equal(t, 1, strings.Count(downSQL, "drop table"), "rollback drops one table")
	assert.Greater(t, productExternalIDsVersion, productExternalIDsPreviousVersion)
	// The newest-migration assertion moved to TestTicketMailDeliveriesMigrationFiles (#219).

	source, err := os.ReadFile("main_test.go")
	require.NoError(t, err)
	assert.Contains(t, string(source), `"`+productExternalIDsTable+`"`, "main_test.go's expected-table list names the table")
}

func productExternalIDCollations(t *testing.T, db *sql.DB, schema string) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT COLUMN_NAME, COALESCE(COLLATION_NAME, '') FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'product_external_ids' AND COLUMN_NAME IN ('source', 'scope', 'external_id')`, schema)
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

// assertProductExternalIDsBinary is used by the fresh and upgrade roots too.
func assertProductExternalIDsBinary(t *testing.T, db *sql.DB, schema string) {
	t.Helper()
	assert.Equal(t, binaryIdentityColumns, productExternalIDCollations(t, db, schema),
		"source, scope and external_id of product_external_ids compare exactly")
}

// insertMigrationProducts writes a live and a soft-deleted product sharing a
// SKU and returns their ids.
func insertMigrationProducts(t *testing.T, db *sql.DB, tag string) []int64 {
	t.Helper()
	var ids []int64
	for i, deleted := range []bool{false, true} {
		deletedAt := "NULL"
		if deleted {
			deletedAt = "NOW(3)"
		}
		res, err := db.Exec(fmt.Sprintf(`INSERT INTO products (created_at, updated_at, deleted_at, name, sku, description, unit_price_cents, currency, is_active)
			VALUES (NOW(3), NOW(3), %s, ?, 'MIG-SKU', 'kept', ?, 'EUR', ?)`, deletedAt), fmt.Sprintf("%s-%d", tag, i), 100*(i+1), !deleted)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		ids = append(ids, id)
	}
	return ids
}

// productRows renders every products row, soft-deleted ones included.
func productRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT id, name, COALESCE(sku, ''), COALESCE(description, ''), unit_price_cents, currency, is_active,
		COALESCE(DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s.%f'), ''), COALESCE(DATE_FORMAT(deleted_at, '%Y-%m-%d %H:%i:%s.%f'), '')
		FROM products ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, price int64
		var active bool
		var name, sku, description, currency, created, deleted string
		require.NoError(t, rows.Scan(&id, &name, &sku, &description, &price, &currency, &active, &created, &deleted))
		out = append(out, fmt.Sprintf("%d|%s|%s|%s|%d|%s|%t|%s|%s", id, name, sku, description, price, currency, active, created, deleted))
	}
	require.NoError(t, rows.Err())
	return out
}

// assertProductMappingsCaseSensitive inserts two triples that differ only in
// letter case (both stored) and the same triple twice (refused).
func assertProductMappingsCaseSensitive(t *testing.T, db *sql.DB, productID int64) {
	t.Helper()
	insert := "INSERT INTO product_external_ids (product_id, source, scope, external_id) VALUES (?, ?, ?, ?)"
	_, err := db.Exec(insert, productID, "Mig-ERP", "", "M1")
	require.NoError(t, err)
	_, err = db.Exec(insert, productID, "mig-erp", "", "m1")
	require.NoError(t, err, "a triple differing only in case is another identity")
	_, err = db.Exec(insert, productID, "Mig-ERP", "", "M1")
	require.Error(t, err, "the same triple twice is refused")
	_, err = db.Exec("DELETE FROM product_external_ids WHERE source IN ('Mig-ERP', 'mig-erp')")
	require.NoError(t, err)
}

func TestProductExternalIDsMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the table as MigrateDatabase (AutoMigrate plus the
	// collation guard) creates it on top of the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "prod_extid_ref")
	require.NoError(t, reference.m.Migrate(productExternalIDsPreviousVersion))
	assert.NotContains(t, listTables(t, reference.sql, reference.name), productExternalIDsTable)
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	models.DB = refDB
	require.NoError(t, models.MigrateDatabase())
	want := snapshotTable(t, reference, productExternalIDsTable)
	require.NotEmpty(t, want.Columns)
	assert.Contains(t, want.Indexes, "idx_product_external_ids_identity (source,scope,external_id) non_unique=0")
	assert.Contains(t, want.Indexes, "idx_product_external_ids_product_id (product_id) non_unique=1")
	assert.Equal(t, []string{"fk_product_external_ids_product product_id -> products.id"}, want.ForeignKeys)
	uniques := 0
	for _, index := range want.Indexes {
		if strings.HasSuffix(index, "non_unique=0") && !strings.HasPrefix(index, "PRIMARY ") {
			uniques++
		}
	}
	assert.Equal(t, 1, uniques, "the identity index is the only unique index")
	for _, column := range want.Columns {
		assert.False(t, strings.HasPrefix(column, "deleted_at "), "no deleted_at column")
	}
	assertProductExternalIDsBinary(t, reference.sql, reference.name)

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "prod_extid_fresh")
		require.NoError(t, tdb.m.Up())
		assert.Equal(t, want, snapshotTable(t, tdb, productExternalIDsTable), "a fresh SQL schema carries what MigrateDatabase creates")
		assertProductExternalIDsBinary(t, tdb.sql, tdb.name)
		ids := insertMigrationProducts(t, tdb.sql, "fresh")
		assertProductMappingsCaseSensitive(t, tdb.sql, ids[0])

		// Rollback of this migration alone, then reapply. The migrations that
		// sort after it (#219) are rolled back to it first.
		require.NoError(t, tdb.m.Migrate(productExternalIDsVersion))
		tablesBefore := listTables(t, tdb.sql, tdb.name)
		require.NoError(t, tdb.m.Steps(-1))
		assert.Equal(t, without(tablesBefore, []string{productExternalIDsTable}), listTables(t, tdb.sql, tdb.name))
		require.NoError(t, tdb.m.Up())
		assert.Equal(t, want, snapshotTable(t, tdb, productExternalIDsTable))

		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
		assert.NotContains(t, listTables(t, tdb.sql, tdb.name), productExternalIDsTable)
	})

	t.Run("MigrateDatabaseOnly", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "prod_extid_app")
		recorder := &ddlRecorder{Interface: gormlogger.Discard}
		appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: recorder})
		require.NoError(t, err)
		pool, err := appDB.DB()
		require.NoError(t, err)
		defer pool.Close()

		models.DB = appDB
		require.NoError(t, models.MigrateDatabase())
		assertProductExternalIDsBinary(t, tdb.sql, tdb.name)
		assert.NotEmpty(t, recorder.ddlOn(productExternalIDsTable), "the first start creates the table")
		ids := insertMigrationProducts(t, tdb.sql, "app")
		assertProductMappingsCaseSensitive(t, tdb.sql, ids[0])

		recorder.reset()
		require.NoError(t, models.MigrateDatabase())
		assert.Empty(t, recorder.ddlOn(productExternalIDsTable), "a second MigrateDatabase issues no DDL on the table")
		assertProductExternalIDsBinary(t, tdb.sql, tdb.name)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("WithProducts/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "prod_extid_mig")
			require.NoError(t, tdb.m.Migrate(productExternalIDsPreviousVersion))
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			// A schema already holding products.
			ids := insertMigrationProducts(t, tdb.sql, fmt.Sprintf("mig-%t", autoMigrateFirst))
			productsBefore := productRows(t, tdb.sql)
			require.Len(t, productsBefore, 2)

			if autoMigrateFirst {
				// AutoMigrate alone, without the guard, leaves the default collation.
				require.NoError(t, appDB.AutoMigrate(&models.ProductExternalID{}))
				_, err = tdb.sql.Exec("INSERT INTO product_external_ids (product_id, source, scope, external_id, created_at, updated_at) VALUES (?, 'kept', '', 'row-1', NOW(), NOW())", ids[0])
				require.NoError(t, err)
			}

			require.NoError(t, tdb.m.Migrate(productExternalIDsVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(productExternalIDsVersion), version)
			assert.Equal(t, want, snapshotTable(t, tdb, productExternalIDsTable), "the SQL migration and AutoMigrate must produce the same table")
			assert.Equal(t, productsBefore, productRows(t, tdb.sql), "the products are unchanged by the migration")

			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, want, snapshotTable(t, tdb, productExternalIDsTable), "MigrateDatabase must not change the table's shape")
			assertProductExternalIDsBinary(t, tdb.sql, tdb.name)
			assertProductMappingsCaseSensitive(t, tdb.sql, ids[0])
			_, err = tdb.sql.Exec("INSERT INTO product_external_ids (product_id, source, scope, external_id) VALUES (?, 'erp', '', 'after-upgrade')", ids[0])
			require.NoError(t, err, "a mapping of an existing product is insertable after the upgrade")
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM product_external_ids WHERE external_id = 'row-1'").Scan(&count))
				assert.Equal(t, 1, count, "a table AutoMigrate created first is kept with its rows")
			}
			require.ErrorIs(t, tdb.m.Migrate(productExternalIDsVersion), migrate.ErrNoChange)

			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(productExternalIDsPreviousVersion), version)
			assert.Equal(t, without(tablesBefore, []string{productExternalIDsTable}), listTables(t, tdb.sql, tdb.name),
				"rollback drops product_external_ids and nothing else")
			assert.Equal(t, productsBefore, productRows(t, tdb.sql), "the products are unchanged by the rollback")

			require.NoError(t, tdb.m.Migrate(productExternalIDsVersion))
			assert.Equal(t, want, snapshotTable(t, tdb, productExternalIDsTable))
			assertProductExternalIDsBinary(t, tdb.sql, tdb.name)
			assertProductMappingsCaseSensitive(t, tdb.sql, ids[0])
			assert.Equal(t, productsBefore, productRows(t, tdb.sql), "the products are unchanged by the reapply")
		})
	}
}
