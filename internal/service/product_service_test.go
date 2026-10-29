package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// productCurrencySource is the one configuration read the catalogue makes.
type productCurrencySource struct {
	value string
	err   error
}

func (s productCurrencySource) GetString(string) (string, error) { return s.value, s.err }

func newProductServiceUnderTest(t *testing.T, source DealCurrencySource) (ProductService, *mocks.ProductRepository) {
	t.Helper()
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	repo := new(mocks.ProductRepository)
	return NewProductService(repo, source), repo
}

// The currency defaults to deals.default_currency the way a deal's does: the
// configured code when it is three upper-case letters, the shipped EUR when
// the setting is unreadable or malformed, and an explicit code always wins.
func TestProductService_CurrencyDefaultComesFromConfiguration(t *testing.T) {
	for name, tc := range map[string]struct {
		source DealCurrencySource
		want   string
	}{
		"configured":    {productCurrencySource{value: "ron"}, "RON"},
		"unreadable":    {productCurrencySource{err: errors.New("no row")}, models.DealDefaultCurrency},
		"malformed":     {productCurrencySource{value: "EURO"}, models.DealDefaultCurrency},
		"no source":     {nil, models.DealDefaultCurrency},
		"blank setting": {productCurrencySource{value: "  "}, models.DealDefaultCurrency},
	} {
		t.Run(name, func(t *testing.T) {
			svc, repo := newProductServiceUnderTest(t, tc.source)
			repo.On("Create", mock.Anything).Return(nil).Once()
			product := &models.Product{Name: " Widget ", UnitPriceCents: 100, IsActive: true}
			require.NoError(t, svc.Create(product))
			assert.Equal(t, tc.want, product.Currency)
			assert.Equal(t, "Widget", product.Name, "the name is trimmed")
			repo.AssertExpectations(t)
		})
	}
	svc, repo := newProductServiceUnderTest(t, productCurrencySource{value: "RON"})
	repo.On("Create", mock.Anything).Return(nil).Once()
	product := &models.Product{Name: "Widget", UnitPriceCents: 100, Currency: "USD"}
	require.NoError(t, svc.Create(product))
	assert.Equal(t, "USD", product.Currency, "an explicit currency wins")
}

// Every domain rule is checked before the repository is reached.
func TestProductService_ValidationFailuresNeverReachTheWrite(t *testing.T) {
	for name, product := range map[string]*models.Product{
		"blank name":            {Name: "   ", UnitPriceCents: 1},
		"name too long":         {Name: strings.Repeat("n", models.ProductNameMaxLength+1), UnitPriceCents: 1},
		"sku too long":          {Name: "X", SKU: strings.Repeat("s", models.ProductSKUMaxLength+1), UnitPriceCents: 1},
		"description too long":  {Name: "X", Description: strings.Repeat("d", models.ProductDescriptionMaxBytes+1), UnitPriceCents: 1},
		"negative price":        {Name: "X", UnitPriceCents: -1},
		"price above the bound": {Name: "X", UnitPriceCents: models.QuoteAmountCentsMax + 1},
		"lowercase currency":    {Name: "X", UnitPriceCents: 1, Currency: "eur"},
		"two-letter currency":   {Name: "X", UnitPriceCents: 1, Currency: "EU"},
	} {
		t.Run("create: "+name, func(t *testing.T) {
			svc, repo := newProductServiceUnderTest(t, nil)
			err := svc.Create(product)
			require.Error(t, err)
			assert.True(t, errors.Is(err, apperrors.ErrValidation), "%v", err)
			repo.AssertNotCalled(t, "Create", mock.Anything)
		})
		t.Run("update: "+name, func(t *testing.T) {
			svc, repo := newProductServiceUnderTest(t, nil)
			stored := *product
			stored.ID = 7
			repo.On("GetByID", uint(7)).Return(&models.Product{BaseModel: models.BaseModel{ID: 7}, Name: "Old", Currency: "EUR"}, nil).Once()
			err := svc.Update(&stored)
			require.Error(t, err)
			assert.True(t, errors.Is(err, apperrors.ErrValidation), "%v", err)
			repo.AssertNotCalled(t, "Update", mock.Anything)
		})
	}

	// The bound itself and a free item are fine.
	svc, repo := newProductServiceUnderTest(t, nil)
	repo.On("Create", mock.Anything).Return(nil).Twice()
	require.NoError(t, svc.Create(&models.Product{Name: "At the bound", UnitPriceCents: models.QuoteAmountCentsMax, Currency: "EUR"}))
	require.NoError(t, svc.Create(&models.Product{Name: "Free", UnitPriceCents: 0, Currency: "EUR"}))
	repo.AssertExpectations(t)
}

// A missing product is the product sentinel on get, update and delete; the
// list never returns a nil slice.
func TestProductService_NotFoundAndList(t *testing.T) {
	svc, repo := newProductServiceUnderTest(t, nil)
	repo.On("GetByID", uint(9)).Return(nil, gorm.ErrRecordNotFound)
	repo.On("Delete", uint(9)).Return(gorm.ErrRecordNotFound)
	repo.On("List", 0, 20, repository.ProductListFilter{}).Return(nil, int64(0), nil)

	_, err := svc.GetByID(9)
	assert.True(t, errors.Is(err, apperrors.ErrProductNotFound), "%v", err)
	err = svc.Update(&models.Product{BaseModel: models.BaseModel{ID: 9}, Name: "X", UnitPriceCents: 1, Currency: "EUR"})
	assert.True(t, errors.Is(err, apperrors.ErrProductNotFound), "%v", err)
	repo.AssertNotCalled(t, "Update", mock.Anything)
	err = svc.Delete(9)
	assert.True(t, errors.Is(err, apperrors.ErrProductNotFound), "%v", err)

	products, total, err := svc.List(0, 20, repository.ProductListFilter{})
	require.NoError(t, err)
	assert.NotNil(t, products)
	assert.Empty(t, products)
	assert.Zero(t, total)
	repo.AssertExpectations(t)
}
