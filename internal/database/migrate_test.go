package database

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// openSQLite opens a fresh SQLite file through the production Open, so the
// pool (one connection) and the DSN pragmas (foreign_keys on) are the ones the
// application runs with.
func openSQLite(t *testing.T, name string) *gorm.DB {
	t.Helper()

	db, err := Open(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), name),
	})
	require.NoError(t, err)
	t.Cleanup(func() { closeDB(t, db) })
	return db
}

func foreignKeysPragma(t *testing.T, db *gorm.DB) int {
	t.Helper()
	var enforced int
	require.NoError(t, db.Raw("PRAGMA foreign_keys").Scan(&enforced).Error)
	return enforced
}

// createParentChild builds the smallest schema with a foreign key: children
// point at parents.
func createParentChild(t *testing.T, tx *gorm.DB) {
	t.Helper()
	require.NoError(t, tx.Exec("CREATE TABLE parents (id integer primary key)").Error)
	require.NoError(t, tx.Exec(
		"CREATE TABLE children (id integer primary key, parent_id integer references parents(id))",
	).Error)
}

// TestMigrate_RefusesDanglingReferences pins the refusal: a migration that
// leaves a reference to a missing parent behind is reported as an error that
// names the tables involved, and enforcement is back on for the application.
func TestMigrate_RefusesDanglingReferences(t *testing.T) {
	db := openSQLite(t, "dangling.db")

	err := Migrate(db, func(tx *gorm.DB) error {
		createParentChild(t, tx)
		// Enforcement is off inside the callback, so the dangling row is
		// accepted here; the check afterwards has to catch it.
		return tx.Exec("INSERT INTO children (id, parent_id) VALUES (1, 999)").Error
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "foreign key check after migration failed")
	assert.Contains(t, err.Error(), "children -> parents")

	assert.Equal(t, 1, foreignKeysPragma(t, db), "enforcement must be restored after a refused migration")

	// The pragma reading 1 is not enough on its own: the next dangling insert
	// has to be rejected by the database.
	insertErr := db.Exec("INSERT INTO children (id, parent_id) VALUES (2, 998)").Error
	require.Error(t, insertErr, "a dangling insert outside Migrate must be rejected")
	assert.Contains(t, insertErr.Error(), "FOREIGN KEY constraint failed")
}

// TestMigrate_RestoresEnforcementWhenTheCallbackFails covers the other exit:
// the callback's own error comes back unchanged and enforcement is on again.
func TestMigrate_RestoresEnforcementWhenTheCallbackFails(t *testing.T) {
	db := openSQLite(t, "callback-fails.db")
	errMigrationBroke := errors.New("migration broke")

	err := Migrate(db, func(tx *gorm.DB) error {
		return errMigrationBroke
	})
	require.ErrorIs(t, err, errMigrationBroke)

	assert.Equal(t, 1, foreignKeysPragma(t, db), "enforcement must be restored after a failed callback")
}

// TestMigrate_EnforcementIsOffInsideTheCallback is the property the fix rests
// on: the callback, and any transaction it opens, run on the connection whose
// pragma was switched off. Open sets a one-connection pool, but the wrapper
// pins the connection itself, so this must hold regardless of the pool size.
func TestMigrate_EnforcementIsOffInsideTheCallback(t *testing.T) {
	db := openSQLite(t, "inside.db")
	require.Equal(t, 1, foreignKeysPragma(t, db), "the DSN switches enforcement on before Migrate runs")

	called := false
	err := Migrate(db, func(tx *gorm.DB) error {
		called = true
		assert.Equal(t, 0, foreignKeysPragma(t, tx), "enforcement must be off inside the callback")

		return tx.Transaction(func(inner *gorm.DB) error {
			assert.Equal(t, 0, foreignKeysPragma(t, inner), "enforcement must be off inside a nested transaction")
			return nil
		})
	})
	require.NoError(t, err)
	assert.True(t, called, "the callback must run")

	assert.Equal(t, 1, foreignKeysPragma(t, db), "enforcement must be back on after Migrate")
}

// renamedDialector answers to a different name and delegates everything else,
// so the dispatch in Migrate can be exercised without a MySQL server.
type renamedDialector struct {
	gorm.Dialector
	name string
}

func (d renamedDialector) Name() string { return d.name }

// TestMigrate_NonSQLiteCallsTheCallbackDirectly checks that any other driver
// gets the plain call: the very handle is passed through and no pragma is
// touched.
func TestMigrate_NonSQLiteCallsTheCallbackDirectly(t *testing.T) {
	db := openSQLite(t, "mysql-like.db")
	db.Dialector = renamedDialector{Dialector: db.Dialector, name: "mysql"}

	var seen *gorm.DB
	err := Migrate(db, func(tx *gorm.DB) error {
		seen = tx
		assert.Equal(t, 1, foreignKeysPragma(t, tx), "no pragma may be flipped for a non-SQLite driver")
		return nil
	})
	require.NoError(t, err)
	assert.Same(t, db, seen, "the callback must receive the handle it was given, not a pinned session")
}
