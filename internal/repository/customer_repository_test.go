package repository

import (
	"fmt"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupCustomerExportDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{
		Logger: logger.Discard,
	})
	require.NoError(t, err)

	// A private schema per test: the shared-cache DSN above is reused, so the
	// tables are dropped and recreated rather than accumulating rows between
	// tests.
	require.NoError(t, db.Migrator().DropTable(&models.Customer{}, &models.User{}))
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Customer{}))

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// The export exists to produce the WHOLE book of customers, so the one thing it
// must never do is silently stop at a page boundary. The row count here is
// comfortably past the 20/100 limits the list endpoints work in and past the
// repository's own batch size, so a batching bug that dropped or repeated a
// batch shows up as a wrong count rather than as a plausible-looking file.
func TestListAllForExport_ReturnsEveryRowRegardlessOfPageSize(t *testing.T) {
	db := setupCustomerExportDB(t)
	repo := NewCustomerRepository(db)

	const total = 1250
	for i := 0; i < total; i++ {
		require.NoError(t, db.Create(&models.Customer{
			FirstName: fmt.Sprintf("First%04d", i),
			LastName:  "Exportable",
			Email:     fmt.Sprintf("export%04d@example.com", i),
		}).Error)
	}

	customers, err := repo.ListAllForExport("", "", "")
	require.NoError(t, err)
	assert.Len(t, customers, total)

	// Every row exactly once: a batching bug that re-read a batch would still
	// produce the right length if it also dropped one.
	seen := map[uint]int{}
	for _, c := range customers {
		seen[c.ID]++
	}
	assert.Len(t, seen, total, "every customer must appear exactly once")
	for id, count := range seen {
		require.Equal(t, 1, count, "customer %d exported %d times", id, count)
	}
}

// An erased customer has already exercised its right to be forgotten. It must
// not reappear in an export.
func TestListAllForExport_ExcludesSoftDeletedRows(t *testing.T) {
	db := setupCustomerExportDB(t)
	repo := NewCustomerRepository(db)

	live := &models.Customer{FirstName: "Live", LastName: "Customer", Email: "live@example.com"}
	erased := &models.Customer{FirstName: "Erased", LastName: "Customer", Email: "erased@example.com"}
	require.NoError(t, db.Create(live).Error)
	require.NoError(t, db.Create(erased).Error)
	require.NoError(t, repo.Delete(erased.ID))

	customers, err := repo.ListAllForExport("", "", "")
	require.NoError(t, err)
	require.Len(t, customers, 1)
	assert.Equal(t, live.ID, customers[0].ID)
}

func TestListAllForExport_HonoursSearch(t *testing.T) {
	db := setupCustomerExportDB(t)
	repo := NewCustomerRepository(db)

	require.NoError(t, db.Create(&models.Customer{FirstName: "John", LastName: "Doe", Email: "john@acme.test", Company: "Acme Corp"}).Error)
	require.NoError(t, db.Create(&models.Customer{FirstName: "Jane", LastName: "Smith", Email: "jane@globex.test", Company: "Globex"}).Error)

	customers, err := repo.ListAllForExport("acme", "", "")
	require.NoError(t, err)
	require.Len(t, customers, 1)
	assert.Equal(t, "john@acme.test", customers[0].Email)
}

func TestListAllForExport_HonoursSortOrder(t *testing.T) {
	db := setupCustomerExportDB(t)
	repo := NewCustomerRepository(db)

	for _, email := range []string{"b@example.com", "a@example.com", "c@example.com"} {
		require.NoError(t, db.Create(&models.Customer{FirstName: "X", LastName: "Y", Email: email}).Error)
	}

	customers, err := repo.ListAllForExport("", "email", "desc")
	require.NoError(t, err)
	require.Len(t, customers, 3)
	assert.Equal(t, []string{"c@example.com", "b@example.com", "a@example.com"},
		[]string{customers[0].Email, customers[1].Email, customers[2].Email})
}

// The sort column is the SQL-injection surface. An unvalidated column must be
// refused rather than interpolated.
func TestListAllForExport_RejectsUnknownSortColumn(t *testing.T) {
	db := setupCustomerExportDB(t)
	repo := NewCustomerRepository(db)

	require.NoError(t, db.Create(&models.Customer{FirstName: "X", LastName: "Y", Email: "x@example.com"}).Error)

	_, err := repo.ListAllForExport("", "email; DROP TABLE customers", "asc")
	assert.Error(t, err)

	var count int64
	require.NoError(t, db.Model(&models.Customer{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "the customers table must still be there")
}
