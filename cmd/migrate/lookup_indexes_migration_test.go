package main

import (
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

// #109: 20261029140000_add_lead_company_lookup_indexes adds four NON-unique
// lookup indexes (leads.email, leads.classification, leads.external_id,
// companies.domain) under the names AutoMigrate gives them, and lower-cases
// any stored company domain so the plain-equality domain lookup cannot miss a
// row.

const (
	lookupIndexesVersion         = 20261029140000
	lookupIndexesPreviousVersion = 20261028120000
	lookupIndexesName            = "20261029140000_add_lead_company_lookup_indexes"
)

var lookupIndexes = map[string][]string{
	"leads": {
		"idx_leads_classification (classification) non_unique=1",
		"idx_leads_email (email) non_unique=1",
		"idx_leads_external_id (external_id) non_unique=1",
	},
	"companies": {
		"idx_companies_domain (domain) non_unique=1",
	},
}

// TestLookupIndexesMigrationFiles needs no database: the pair exists, names
// every index in both directions and declares none of them unique.
func TestLookupIndexesMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, lookupIndexesName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, lookupIndexesName+".down.sql"))
	require.NoError(t, err)
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))
	for table, indexes := range lookupIndexes {
		for _, index := range indexes {
			name := strings.Fields(index)[0]
			column := strings.Trim(strings.Fields(index)[1], "()")
			assert.Contains(t, upSQL, fmt.Sprintf("'create index %s on %s (%s)'", name, table, column))
			assert.Contains(t, downSQL, fmt.Sprintf("'drop index %s on %s'", name, table))
		}
	}
	for _, unique := range []string{"create unique", "unique index", "unique key", "add unique"} {
		assert.NotContains(t, upSQL, unique, "lead emails repeat and domains are reused after a soft delete")
	}
}

// lookupIndexSnapshot returns the index lines of leads and companies.
func lookupIndexSnapshot(t *testing.T, tdb *migrationTestDB) map[string][]string {
	t.Helper()
	return map[string][]string{
		"leads":     snapshotTable(t, tdb, "leads").Indexes,
		"companies": snapshotTable(t, tdb, "companies").Indexes,
	}
}

func assertLookupIndexes(t *testing.T, snapshot map[string][]string, present bool) {
	t.Helper()
	for table, indexes := range lookupIndexes {
		for _, index := range indexes {
			if present {
				assert.Containsf(t, snapshot[table], index, "%s must carry %s", table, index)
			} else {
				assert.NotContainsf(t, snapshot[table], index, "%s must not carry %s", table, index)
			}
		}
	}
}

// seedLookupRows writes the populated shape the upgrade must survive: two
// live leads sharing an email, a company domain written in mixed case by
// hand, and the same domain on a soft-deleted and a live company.
func seedLookupRows(t *testing.T, tdb *migrationTestDB) {
	t.Helper()
	statements := []string{
		"INSERT INTO users (email, password, first_name, last_name, role, is_active) VALUES ('owner@example.com', 'x', 'O', 'W', 'sales', 1)",
		"INSERT INTO leads (first_name, last_name, email, status, owner_id, classification, external_id, created_at) VALUES ('A', 'One', 'twice@example.com', 'new', 1, 'spam', 'ext-1', '2026-01-01 00:00:00')",
		"INSERT INTO leads (first_name, last_name, email, status, owner_id, classification, external_id, created_at) VALUES ('A', 'Two', 'twice@example.com', 'new', 1, 'lead', 'ext-2', '2026-01-02 00:00:00')",
		"INSERT INTO companies (name, domain) VALUES ('Mixed', 'MixedCase.Example')",
		"INSERT INTO companies (name, domain, deleted_at) VALUES ('Gone', 'reused.example', '2026-01-03 00:00:00')",
		"INSERT INTO companies (name, domain) VALUES ('Back', 'reused.example')",
	}
	for _, statement := range statements {
		_, err := tdb.sql.Exec(statement)
		require.NoError(t, err, statement)
	}
}

