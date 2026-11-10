package service

// ProductService.Delete (#239): one transaction that locks the live product,
// hard-deletes its product_external_ids rows and soft-deletes it, so the
// triples can be mapped again. On SQLite (one pooled connection) and on a
// disposable MySQL schema on TEST_MYSQL_DSN (skipped without it).

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errInjectedSoftDelete = errors.New("injected soft delete failure")

// failingSoftDeleteRepo is the real product repository whose Delete fails, on
// the root handle and on every transaction-bound copy.
type failingSoftDeleteRepo struct {
	repository.ProductRepository
}

func (r failingSoftDeleteRepo) Delete(uint) error { return errInjectedSoftDelete }

func (r failingSoftDeleteRepo) WithTx(tx *gorm.DB) repository.ProductRepository {
	return failingSoftDeleteRepo{r.ProductRepository.WithTx(tx)}
}

// deleteStatement is one statement GORM ran while the recorder was armed.
type deleteStatement struct {
	kind  string
	table string
	inTx  bool
}

// statementRecorder records the table and transaction state of every query
// and delete GORM runs on db while armed.
type statementRecorder struct {
	armed atomic.Bool
	mu    sync.Mutex
	seen  []deleteStatement
}

func recordProductDeleteStatements(t *testing.T, db *gorm.DB) *statementRecorder {
	t.Helper()
	rec := &statementRecorder{}
	hook := func(kind string) func(*gorm.DB) {
		return func(tx *gorm.DB) {
			if !rec.armed.Load() {
				return
			}
			_, inTx := tx.Statement.ConnPool.(gorm.TxCommitter)
			rec.mu.Lock()
			rec.seen = append(rec.seen, deleteStatement{kind: kind, table: tx.Statement.Table, inTx: inTx})
			rec.mu.Unlock()
		}
	}
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("product_delete_test:query", hook("query")))
	require.NoError(t, db.Callback().Delete().After("gorm:delete").Register("product_delete_test:delete", hook("delete")))
	return rec
}

func (r *statementRecorder) capture(fn func()) []deleteStatement {
	r.mu.Lock()
	r.seen = nil
	r.mu.Unlock()
	r.armed.Store(true)
	fn()
	r.armed.Store(false)
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]deleteStatement(nil), r.seen...)
}

func seedProductDeleteQuote(t *testing.T, db *gorm.DB, product *models.Product) *models.QuoteLine {
	t.Helper()
	owner := &models.User{Email: fmt.Sprintf("owner-%d@product-delete.test", product.ID), Password: "$2a$10$ownerhash",
		FirstName: "Quote", LastName: "Owner", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(owner).Error)
	quote := &models.Quote{Title: "Quote for " + product.Name, Status: models.QuoteStatus("draft"), Currency: "EUR",
		SubtotalCents: 500, TotalCents: 500, OwnerID: owner.ID}
	require.NoError(t, db.Omit(clause.Associations).Create(quote).Error)
	line := &models.QuoteLine{QuoteID: quote.ID, ProductID: product.ID, Position: 1, Description: product.Name,
		Quantity: 2, UnitPriceCents: 250, LineTotalCents: 500}
	require.NoError(t, db.Omit(clause.Associations).Create(line).Error)
	return line
}

func rawProductMappings(t *testing.T, db *gorm.DB, productID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM product_external_ids WHERE product_id = ?", productID).Scan(&n).Error)
	return n
}

func productDeleted(t *testing.T, db *gorm.DB, productID uint) bool {
	t.Helper()
	var p models.Product
	require.NoError(t, db.Unscoped().First(&p, productID).Error)
	return p.DeletedAt.Valid
}

func TestProductDeleteFreesExternalIDsSQLite(t *testing.T) {
	runProductDeleteFreesExternalIDs(t, openExternalIDSQLite(t))
}

func TestProductDeleteFreesExternalIDsMySQL(t *testing.T) {
	handles := openExternalIDMySQL(t, 1)
	runProductDeleteFreesExternalIDs(t, handles[0])
}

