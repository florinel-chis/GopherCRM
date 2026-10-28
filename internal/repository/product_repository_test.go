package repository

import (
	"errors"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// setupQuoteRepoDB gives each test a private schema on its own shared
// in-memory DSN (separate from the deal tests' one), with foreign keys on.
// The four quote tables are dropped and recreated per test; the tables they
// reference are migrated in place.
func setupQuoteRepoDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:quotes_repo?mode=memory&cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{
		Logger: logger.Discard,
	})
	require.NoError(t, err)

	quoteTables := []interface{}{&models.QuoteStatusChange{}, &models.QuoteLine{}, &models.Quote{}, &models.Product{}}
	require.NoError(t, db.Migrator().DropTable(quoteTables...))
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.Company{}, &models.Lead{}, &models.Customer{}, &models.Deal{},
		&models.Product{}, &models.Quote{}, &models.QuoteLine{}, &models.QuoteStatusChange{},
	))

	t.Cleanup(func() {
		_ = db.Migrator().DropTable(quoteTables...)
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func createTestProduct(t *testing.T, db *gorm.DB, product *models.Product) *models.Product {
	t.Helper()
	if product.Currency == "" {
		product.Currency = "EUR"
	}
	require.NoError(t, db.Create(product).Error)
	return product
}

func productNames(products []models.Product) []string {
	names := make([]string, 0, len(products))
	for _, product := range products {
		names = append(names, product.Name)
	}
	return names
}

func TestProductRepository_ListFiltersSortsAndHidesDeleted(t *testing.T) {
	db := setupQuoteRepoDB(t)
	repo := NewProductRepository(db)

	createTestProduct(t, db, &models.Product{Name: "Widget", SKU: "SKU-1", UnitPriceCents: 1000, IsActive: true})
	createTestProduct(t, db, &models.Product{Name: "Gadget", SKU: "SKU-2", UnitPriceCents: 2500, IsActive: true})
	createTestProduct(t, db, &models.Product{Name: "Gizmo", SKU: "SKU-3", UnitPriceCents: 500, Currency: "USD", IsActive: true})
	createTestProduct(t, db, &models.Product{Name: "Retired", SKU: "OLD-9", UnitPriceCents: 100, IsActive: false})
	deleted := createTestProduct(t, db, &models.Product{Name: "Deleted", SKU: "SKU-4", UnitPriceCents: 1, IsActive: true})
	require.NoError(t, repo.Delete(deleted.ID))

	products, total, err := repo.List(0, 100, ProductListFilter{})
	require.NoError(t, err)
	assert.EqualValues(t, 4, total, "a soft-deleted product is not counted")
	assert.NotContains(t, productNames(products), "Deleted")
	assert.Equal(t, []string{"Deleted", "Retired", "Gizmo", "Gadget", "Widget"}[1:], productNames(products), "newest first by default")

	products, total, err = repo.List(0, 2, ProductListFilter{})
	require.NoError(t, err)
	assert.EqualValues(t, 4, total, "the total counts the whole filter, not the page")
	assert.Len(t, products, 2)

	inactive := false
	products, total, err = repo.List(0, 100, ProductListFilter{IsActive: &inactive})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, []string{"Retired"}, productNames(products))

	products, _, err = repo.List(0, 100, ProductListFilter{Currency: "USD"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Gizmo"}, productNames(products))

	products, _, err = repo.List(0, 100, ProductListFilter{Search: "adget"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Gadget"}, productNames(products), "search matches the name")
	products, _, err = repo.List(0, 100, ProductListFilter{Search: "OLD-"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Retired"}, productNames(products), "search matches the SKU")
	products, _, err = repo.List(0, 100, ProductListFilter{Search: "SKU-4"})
	require.NoError(t, err)
	assert.Empty(t, products, "a soft-deleted product is not found by search either")

	products, _, err = repo.List(0, 100, ProductListFilter{SortBy: "unit_price_cents", SortOrder: "asc"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Retired", "Gizmo", "Widget", "Gadget"}, productNames(products))
	products, _, err = repo.List(0, 100, ProductListFilter{SortBy: "name", SortOrder: "desc"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Widget", "Retired", "Gizmo", "Gadget"}, productNames(products))

	_, err = repo.GetByID(deleted.ID)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "a soft-deleted product is not found: %v", err)
	assert.True(t, errors.Is(repo.Delete(deleted.ID), gorm.ErrRecordNotFound), "deleting it again is a not-found")
	var unscoped models.Product
	require.NoError(t, db.Unscoped().First(&unscoped, deleted.ID).Error)
	assert.Equal(t, "Deleted", unscoped.Name, "the row itself stays for the quote lines that reference it")
}

func TestProductRepository_ListRejectsUnknownSortColumns(t *testing.T) {
	db := setupQuoteRepoDB(t)
	repo := NewProductRepository(db)
	for _, column := range []string{"deleted_at", "description", "name; DROP TABLE products;--", "owner_id"} {
		_, _, err := repo.List(0, 10, ProductListFilter{SortBy: column})
		assert.Errorf(t, err, "%q must be refused before it reaches the query", column)
	}
}

// Save writes every column, so a price of 0 and an inactive flag survive an
// update; a GORM default would have replaced them on create.
func TestProductRepository_UpdateKeepsZeroValues(t *testing.T) {
	db := setupQuoteRepoDB(t)
	repo := NewProductRepository(db)
	product := createTestProduct(t, db, &models.Product{Name: "Widget", UnitPriceCents: 0, IsActive: false})

	stored, err := repo.GetByID(product.ID)
	require.NoError(t, err)
	assert.Zero(t, stored.UnitPriceCents, "an explicit 0 price is stored as 0")
	assert.False(t, stored.IsActive, "an explicit false is stored as false")

	stored.UnitPriceCents = 1250
	stored.IsActive = true
	require.NoError(t, repo.Update(stored))
	stored.UnitPriceCents = 0
	stored.IsActive = false
	require.NoError(t, repo.Update(stored))

	again, err := repo.GetByID(product.ID)
	require.NoError(t, err)
	assert.Zero(t, again.UnitPriceCents)
	assert.False(t, again.IsActive)

	for i := 0; i < 3; i++ {
		createTestProduct(t, db, &models.Product{Name: fmt.Sprintf("Bulk %d", i), UnitPriceCents: int64(i), IsActive: true})
	}
	_, total, err := repo.List(0, 1, ProductListFilter{})
	require.NoError(t, err)
	assert.EqualValues(t, 4, total)
}
