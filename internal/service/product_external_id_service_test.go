package service

// Product external identity mappings (#239): validation, the unwired service,
// the unwired product delete and the log lines.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedExternalIDServiceProduct(t *testing.T, db *gorm.DB, label string, active bool) *models.Product {
	t.Helper()
	product := &models.Product{Name: "Svc product " + label, SKU: "SVC-SKU", UnitPriceCents: 250, Currency: "EUR", IsActive: active}
	require.NoError(t, db.Create(product).Error)
	return product
}

// countingProductExternalIDRepo counts every call, so a test can prove the
// service refused before touching its repository.
type countingProductExternalIDRepo struct {
	calls atomic.Int32
}

func (r *countingProductExternalIDRepo) LockLiveProduct(context.Context, uint) error {
	r.calls.Add(1)
	return nil
}

func (r *countingProductExternalIDRepo) Create(context.Context, *models.ProductExternalID) error {
	r.calls.Add(1)
	return nil
}

func (r *countingProductExternalIDRepo) FindProductByIdentity(context.Context, string, string, string) (*models.Product, error) {
	r.calls.Add(1)
	return nil, nil
}

func (r *countingProductExternalIDRepo) DeleteForProduct(context.Context, uint, uint) error {
	r.calls.Add(1)
	return nil
}

func (r *countingProductExternalIDRepo) PurgeForProduct(context.Context, uint) (int64, error) {
	r.calls.Add(1)
	return 0, nil
}

func (r *countingProductExternalIDRepo) WithTx(*gorm.DB) repository.ProductExternalIDRepository {
	return r
}

func TestProductExternalIDValidation(t *testing.T) {
	ok := func(source, scope, externalID string) {
		t.Helper()
		assert.NoError(t, ValidateProductExternalIdentity(source, scope, externalID), "%q %q %q", source, scope, externalID)
	}
	ok("erp", "", "1")
	ok("erp", "plant-01", "MAT-100042")
	ok(strings.Repeat("s", 64), strings.Repeat("c", 128), strings.Repeat("e", 255))
	ok(strings.Repeat("é", 64), strings.Repeat("ß", 128), strings.Repeat("ü", 255))
	ok("a b", "x y", "inner spaces are kept")
	ok("ERP-A", "", "M1")
	assert.Equal(t, models.ProductExternalIDSourceMaxLength, 64)
	assert.Equal(t, models.ProductExternalIDScopeMaxLength, 128)
	assert.Equal(t, models.ProductExternalIDValueMaxLength, 255)

	cases := []struct {
		name, source, scope, externalID, field, reasonFragment string
	}{
		{"source missing", "", "", "1", "source", "required"},
		{"source too long", strings.Repeat("é", 65), "", "1", "source", "at most 64"},
		{"source leading space", " erpx", "", "1", "source", "whitespace"},
		{"source trailing space", "erpx ", "", "1", "source", "whitespace"},
		{"source tab", "er\tpx", "", "1", "source", "control"},
		{"source invalid utf8", "er\xffpx", "", "1", "source", "UTF-8"},
		{"scope too long", "erp", strings.Repeat("c", 129), "1", "scope", "at most 128"},
		{"scope trailing newline", "erp", "plant\n", "1", "scope", "control"},
		{"scope leading space", "erp", " plant", "1", "scope", "whitespace"},
		{"scope only space", "erp", " ", "1", "scope", "whitespace"},
		{"external_id missing", "erp", "", "", "external_id", "required"},
		{"external_id too long", "erp", "", strings.Repeat("e", 256), "external_id", "at most 255"},
		{"external_id control", "erp", "", "a\x00b", "external_id", "control"},
		{"external_id del", "erp", "", "a\x7fb", "external_id", "control"},
		{"external_id trailing space", "erp", "", "MAT-1 ", "external_id", "whitespace"},
		{"external_id invalid utf8", "erp", "", "\xc3", "external_id", "UTF-8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProductExternalIdentity(tc.source, tc.scope, tc.externalID)
			require.Error(t, err)
			var fieldErr *CustomerExternalIDFieldError
			require.True(t, errors.As(err, &fieldErr), "a *CustomerExternalIDFieldError, got %T", err)
			assert.Equal(t, tc.field, fieldErr.Field)
			assert.Contains(t, fieldErr.Reason, tc.reasonFragment)
			assert.True(t, errors.Is(err, apperrors.ErrValidation), "matches apperrors.ErrValidation")
			for _, value := range []string{tc.source, tc.scope, tc.externalID} {
				if trimmed := strings.TrimSpace(value); len(trimmed) > 2 {
					assert.NotContains(t, err.Error(), trimmed, "the error never carries the value")
					assert.NotContains(t, utils.LogError(err).Error(), trimmed)
				}
			}
		})
	}

	// The service validates before any read or write.
	db := openExternalIDSQLite(t)
	repo := &countingProductExternalIDRepo{}
	statements := countStatements(t, db)
	svc := NewProductExternalIDService(repo, utils.NewTransactionManager(db))
	ctx := context.Background()
	for _, tc := range cases {
		_, err := svc.AddExternalID(ctx, 1, tc.source, tc.scope, tc.externalID)
		assert.True(t, errors.Is(err, apperrors.ErrValidation), "AddExternalID %s: %v", tc.name, err)
		product, err := svc.LookupProduct(ctx, tc.source, tc.scope, tc.externalID)
		assert.Nil(t, product)
		assert.True(t, errors.Is(err, apperrors.ErrValidation), "LookupProduct %s: %v", tc.name, err)
	}
	assert.Zero(t, repo.calls.Load(), "a refused input reaches no repository method")
	assert.Zero(t, statements.Load(), "a refused input runs no statement")
}

