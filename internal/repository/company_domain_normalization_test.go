package repository

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// #109 follow-up: ExistsByDomain compares the stored domain with plain
// equality, which is only correct while every stored domain is lower case. The
// application always writes it lower-cased, and the MySQL SQL migration
// lower-cases rows written another way, but SQLite installs never run that
// migration: they get their schema from models.MigrateDatabase. These roots
// seed a mixed-case domain through raw SQL, as a hand edit or an old import
// would, run the startup migration and require the duplicate check to see it.

func TestCompanyDomainNormalizationSQLite(t *testing.T) {
	runCompanyDomainNormalization(t, openDisposableDB(t, "company_domain_normalization", false))
}

func TestCompanyDomainNormalizationMySQL(t *testing.T) {
	runCompanyDomainNormalization(t, openDisposableDB(t, "company_domain_normalization", true))
}

func runCompanyDomainNormalization(t *testing.T, db *gorm.DB) {
	previous := models.DB
	t.Cleanup(func() { models.DB = previous })
	models.DB = db

	// Idempotent on an empty table.
	require.NoError(t, models.MigrateDatabase())

	// Every statement that writes companies is recorded, so the test can
	// prove which rows the normalisation touches. An UPDATE on MySQL/MariaDB
	// row-locks every record it scans under REPEATABLE READ, so a normalised
	// table must see no UPDATE at all on startup.
	type write struct {
		sql  string
		vars []interface{}
	}
	var writes []write
	record := func(tx *gorm.DB) {
		statement := tx.Statement.SQL.String()
		if strings.Contains(strings.ToUpper(statement), "UPDATE") && strings.Contains(statement, "companies") {
			writes = append(writes, write{statement, append([]interface{}(nil), tx.Statement.Vars...)})
		}
	}
	require.NoError(t, db.Callback().Raw().After("gorm:raw").Register("test:record_company_writes_raw", record))
	require.NoError(t, db.Callback().Update().After("gorm:update").Register("test:record_company_writes_update", record))

	// A normalised table: a full read, no write.
	require.NoError(t, db.Exec("INSERT INTO companies (name, domain, created_at, updated_at) VALUES ('Clean', 'clean.example', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)").Error)
	writes = nil
	require.NoError(t, models.MigrateDatabase())
	assert.Empty(t, writes, "a normalised table must not be updated (and so not row-locked) on startup")

	require.NoError(t, db.Exec("INSERT INTO companies (name, domain, created_at, updated_at) VALUES ('Mixed', 'MixedCase.Example', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)").Error)
	require.NoError(t, db.Exec("INSERT INTO companies (name, domain, created_at, updated_at, deleted_at) VALUES ('Gone', 'Gone.Example', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)").Error)
	require.NoError(t, db.Exec("INSERT INTO companies (name, domain, created_at, updated_at) VALUES ('Plain', 'plain.example', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)").Error)
	require.NoError(t, db.Exec("INSERT INTO companies (name, created_at, updated_at) VALUES ('NoDomain', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)").Error)

	var mixedIDs []uint
	require.NoError(t, db.Raw("SELECT id FROM companies WHERE name IN ('Mixed', 'Gone') ORDER BY id").Scan(&mixedIDs).Error)
	require.Len(t, mixedIDs, 2)

	// Startup runs the migration on every boot; twice proves it is repeatable.
	writes = nil
	require.NoError(t, models.MigrateDatabase())
	require.Len(t, writes, 1, "one bounded update for the two mixed-case rows")
	var updated []uint
	for _, v := range writes[0].vars {
		switch ids := v.(type) {
		case []uint:
			updated = append(updated, ids...)
		case uint:
			updated = append(updated, ids)
		}
	}
	assert.ElementsMatch(t, mixedIDs, updated, "only the mixed-case rows are written, by primary key")
	writes = nil
	require.NoError(t, models.MigrateDatabase())
	assert.Empty(t, writes, "the second start finds nothing to normalise")

	stored := map[string]*string{}
	rows, err := db.Raw("SELECT name, domain FROM companies").Rows()
	require.NoError(t, err)
	for rows.Next() {
		var name string
		var domain *string
		require.NoError(t, rows.Scan(&name, &domain))
		stored[name] = domain
	}
	require.NoError(t, rows.Close())
	require.NotNil(t, stored["Mixed"])
	assert.Equal(t, "mixedcase.example", *stored["Mixed"])
	require.NotNil(t, stored["Gone"])
	assert.Equal(t, "gone.example", *stored["Gone"], "soft-deleted rows are normalised too")
	require.NotNil(t, stored["Plain"])
	assert.Equal(t, "plain.example", *stored["Plain"])
	assert.Nil(t, stored["NoDomain"], "a NULL domain stays NULL")

	repo := NewCompanyRepository(db)
	exists, err := repo.ExistsByDomain("mixedcase.example", 0)
	require.NoError(t, err)
	assert.True(t, exists, "an existing mixed-case live domain must still block its lower-case twin")
	exists, err = repo.ExistsByDomain("MIXEDCASE.EXAMPLE", 0)
	require.NoError(t, err)
	assert.True(t, exists)
	exists, err = repo.ExistsByDomain("gone.example", 0)
	require.NoError(t, err)
	assert.False(t, exists, "a soft-deleted company does not reserve its domain")
}

// TestCompanyDomainNormalizationChunksSQLite: more mixed-case rows than one
// chunk are written in bounded primary-key batches, every one of them.
func TestCompanyDomainNormalizationChunksSQLite(t *testing.T) {
	db := openDisposableDB(t, "company_domain_normalization_chunks", false)
	previous := models.DB
	t.Cleanup(func() { models.DB = previous })
	models.DB = db
	require.NoError(t, models.MigrateDatabase())
	for i := 0; i < 1001; i++ {
		require.NoError(t, db.Exec("INSERT INTO companies (name, domain, created_at, updated_at) VALUES ('Bulk', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)", fmt.Sprintf("Bulk%04d.Example", i)).Error)
	}
	updates := 0
	require.NoError(t, db.Callback().Raw().After("gorm:raw").Register("test:count_company_updates", func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.SQL.String(), "UPDATE companies") {
			updates++
		}
	}))
	require.NoError(t, models.MigrateDatabase())
	assert.Equal(t, 3, updates, "1001 rows in chunks of 500")
	var remaining int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM companies WHERE domain <> LOWER(domain)").Scan(&remaining).Error)
	assert.Zero(t, remaining)
}
