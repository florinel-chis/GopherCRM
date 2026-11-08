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

// CustomerExternalIDRepository (#212) on the full production schema with
// foreign keys on: SQLite, and a disposable MySQL schema on TEST_MYSQL_DSN
// (skipped without it, failing when the server is unreachable).

func TestCustomerExternalIDRepositorySQLite(t *testing.T) {
	runCustomerExternalIDRepository(t, openIdentityErasureDB(t, false))
}

func TestCustomerExternalIDRepositoryMySQL(t *testing.T) {
	if os.Getenv("TEST_MYSQL_DSN") == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	runCustomerExternalIDRepository(t, openIdentityErasureDB(t, true))
}

func seedExternalIDCustomer(t *testing.T, db *gorm.DB, label string) *models.Customer {
	t.Helper()
	tag := nextIdentityTag()
	customer := &models.Customer{FirstName: "Ext" + label, LastName: "Customer" + tag,
		Email: fmt.Sprintf("ext-%s-%s@person.test", label, tag)}
	require.NoError(t, db.Create(customer).Error)
	return customer
}

func countExternalIDs(t *testing.T, db *gorm.DB, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	q := db.Model(&models.CustomerExternalID{})
	if where != "" {
		q = q.Where(where, args...)
	}
	require.NoError(t, q.Count(&n).Error)
	return n
}

