package repository

import (
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

	require.NoError(t, db.Exec("INSERT INTO companies (name, domain, created_at, updated_at) VALUES ('Mixed', 'MixedCase.Example', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)").Error)
	require.NoError(t, db.Exec("INSERT INTO companies (name, domain, created_at, updated_at, deleted_at) VALUES ('Gone', 'Gone.Example', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)").Error)
	require.NoError(t, db.Exec("INSERT INTO companies (name, domain, created_at, updated_at) VALUES ('Plain', 'plain.example', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)").Error)
	require.NoError(t, db.Exec("INSERT INTO companies (name, created_at, updated_at) VALUES ('NoDomain', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)").Error)

	// Startup runs the migration on every boot; twice proves it is repeatable.
	require.NoError(t, models.MigrateDatabase())
	require.NoError(t, models.MigrateDatabase())

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
