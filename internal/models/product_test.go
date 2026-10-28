package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The handler and the service enforce the Product* constants; the migration
// and AutoMigrate enforce the `type:...` tags. This test is what keeps the two
// in step: widen a column and forget the constant, or the other way round, and
// it fails.
func TestProductColumnWidthsMatchConstants(t *testing.T) {
	for _, tc := range []struct {
		field string
		limit int
	}{
		{"Name", ProductNameMaxLength},
		{"SKU", ProductSKUMaxLength},
	} {
		assert.Equal(t, columnSize(t, &Product{}, tc.field), tc.limit, "limit for Product.%s disagrees with its column width", tc.field)
	}
	// char(3) is not a varchar, so it is pinned by its declared type.
	assert.Equal(t, "char(3)", columnType(t, &Product{}, "Currency"))
	assert.Equal(t, 3, ProductCurrencyLength)
	assert.Equal(t, 200, ProductNameMaxLength)
	assert.Equal(t, 64, ProductSKUMaxLength)
	assert.Equal(t, LeadNotesMaxBytes, ProductDescriptionMaxBytes)
}

// The price and the active flag deliberately have no column default: a GORM
// default would turn an explicit 0 or false into the default on create.
func TestProductPriceAndActiveFlagHaveNoColumnDefault(t *testing.T) {
	assert.Empty(t, columnDefault(t, &Product{}, "UnitPriceCents"))
	assert.Empty(t, columnDefault(t, &Product{}, "IsActive"))
}