func runCustomerExternalIDRepository(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	repo := NewCustomerExternalIDRepository(db)
	a := seedExternalIDCustomer(t, db, "a")
	b := seedExternalIDCustomer(t, db, "b")
	gone := seedExternalIDCustomer(t, db, "gone")

	t.Run("create and duplicate", func(t *testing.T) {
		first := &models.CustomerExternalID{CustomerID: a.ID, Source: "Shop-A", Scope: "", ExternalID: "X1"}
		require.NoError(t, repo.Create(ctx, first))
		assert.NotZero(t, first.ID)
		assert.EqualValues(t, 1, countExternalIDs(t, db, ""))

		for _, owner := range []uint{a.ID, b.ID} {
			dup := &models.CustomerExternalID{CustomerID: owner, Source: "Shop-A", Scope: "", ExternalID: "X1"}
			err := repo.Create(ctx, dup)
			require.Error(t, err)
			assert.True(t, errors.Is(err, apperrors.ErrCustomerExternalIDTaken), "duplicate for customer %d: %v", owner, err)
			assert.NotContains(t, err.Error(), "Shop-A", "the error never carries the triple")
			assert.NotContains(t, err.Error(), "X1")
		}
		assert.EqualValues(t, 1, countExternalIDs(t, db, ""), "a refused duplicate writes nothing")

		// A duplicate inside a transaction is refused the same way and the
		// transaction can still commit its other work.
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			err := repo.WithTx(tx).Create(ctx, &models.CustomerExternalID{CustomerID: b.ID, Source: "Shop-A", ExternalID: "X1"})
			if !errors.Is(err, apperrors.ErrCustomerExternalIDTaken) {
				return fmt.Errorf("expected the taken sentinel, got %v", err)
			}
			return repo.WithTx(tx).Create(ctx, &models.CustomerExternalID{CustomerID: b.ID, Source: "tx-shop", ExternalID: "T1"})
		}))
		assert.EqualValues(t, 1, countExternalIDs(t, db, "source = ?", "tx-shop"))
	})

	t.Run("case and accents are distinct", func(t *testing.T) {
		require.NoError(t, repo.Create(ctx, &models.CustomerExternalID{CustomerID: b.ID, Source: "shop-a", Scope: "", ExternalID: "x1"}))
		require.NoError(t, repo.Create(ctx, &models.CustomerExternalID{CustomerID: b.ID, Source: "Shop-A", Scope: "Site", ExternalID: "X1"}))
		require.NoError(t, repo.Create(ctx, &models.CustomerExternalID{CustomerID: b.ID, Source: "Shop-A", Scope: "", ExternalID: "Xé"}))
		require.NoError(t, repo.Create(ctx, &models.CustomerExternalID{CustomerID: a.ID, Source: "Shop-A", Scope: "", ExternalID: "Xe"}))
		assert.EqualValues(t, 2, countExternalIDs(t, db, "scope = '' AND source IN ? AND external_id IN ?",
			[]string{"Shop-A", "shop-a"}, []string{"X1", "x1"}), "('Shop-A','','X1') and ('shop-a','','x1') coexist")
	})

	t.Run("find", func(t *testing.T) {
		found, err := repo.FindCustomerByIdentity(ctx, "Shop-A", "", "X1")
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, a.ID, found.ID)

		found, err = repo.FindCustomerByIdentity(ctx, "shop-a", "", "x1")
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, b.ID, found.ID)

		found, err = repo.FindCustomerByIdentity(ctx, "Shop-A", "", "Xe")
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, a.ID, found.ID, "Xe and Xé are different identities")

		for _, miss := range [][3]string{
			{"SHOP-A", "", "X1"}, {"Shop-A", "", "x1"}, {"shop-a", "", "X1"}, {"Shop-A", "site", "X1"},
			{"Shop-A", "Other", "X1"}, {"unknown", "", "X1"}, {"Shop-A", "", "X2"},
		} {
			found, err := repo.FindCustomerByIdentity(ctx, miss[0], miss[1], miss[2])
			require.NoError(t, err)
			assert.Nil(t, found, "no customer for %q", miss)
		}

		// A mapping of a soft-deleted customer is not answered.
		require.NoError(t, repo.Create(ctx, &models.CustomerExternalID{CustomerID: gone.ID, Source: "Shop-A", ExternalID: "G1"}))
		found, err = repo.FindCustomerByIdentity(ctx, "Shop-A", "", "G1")
		require.NoError(t, err)
		require.NotNil(t, found)
		require.NoError(t, db.Delete(&models.Customer{}, gone.ID).Error)
		found, err = repo.FindCustomerByIdentity(ctx, "Shop-A", "", "G1")
		require.NoError(t, err)
		assert.Nil(t, found, "a soft-deleted customer is never returned")
	})

	t.Run("lock live customer", func(t *testing.T) {
		require.NoError(t, repo.LockLiveCustomer(ctx, a.ID))
		assert.ErrorIs(t, repo.LockLiveCustomer(ctx, gone.ID), gorm.ErrRecordNotFound, "a soft-deleted customer is not live")
		assert.ErrorIs(t, repo.LockLiveCustomer(ctx, 99999999), gorm.ErrRecordNotFound)
	})

	t.Run("delete for customer", func(t *testing.T) {
		var mapping models.CustomerExternalID
		require.NoError(t, db.Where("customer_id = ? AND source = ? AND external_id = ?", a.ID, "Shop-A", "X1").Take(&mapping).Error)

		assert.ErrorIs(t, repo.DeleteForCustomer(ctx, b.ID, mapping.ID), gorm.ErrRecordNotFound, "another customer's mapping")
		assert.ErrorIs(t, repo.DeleteForCustomer(ctx, a.ID, 99999999), gorm.ErrRecordNotFound, "an unknown mapping")
		assert.EqualValues(t, 1, countExternalIDs(t, db, "id = ?", mapping.ID), "a refused delete removes nothing")
		before := countExternalIDs(t, db, "")

		require.NoError(t, repo.DeleteForCustomer(ctx, a.ID, mapping.ID))
		assert.EqualValues(t, 0, countExternalIDs(t, db, "id = ?", mapping.ID))
		var raw int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM customer_external_ids WHERE id = ?", mapping.ID).Scan(&raw).Error)
		assert.Zero(t, raw, "the row is hard-deleted")
		assert.Equal(t, before-1, countExternalIDs(t, db, ""), "only that row is deleted")
		assert.ErrorIs(t, repo.DeleteForCustomer(ctx, a.ID, mapping.ID), gorm.ErrRecordNotFound, "a second delete finds nothing")

		// The triple is free again.
		require.NoError(t, repo.Create(ctx, &models.CustomerExternalID{CustomerID: b.ID, Source: "Shop-A", ExternalID: "X1"}))
		found, err := repo.FindCustomerByIdentity(ctx, "Shop-A", "", "X1")
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, b.ID, found.ID)
	})
}
