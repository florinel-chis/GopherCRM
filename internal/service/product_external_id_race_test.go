package service

// A mapping create racing the delete of its product (#239). AddExternalID
// locks the live product row and then inserts, in one transaction;
// ProductService.Delete takes the same lock, purges the product's mappings and
// soft-deletes it, in one transaction. The race therefore ends in one of two
// states, never with a mapping on a deleted product: the create commits first
// and the delete purges its mapping, or the delete commits first and the
// create answers ErrNotFound with nothing written.
//
// On SQLite the single pooled connection serialises the two, so both
// orderings run in sequence (and concurrently, where either outcome is
// acceptable). On MySQL both orderings are forced with GORM callbacks on the
// test's own handles, each wait is confirmed in information_schema.PROCESSLIST
// on two consecutive polls, and no error 1213 may occur.

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The locking read both sides wait on. MariaDB can keep `?` in INFO, so the
// pattern depends on no parameter's value.
var productExternalIDProductLock = regexp.MustCompile("^SELECT `id` FROM `products` .*FOR UPDATE$")

func assertNoMappingOnDeletedProduct(t *testing.T, db *gorm.DB) {
	t.Helper()
	var orphans int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM product_external_ids m JOIN products p ON p.id = m.product_id
		WHERE p.deleted_at IS NOT NULL`).Scan(&orphans).Error)
	assert.Zero(t, orphans, "no mapping row on a deleted product")
}

func newProductDeleteService(db *gorm.DB) ProductService {
	return NewProductService(repository.NewProductRepository(db), nil,
		WithProductExternalIDs(repository.NewProductExternalIDRepository(db), utils.NewTransactionManager(db)))
}

func TestProductExternalIDCreateRacesDeleteSQLite(t *testing.T) {
	db := openExternalIDSQLite(t)
	svc := NewProductExternalIDService(repository.NewProductExternalIDRepository(db), utils.NewTransactionManager(db))
	products := newProductDeleteService(db)
	ctx := context.Background()

	t.Run("create_commits_first", func(t *testing.T) {
		product := seedExternalIDServiceProduct(t, db, "race-create-first", true)
		mapping, err := svc.AddExternalID(ctx, product.ID, "race", "", "create-first")
		require.NoError(t, err)
		require.NotZero(t, mapping.ID)
		require.NoError(t, products.Delete(ctx, product.ID))
		assert.True(t, productDeleted(t, db, product.ID))
		assert.Zero(t, rawProductMappings(t, db, product.ID), "the delete purged the mapping")
		assertNoMappingOnDeletedProduct(t, db)
	})

	t.Run("delete_commits_first", func(t *testing.T) {
		product := seedExternalIDServiceProduct(t, db, "race-delete-first", true)
		require.NoError(t, products.Delete(ctx, product.ID))
		mapping, err := svc.AddExternalID(ctx, product.ID, "race", "", "delete-first")
		assert.Nil(t, mapping)
		require.ErrorIs(t, err, apperrors.ErrNotFound)
		assert.Zero(t, rawProductMappings(t, db, product.ID), "nothing is written")
		assertNoMappingOnDeletedProduct(t, db)
	})

	t.Run("concurrent", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			product := seedExternalIDServiceProduct(t, db, fmt.Sprintf("race-concurrent-%d", i), true)
			var wg sync.WaitGroup
			var createErr, deleteErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, createErr = svc.AddExternalID(ctx, product.ID, "race", "", fmt.Sprintf("concurrent-%d", i))
			}()
			go func() {
				defer wg.Done()
				deleteErr = products.Delete(ctx, product.ID)
			}()
			wg.Wait()
			require.NoError(t, deleteErr)
			if createErr != nil {
				require.ErrorIs(t, createErr, apperrors.ErrNotFound, "the only refusal is a product already deleted")
			}
			assert.True(t, productDeleted(t, db, product.ID))
			assert.Zero(t, rawProductMappings(t, db, product.ID))
		}
		assertNoMappingOnDeletedProduct(t, db)
	})
}

func TestProductExternalIDCreateRacesDeleteMySQL(t *testing.T) {
	handles := openExternalIDMySQL(t, 3)
	createDB, deleteDB, probeDB := handles[0], handles[1], handles[2]
	svc := NewProductExternalIDService(repository.NewProductExternalIDRepository(createDB), utils.NewTransactionManager(createDB))
	products := newProductDeleteService(deleteDB)

	var mu sync.Mutex
	var beforeInsert, afterSoftDelete *externalIDGate
	require.NoError(t, createDB.Callback().Create().Before("gorm:create").Register("product_extid_race:before_insert", func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "product_external_ids" {
			return
		}
		mu.Lock()
		gate := beforeInsert
		mu.Unlock()
		if gate != nil {
			gate.pause()
		}
	}))
	require.NoError(t, deleteDB.Callback().Delete().After("gorm:delete").Register("product_extid_race:after_soft_delete", func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "products" {
			return
		}
		mu.Lock()
		gate := afterSoftDelete
		mu.Unlock()
		if gate != nil {
			gate.pause()
		}
	}))

	t.Run("create_commits_first", func(t *testing.T) {
		product := seedExternalIDServiceProduct(t, createDB, "mysql-create-first", true)
		gate := newExternalIDGate()
		mu.Lock()
		beforeInsert = gate
		mu.Unlock()
		t.Cleanup(func() {
			mu.Lock()
			beforeInsert = nil
			mu.Unlock()
		})

		createDone := make(chan error, 1)
		var created *models.ProductExternalID
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), externalIDRaceTimeout)
			defer cancel()
			mapping, err := svc.AddExternalID(ctx, product.ID, "race", "", "mysql-create-first")
			created = mapping
			createDone <- err
		}()
		// The create holds the product row lock and is parked before its insert.
		gate.awaitReached(t)

		deleteDone := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), externalIDRaceTimeout)
			defer cancel()
			deleteDone <- products.Delete(ctx, product.ID)
		}()
		// The delete's locking read of the product waits for that lock.
		awaitExternalIDLockWait(t, probeDB, productExternalIDProductLock)
		close(gate.release)

		var createErr, deleteErr error
		select {
		case createErr = <-createDone:
		case <-time.After(externalIDRaceTimeout):
			t.Fatal("the create never finished")
		}
		select {
		case deleteErr = <-deleteDone:
		case <-time.After(externalIDRaceTimeout):
			t.Fatal("the delete never finished")
		}
		assertNotDeadlock(t, createErr)
		assertNotDeadlock(t, deleteErr)
		require.NoError(t, createErr, "the create committed first")
		require.NotNil(t, created)
		require.NoError(t, deleteErr)
		assert.True(t, productDeleted(t, createDB, product.ID))
		assert.Zero(t, rawProductMappings(t, createDB, product.ID), "the delete purged the mapping the create committed")
		assertNoMappingOnDeletedProduct(t, createDB)
	})

	t.Run("delete_commits_first", func(t *testing.T) {
		product := seedExternalIDServiceProduct(t, createDB, "mysql-delete-first", true)
		gate := newExternalIDGate()
		mu.Lock()
		afterSoftDelete = gate
		mu.Unlock()
		t.Cleanup(func() {
			mu.Lock()
			afterSoftDelete = nil
			mu.Unlock()
		})

		deleteDone := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), externalIDRaceTimeout)
			defer cancel()
			deleteDone <- products.Delete(ctx, product.ID)
		}()
		// The delete has locked the product, purged its mappings and
		// soft-deleted it, and is parked before its commit.
		gate.awaitReached(t)

		createDone := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), externalIDRaceTimeout)
			defer cancel()
			_, err := svc.AddExternalID(ctx, product.ID, "race", "", "mysql-delete-first")
			createDone <- err
		}()
		// The create's locking read of the product waits for the delete.
		awaitExternalIDLockWait(t, probeDB, productExternalIDProductLock)
		close(gate.release)

		var createErr, deleteErr error
		select {
		case deleteErr = <-deleteDone:
		case <-time.After(externalIDRaceTimeout):
			t.Fatal("the delete never finished")
		}
		select {
		case createErr = <-createDone:
		case <-time.After(externalIDRaceTimeout):
			t.Fatal("the create never finished")
		}
		assertNotDeadlock(t, createErr)
		assertNotDeadlock(t, deleteErr)
		require.NoError(t, deleteErr, "the delete committed first")
		require.ErrorIs(t, createErr, apperrors.ErrNotFound, "the create finds no live product")
		assert.True(t, productDeleted(t, createDB, product.ID))
		assert.Zero(t, rawProductMappings(t, createDB, product.ID), "nothing is written")
		assertNoMappingOnDeletedProduct(t, createDB)
	})
}