func TestProductExternalIDServiceRefusedWhenUnwired(t *testing.T) {
	db := openExternalIDSQLite(t)
	statements := countStatements(t, db)
	repo := &countingProductExternalIDRepo{}
	tm := utils.NewTransactionManager(db)
	ctx := context.Background()

	for name, svc := range map[string]ProductExternalIDService{
		"no repository or manager": NewProductExternalIDService(nil, nil),
		"no manager":               NewProductExternalIDService(repo, nil),
		"no repository":            NewProductExternalIDService(nil, tm),
	} {
		t.Run(name, func(t *testing.T) {
			mapping, err := svc.AddExternalID(ctx, 1, "erp", "", "M1")
			assert.Nil(t, mapping)
			assert.ErrorIs(t, err, ErrProductExternalIDsUnavailable)
			assert.ErrorIs(t, svc.RemoveExternalID(ctx, 1, 1), ErrProductExternalIDsUnavailable)
			product, err := svc.LookupProduct(ctx, "erp", "", "M1")
			assert.Nil(t, product)
			assert.ErrorIs(t, err, ErrProductExternalIDsUnavailable)
		})
	}
	assert.Zero(t, repo.calls.Load(), "the repository is never called")
	assert.Zero(t, statements.Load(), "the database is never touched")
	var mappings int64
	require.NoError(t, db.Model(&models.ProductExternalID{}).Count(&mappings).Error)
	assert.Zero(t, mappings)
}

func TestProductDeleteRefusedWhenUnwired(t *testing.T) {
	ctx := context.Background()

	t.Run("mock repository", func(t *testing.T) {
		db := openExternalIDSQLite(t)
		tm := utils.NewTransactionManager(db)
		ext := &countingProductExternalIDRepo{}
		for name, opts := range map[string][]ProductServiceOption{
			"no option":                  {},
			"nil repository":             {WithProductExternalIDs(nil, tm)},
			"nil transaction manager":    {WithProductExternalIDs(ext, nil)},
			"nil repository and manager": {WithProductExternalIDs(nil, nil)},
		} {
			t.Run(name, func(t *testing.T) {
				repo := new(mocks.ProductRepository)
				svc := NewProductService(repo, nil, opts...)
				err := svc.Delete(ctx, 7)
				assert.ErrorIs(t, err, ErrProductExternalIDsUnavailable)
				assert.False(t, errors.Is(err, apperrors.ErrProductNotFound))
				repo.AssertNotCalled(t, "Delete", mock.Anything)
				repo.AssertNotCalled(t, "WithTx", mock.Anything)
			})
		}
		assert.Zero(t, ext.calls.Load(), "the mapping repository is never called")
	})

	t.Run("real schema", func(t *testing.T) {
		db := openExternalIDSQLite(t)
		product := seedExternalIDServiceProduct(t, db, "unwired", true)
		require.NoError(t, db.Create(&models.ProductExternalID{ProductID: product.ID, Source: "erp", ExternalID: "U1"}).Error)
		statements := countStatements(t, db)

		svc := NewProductService(repository.NewProductRepository(db), nil)
		require.ErrorIs(t, svc.Delete(ctx, product.ID), ErrProductExternalIDsUnavailable)
		assert.Zero(t, statements.Load(), "nothing is read or written")

		var live models.Product
		require.NoError(t, db.First(&live, product.ID).Error, "the product is still live")
		var mappings int64
		require.NoError(t, db.Model(&models.ProductExternalID{}).Where("product_id = ?", product.ID).Count(&mappings).Error)
		assert.EqualValues(t, 1, mappings, "the mapping is kept")
	})
}