// withoutLookupIndexes is a snapshot minus the four indexes this migration owns.
func withoutLookupIndexes(snapshot map[string][]string) map[string][]string {
	out := map[string][]string{}
	for table, indexes := range snapshot {
		owned := map[string]bool{}
		for _, index := range lookupIndexes[table] {
			owned[index] = true
		}
		kept := []string{}
		for _, index := range indexes {
			if !owned[index] {
				kept = append(kept, index)
			}
		}
		out[table] = kept
	}
	return out
}

func lookupModels() []interface{} {
	return []interface{}{&models.User{}, &models.Company{}, &models.Lead{}}
}

func TestLookupIndexesMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: the two tables as AutoMigrate leaves them on top
	// of the previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "lookup_ref")
	require.NoError(t, reference.m.Migrate(lookupIndexesPreviousVersion))
	before := lookupIndexSnapshot(t, reference)
	assertLookupIndexes(t, before, false)
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	require.NoError(t, refDB.AutoMigrate(lookupModels()...))
	want := lookupIndexSnapshot(t, reference)
	assertLookupIndexes(t, want, true)

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "lookup_fresh")
		require.NoError(t, tdb.m.Up())
		fresh := lookupIndexSnapshot(t, tdb)
		assert.Equal(t, want, fresh, "a fresh SQL schema carries the indexes AutoMigrate creates")
		require.NoError(t, tdb.m.Down())
		_, _, err := tdb.m.Version()
		assert.ErrorIs(t, err, migrate.ErrNilVersion)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "lookup_mig")
			require.NoError(t, tdb.m.Migrate(lookupIndexesPreviousVersion))
			seedLookupRows(t, tdb)
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(lookupModels()...))
				assertLookupIndexes(t, lookupIndexSnapshot(t, tdb), true)
			}

			require.NoError(t, tdb.m.Migrate(lookupIndexesVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(lookupIndexesVersion), version)

			got := lookupIndexSnapshot(t, tdb)
			assert.Equal(t, want, got, "the SQL migration and AutoMigrate must produce the same indexes")

			var leads, companies int
			require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM leads").Scan(&leads))
			require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM companies").Scan(&companies))
			assert.Equal(t, 2, leads)
			assert.Equal(t, 3, companies)
			var domain string
			require.NoError(t, tdb.sql.QueryRow("SELECT domain FROM companies WHERE name = 'Mixed'").Scan(&domain))
			assert.Equal(t, "mixedcase.example", domain, "the upgrade lower-cases a hand-written domain")

			// Still non-unique after the upgrade.
			_, err = tdb.sql.Exec("INSERT INTO leads (first_name, last_name, email, status, owner_id) VALUES ('A', 'Three', 'twice@example.com', 'new', 1)")
			assert.NoError(t, err, "lead emails may repeat")
			_, err = tdb.sql.Exec("INSERT INTO companies (name, domain) VALUES ('Again', 'reused.example')")
			assert.NoError(t, err, "a domain may repeat at the database level; liveness is the service's rule")

			// The application's own startup on top changes nothing and the
			// migration is not applied twice.
			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, got, lookupIndexSnapshot(t, tdb), "MigrateDatabase must not alter the migrated indexes")
			require.ErrorIs(t, tdb.m.Migrate(lookupIndexesVersion), migrate.ErrNoChange)

			// Rollback removes exactly the four indexes and keeps the rows.
			tablesBefore := listTables(t, tdb.sql, tdb.name)
			require.NoError(t, tdb.m.Steps(-1))
			version, dirty, err = tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(lookupIndexesPreviousVersion), version)
			assert.Equal(t, withoutLookupIndexes(got), lookupIndexSnapshot(t, tdb), "rollback removes the four indexes and nothing else")
			assertLookupIndexes(t, lookupIndexSnapshot(t, tdb), false)
			assert.Equal(t, tablesBefore, listTables(t, tdb.sql, tdb.name), "rollback drops no table")
			require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM leads").Scan(&leads))
			assert.Equal(t, 3, leads)

			// Reapply.
			require.NoError(t, tdb.m.Migrate(lookupIndexesVersion))
			assert.Equal(t, want, lookupIndexSnapshot(t, tdb))
		})
	}
}
