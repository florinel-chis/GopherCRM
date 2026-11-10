package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ProductExternalIDRepository (#239) on the full production schema with foreign
// keys on (models.MigrateDatabase, collation guard included): SQLite, and a
// disposable MySQL schema on TEST_MYSQL_DSN (skipped without it, failing when
// the server is unreachable).

func TestProductExternalIDRepositorySQLite(t *testing.T) {
	runProductExternalIDRepository(t, openIdentityErasureDB(t, false))
}

func TestProductExternalIDRepositoryMySQL(t *testing.T) {
	if os.Getenv("TEST_MYSQL_DSN") == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	runProductExternalIDRepository(t, openIdentityErasureDB(t, true))
}

func seedExternalIDProduct(t *testing.T, db *gorm.DB, label string, active bool) *models.Product {
	t.Helper()
	product := &models.Product{Name: fmt.Sprintf("Product %s %s", label, nextIdentityTag()), SKU: "SHARED-SKU",
		UnitPriceCents: 500, Currency: "EUR", IsActive: active}
	require.NoError(t, db.Create(product).Error)
	return product
}

func countProductExternalIDs(t *testing.T, db *gorm.DB, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	q := db.Model(&models.ProductExternalID{})
	if where != "" {
		q = q.Where(where, args...)
	}
	require.NoError(t, q.Count(&n).Error)
	return n
}

func runProductExternalIDRepository(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	repo := NewProductExternalIDRepository(db)
	a := seedExternalIDProduct(t, db, "a", true)
	b := seedExternalIDProduct(t, db, "b", true)
	inactive := seedExternalIDProduct(t, db, "inactive", false)
	gone := seedExternalIDProduct(t, db, "gone", true)

	t.Run("create and duplicate", func(t *testing.T) {
		first := &models.ProductExternalID{ProductID: a.ID, Source: "ERP-A", Scope: "", ExternalID: "M1"}
		require.NoError(t, repo.Create(ctx, first))
		assert.NotZero(t, first.ID)
		assert.EqualValues(t, 1, countProductExternalIDs(t, db, ""))

		for _, owner := range []uint{a.ID, b.ID} {
			dup := &models.ProductExternalID{ProductID: owner, Source: "ERP-A", Scope: "", ExternalID: "M1"}
			err := repo.Create(ctx, dup)
			require.Error(t, err)
			assert.True(t, errors.Is(err, apperrors.ErrProductExternalIDTaken), "duplicate for product %d: %v", owner, err)
			assert.NotContains(t, err.Error(), "ERP-A", "the error never carries the triple")
			assert.NotContains(t, err.Error(), "M1")
			assert.NotContains(t, err.Error(), "Duplicate", "no driver text")
			assert.NotContains(t, err.Error(), "UNIQUE", "no driver text")
		}
		assert.EqualValues(t, 1, countProductExternalIDs(t, db, ""), "a refused duplicate writes nothing")

		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			err := repo.WithTx(tx).Create(ctx, &models.ProductExternalID{ProductID: b.ID, Source: "ERP-A", ExternalID: "M1"})
			if !errors.Is(err, apperrors.ErrProductExternalIDTaken) {
				return fmt.Errorf("expected the taken sentinel, got %v", err)
			}
			return repo.WithTx(tx).Create(ctx, &models.ProductExternalID{ProductID: b.ID, Source: "tx-erp", ExternalID: "T1"})
		}))
		assert.EqualValues(t, 1, countProductExternalIDs(t, db, "source = ?", "tx-erp"))
	})

	t.Run("case and scope are distinct", func(t *testing.T) {
		require.NoError(t, repo.Create(ctx, &models.ProductExternalID{ProductID: b.ID, Source: "erp-a", Scope: "", ExternalID: "m1"}))
		require.NoError(t, repo.Create(ctx, &models.ProductExternalID{ProductID: b.ID, Source: "ERP-A", Scope: "Plant", ExternalID: "M1"}))
		require.NoError(t, repo.Create(ctx, &models.ProductExternalID{ProductID: b.ID, Source: "ERP-A", Scope: "", ExternalID: "Mé"}))
		require.NoError(t, repo.Create(ctx, &models.ProductExternalID{ProductID: a.ID, Source: "ERP-A", Scope: "", ExternalID: "Me"}))
		assert.EqualValues(t, 2, countProductExternalIDs(t, db, "scope = '' AND source IN ? AND external_id IN ?",
			[]string{"ERP-A", "erp-a"}, []string{"M1", "m1"}), "('ERP-A','','M1') and ('erp-a','','m1') coexist")
	})

	t.Run("find", func(t *testing.T) {
		found, err := repo.FindProductByIdentity(ctx, "ERP-A", "", "M1")
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, a.ID, found.ID)
		assert.Equal(t, a.Name, found.Name, "the whole product row is returned")

		found, err = repo.FindProductByIdentity(ctx, "erp-a", "", "m1")
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, b.ID, found.ID)

		found, err = repo.FindProductByIdentity(ctx, "ERP-A", "Plant", "M1")
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, b.ID, found.ID)

		for _, miss := range [][3]string{
			{"erp-A", "", "M1"}, {"ERP-A", "", "m1"}, {"erp-a", "", "M1"}, {"ERP-A", "plant", "M1"},
			{"ERP-A", "Other", "M1"}, {"unknown", "", "M1"}, {"ERP-A", "", "M2"},
		} {
			found, err := repo.FindProductByIdentity(ctx, miss[0], miss[1], miss[2])
			require.NoError(t, err)
			assert.Nil(t, found, "no product for %q", miss)
		}

		// An inactive product is still found.
		require.NoError(t, repo.Create(ctx, &models.ProductExternalID{ProductID: inactive.ID, Source: "ERP-A", ExternalID: "I1"}))
		found, err = repo.FindProductByIdentity(ctx, "ERP-A", "", "I1")
		require.NoError(t, err)
		require.NotNil(t, found, "an inactive product is a live product")
		assert.Equal(t, inactive.ID, found.ID)
		assert.False(t, found.IsActive)

		// A mapping of a soft-deleted product is not answered.
		require.NoError(t, repo.Create(ctx, &models.ProductExternalID{ProductID: gone.ID, Source: "ERP-A", ExternalID: "G1"}))
		found, err = repo.FindProductByIdentity(ctx, "ERP-A", "", "G1")
		require.NoError(t, err)
		require.NotNil(t, found)
		require.NoError(t, db.Delete(&models.Product{}, gone.ID).Error)
		found, err = repo.FindProductByIdentity(ctx, "ERP-A", "", "G1")
		require.NoError(t, err)
		assert.Nil(t, found, "a soft-deleted product is never returned")
	})

	t.Run("lock live product", func(t *testing.T) {
		require.NoError(t, repo.LockLiveProduct(ctx, a.ID))
		require.NoError(t, repo.LockLiveProduct(ctx, inactive.ID), "an inactive product is live")
		assert.ErrorIs(t, repo.LockLiveProduct(ctx, gone.ID), gorm.ErrRecordNotFound, "a soft-deleted product is not live")
		assert.ErrorIs(t, repo.LockLiveProduct(ctx, 99999999), gorm.ErrRecordNotFound)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return repo.WithTx(tx).LockLiveProduct(ctx, b.ID)
		}))
	})

	t.Run("delete for product", func(t *testing.T) {
		var mapping models.ProductExternalID
		require.NoError(t, db.Where("product_id = ? AND source = ? AND external_id = ?", a.ID, "ERP-A", "M1").Take(&mapping).Error)

		assert.ErrorIs(t, repo.DeleteForProduct(ctx, b.ID, mapping.ID), gorm.ErrRecordNotFound, "another product's mapping")
		assert.ErrorIs(t, repo.DeleteForProduct(ctx, a.ID, 99999999), gorm.ErrRecordNotFound, "an unknown mapping")
		assert.EqualValues(t, 1, countProductExternalIDs(t, db, "id = ?", mapping.ID), "a refused delete removes nothing")
		before := countProductExternalIDs(t, db, "")

		require.NoError(t, repo.DeleteForProduct(ctx, a.ID, mapping.ID))
		var raw int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM product_external_ids WHERE id = ?", mapping.ID).Scan(&raw).Error)
		assert.Zero(t, raw, "the row is hard-deleted")
		assert.Equal(t, before-1, countProductExternalIDs(t, db, ""), "only that row is deleted")
		assert.ErrorIs(t, repo.DeleteForProduct(ctx, a.ID, mapping.ID), gorm.ErrRecordNotFound, "a second delete finds nothing")

		require.NoError(t, repo.Create(ctx, &models.ProductExternalID{ProductID: b.ID, Source: "ERP-A", ExternalID: "M1"}))
		found, err := repo.FindProductByIdentity(ctx, "ERP-A", "", "M1")
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, b.ID, found.ID, "the triple is free again")
	})

	t.Run("purge for product", func(t *testing.T) {
		otherBefore := countProductExternalIDs(t, db, "product_id <> ?", b.ID)
		ofB := countProductExternalIDs(t, db, "product_id = ?", b.ID)
		require.NotZero(t, ofB)

		removed, err := repo.PurgeForProduct(ctx, b.ID)
		require.NoError(t, err)
		assert.Equal(t, ofB, removed)
		var raw int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM product_external_ids WHERE product_id = ?", b.ID).Scan(&raw).Error)
		assert.Zero(t, raw, "every mapping of the product is hard-deleted")
		assert.Equal(t, otherBefore, countProductExternalIDs(t, db, "product_id <> ?", b.ID), "other products keep theirs")

		removed, err = repo.PurgeForProduct(ctx, b.ID)
		require.NoError(t, err)
		assert.Zero(t, removed, "nothing left to purge is not an error")
	})
}