func TestProductExternalIDLogsOmitValues(t *testing.T) {
	var buf bytes.Buffer
	previousLevel, previousOut, previousFormatter := utils.Logger.GetLevel(), utils.Logger.Out, utils.Logger.Formatter
	utils.Logger.SetOutput(&buf)
	utils.Logger.SetLevel(logrus.DebugLevel)
	utils.Logger.SetFormatter(&logrus.JSONFormatter{})
	t.Cleanup(func() {
		utils.Logger.SetOutput(previousOut)
		utils.Logger.SetLevel(previousLevel)
		utils.Logger.SetFormatter(previousFormatter)
	})

	db := openExternalIDSQLite(t)
	tm := utils.NewTransactionManager(db)
	extRepo := repository.NewProductExternalIDRepository(db)
	svc := NewProductExternalIDService(extRepo, tm)
	products := NewProductService(repository.NewProductRepository(db), nil, WithProductExternalIDs(extRepo, tm))
	ctx := context.Background()
	a := seedExternalIDServiceProduct(t, db, "logs-a", true)
	b := seedExternalIDServiceProduct(t, db, "logs-b", true)

	const (
		source     = "CanarySourceQ7"
		scope      = "CanaryScopeZ9"
		externalID = "CanaryExternalK3"
	)

	mapping, err := svc.AddExternalID(ctx, a.ID, source, scope, externalID)
	require.NoError(t, err)
	_, err = svc.AddExternalID(ctx, a.ID, source, scope, externalID)
	require.ErrorIs(t, err, apperrors.ErrProductExternalIDTaken)
	_, err = svc.AddExternalID(ctx, b.ID, source, scope, externalID)
	require.ErrorIs(t, err, apperrors.ErrProductExternalIDTaken)
	_, err = svc.AddExternalID(ctx, 99999999, source, scope, externalID+"Other")
	require.ErrorIs(t, err, apperrors.ErrNotFound)
	_, err = svc.AddExternalID(ctx, a.ID, source+" ", scope, externalID)
	require.ErrorIs(t, err, apperrors.ErrValidation)
	_, err = svc.AddExternalID(ctx, a.ID, source, scope+"\n", externalID)
	require.ErrorIs(t, err, apperrors.ErrValidation)
	_, err = svc.AddExternalID(ctx, a.ID, source, scope, externalID+strings.Repeat("x", 300))
	require.ErrorIs(t, err, apperrors.ErrValidation)

	found, err := svc.LookupProduct(ctx, source, scope, externalID)
	require.NoError(t, err)
	require.NotNil(t, found)
	found, err = svc.LookupProduct(ctx, source, scope, externalID+"Miss")
	require.NoError(t, err)
	require.Nil(t, found)
	_, err = svc.LookupProduct(ctx, "", scope, externalID)
	require.ErrorIs(t, err, apperrors.ErrValidation)
	_, err = svc.LookupProduct(ctx, source, scope, " "+externalID)
	require.ErrorIs(t, err, apperrors.ErrValidation)

	require.ErrorIs(t, svc.RemoveExternalID(ctx, b.ID, mapping.ID), apperrors.ErrNotFound)
	require.NoError(t, svc.RemoveExternalID(ctx, a.ID, mapping.ID))

	// The product delete that purges a mapping, then a delete of a missing one.
	_, err = svc.AddExternalID(ctx, b.ID, source, scope, externalID+"Purged")
	require.NoError(t, err)
	require.NoError(t, products.Delete(ctx, b.ID))
	require.ErrorIs(t, products.Delete(ctx, b.ID), apperrors.ErrProductNotFound)

	output := buf.String()
	require.NotEmpty(t, output, "the service paths wrote log lines")
	for _, op := range []string{"AddExternalID", "RemoveExternalID", "LookupProduct", "Delete"} {
		assert.Contains(t, output, op, "the operation is named")
	}
	assert.Contains(t, output, fmt.Sprintf(`"product_id":%d`, a.ID))
	assert.Contains(t, output, fmt.Sprintf(`"mapping_id":%d`, mapping.ID))
	assert.Contains(t, output, `"mappings_removed":1`)
	assert.NotContains(t, strings.ToLower(output), "canary", "no log line carries a submitted or stored value")
}