func runProductDeleteFreesExternalIDs(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	tm := utils.NewTransactionManager(db)
	extRepo := repository.NewProductExternalIDRepository(db)
	productRepo := repository.NewProductRepository(db)
	products := NewProductService(productRepo, nil, WithProductExternalIDs(extRepo, tm))
	mappings := NewProductExternalIDService(extRepo, tm)
	rec := recordProductDeleteStatements(t, db)

	target := seedExternalIDServiceProduct(t, db, "purge-target", true)
	other := seedExternalIDServiceProduct(t, db, "purge-other", false)
	for _, triple := range [][3]string{{"erp", "", "P-1"}, {"erp", "plant", "P-1"}, {"shop", "", "p-1"}} {
		_, err := mappings.AddExternalID(ctx, target.ID, triple[0], triple[1], triple[2])
		require.NoError(t, err)
	}
	otherMapping, err := mappings.AddExternalID(ctx, other.ID, "erp", "", "O-1")
	require.NoError(t, err)
	line := seedProductDeleteQuote(t, db, target)

	t.Run("a failed soft delete rolls the purge back", func(t *testing.T) {
		failing := NewProductService(failingSoftDeleteRepo{productRepo}, nil, WithProductExternalIDs(extRepo, tm))
		err := failing.Delete(ctx, target.ID)
		require.ErrorIs(t, err, errInjectedSoftDelete)
		assert.EqualValues(t, 3, rawProductMappings(t, db, target.ID), "the purge rolled back with the soft delete")
		assert.False(t, productDeleted(t, db, target.ID), "the product is still live")
		found, err := mappings.LookupProduct(ctx, "erp", "", "P-1")
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, target.ID, found.ID)
	})

	t.Run("delete purges the mappings in one transaction", func(t *testing.T) {
		var deleteErr error
		statements := rec.capture(func() { deleteErr = products.Delete(ctx, target.ID) })
		require.NoError(t, deleteErr)

		// The locking read of the product, the purge, the soft delete: all
		// on the transaction's connection, in that order.
		require.Len(t, statements, 3, "%+v", statements)
		assert.Equal(t, deleteStatement{kind: "query", table: "products", inTx: true}, statements[0], "the locking read comes first")
		assert.Equal(t, deleteStatement{kind: "delete", table: "product_external_ids", inTx: true}, statements[1], "then the purge")
		assert.Equal(t, deleteStatement{kind: "delete", table: "products", inTx: true}, statements[2], "then the soft delete")

		assert.Zero(t, rawProductMappings(t, db, target.ID), "no mapping of the deleted product remains")
		assert.True(t, productDeleted(t, db, target.ID), "the product is soft-deleted, not removed")
		assert.EqualValues(t, 1, rawProductMappings(t, db, other.ID), "other products keep their mappings")
		var kept models.ProductExternalID
		require.NoError(t, db.First(&kept, otherMapping.ID).Error)
		assert.Equal(t, "O-1", kept.ExternalID)

		var storedLine models.QuoteLine
		require.NoError(t, db.First(&storedLine, line.ID).Error)
		assert.Equal(t, target.ID, storedLine.ProductID, "the quote line keeps its product_id")
		assert.Equal(t, line.Description, storedLine.Description)
		assert.Equal(t, line.UnitPriceCents, storedLine.UnitPriceCents)

		found, err := mappings.LookupProduct(ctx, "erp", "", "P-1")
		require.NoError(t, err)
		assert.Nil(t, found, "the freed triple maps to nothing")
	})

	t.Run("the freed triples map to another product", func(t *testing.T) {
		for _, triple := range [][3]string{{"erp", "", "P-1"}, {"erp", "plant", "P-1"}, {"shop", "", "p-1"}} {
			mapping, err := mappings.AddExternalID(ctx, other.ID, triple[0], triple[1], triple[2])
			require.NoError(t, err, "%q is reusable", triple)
			assert.Equal(t, other.ID, mapping.ProductID)
		}
		found, err := mappings.LookupProduct(ctx, "erp", "", "P-1")
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, other.ID, found.ID, "the inactive product is found")

		// A mapping on the deleted product is refused.
		_, err = mappings.AddExternalID(ctx, target.ID, "erp", "", "after-delete")
		require.ErrorIs(t, err, apperrors.ErrNotFound)
		assert.Zero(t, rawProductMappings(t, db, target.ID))
	})

	t.Run("a missing or deleted product writes nothing", func(t *testing.T) {
		before := rawProductMappings(t, db, other.ID)
		for _, id := range []uint{target.ID, 99999999} {
			var deleteErr error
			statements := rec.capture(func() { deleteErr = products.Delete(ctx, id) })
			require.ErrorIs(t, deleteErr, apperrors.ErrProductNotFound, "product %d", id)
			for _, statement := range statements {
				assert.NotEqual(t, "delete", statement.kind, "no write for product %d: %+v", id, statement)
			}
		}
		assert.Equal(t, before, rawProductMappings(t, db, other.ID))
		assert.False(t, productDeleted(t, db, other.ID))
	})
}
