package models

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #239: product_external_ids on SQLite, created by MigrateDatabase.

func openProductExternalIDDB(t *testing.T) {
	t.Helper()
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "product-external-ids.db"),
	}))
	require.NoError(t, MigrateDatabase())
}

func assertProductExternalIDSchemaSQLite(t *testing.T) {
	t.Helper()
	require.True(t, DB.Migrator().HasTable("product_external_ids"))

	var columns []sqliteColumn
	require.NoError(t, DB.Raw("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info('product_external_ids')").Scan(&columns).Error)
	byName := map[string]sqliteColumn{}
	names := []string{}
	for _, column := range columns {
		byName[column.Name] = column
		names = append(names, column.Name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"created_at", "external_id", "id", "product_id", "scope", "source", "updated_at"}, names,
		"exactly these columns, no deleted_at")

	assert.Equal(t, 1, byName["id"].PK)
	assert.Equal(t, 1, byName["product_id"].NotNull, "product_id is NOT NULL")
	for name, typ := range map[string]string{"source": "varchar(64)", "scope": "varchar(128)", "external_id": "varchar(255)"} {
		assert.Equal(t, typ, strings.ToLower(byName[name].Type), name)
		assert.Equal(t, 1, byName[name].NotNull, "%s is NOT NULL", name)
	}
	require.NotNil(t, byName["scope"].Dflt, "scope has a default")
	assert.Equal(t, "", strings.Trim(*byName["scope"].Dflt, `'"`), "scope defaults to ''")
	assert.Nil(t, byName["source"].Dflt)
	assert.Nil(t, byName["external_id"].Dflt)

	var fks []struct {
		Table string `gorm:"column:table"`
		From  string `gorm:"column:from"`
		To    string `gorm:"column:to"`
	}
	require.NoError(t, DB.Raw("SELECT \"table\", \"from\", \"to\" FROM pragma_foreign_key_list('product_external_ids')").Scan(&fks).Error)
	require.Len(t, fks, 1)
	assert.Equal(t, "products", fks[0].Table)
	assert.Equal(t, "product_id", fks[0].From)
	assert.Equal(t, "id", fks[0].To)
	assert.Contains(t, tableSQL(t, "product_external_ids"), "fk_product_external_ids_product",
		"the foreign key carries the name the SQL migration uses")

	var indexes []sqliteIndex
	require.NoError(t, DB.Raw("SELECT name, \"unique\", origin FROM pragma_index_list('product_external_ids')").Scan(&indexes).Error)
	unique := []string{}
	all := map[string]sqliteIndex{}
	for _, index := range indexes {
		all[index.Name] = index
		if index.Unique == 1 {
			unique = append(unique, index.Name)
		}
	}
	assert.Equal(t, []string{"idx_product_external_ids_identity"}, unique, "the identity index is the only unique index")
	assert.Equal(t, []string{"source", "scope", "external_id"}, customerExternalIDIndexColumns(t, "idx_product_external_ids_identity"))
	require.Contains(t, all, "idx_product_external_ids_product_id")
	assert.Equal(t, 0, all["idx_product_external_ids_product_id"].Unique, "the product index is not unique")
	assert.Equal(t, []string{"product_id"}, customerExternalIDIndexColumns(t, "idx_product_external_ids_product_id"))
}

func TestProductExternalIDSchemaSQLite(t *testing.T) {
	openProductExternalIDDB(t)
	assertProductExternalIDSchemaSQLite(t)

	product := &Product{Name: "Widget", UnitPriceCents: 100, Currency: "EUR", IsActive: true}
	require.NoError(t, DB.Create(product).Error)

	first := &ProductExternalID{ProductID: product.ID, Source: "ERP-A", Scope: "", ExternalID: "M1"}
	require.NoError(t, DB.Create(first).Error)
	assert.NotZero(t, first.ID)

	err := DB.Create(&ProductExternalID{ProductID: product.ID, Source: "ERP-A", Scope: "", ExternalID: "M1"}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNIQUE constraint failed")

	// Triples that differ only in letter case, or in scope, are distinct.
	require.NoError(t, DB.Create(&ProductExternalID{ProductID: product.ID, Source: "erp-a", Scope: "", ExternalID: "m1"}).Error)
	require.NoError(t, DB.Create(&ProductExternalID{ProductID: product.ID, Source: "ERP-A", Scope: "", ExternalID: "m1"}).Error)
	require.NoError(t, DB.Create(&ProductExternalID{ProductID: product.ID, Source: "ERP-A", Scope: "Plant", ExternalID: "M1"}).Error)
	require.NoError(t, DB.Create(&ProductExternalID{ProductID: product.ID, Source: "ERP-A", Scope: "plant", ExternalID: "M1"}).Error)
	var count int64
	require.NoError(t, DB.Model(&ProductExternalID{}).Count(&count).Error)
	assert.EqualValues(t, 5, count)

	require.NoError(t, DB.Exec("INSERT INTO product_external_ids (product_id, source, external_id) VALUES (?, 'raw', 'r1')", product.ID).Error)
	var scope string
	require.NoError(t, DB.Raw("SELECT scope FROM product_external_ids WHERE source = 'raw'").Scan(&scope).Error)
	assert.Equal(t, "", scope)

	err = DB.Create(&ProductExternalID{ProductID: product.ID + 1000, Source: "s", ExternalID: "orphan"}).Error
	require.Error(t, err, "a mapping to a product that does not exist is refused")

	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t))
}

// TestProductExternalIDSQLiteUpgrade covers two databases migrated before the
// change: the v1.2.0 fixture (no products table either) and a current schema
// that already holds products but lacks product_external_ids.
func TestProductExternalIDSQLiteUpgrade(t *testing.T) {
	t.Run("v1.2.0 fixture", func(t *testing.T) {
		openV120Fixture(t)
		require.False(t, DB.Migrator().HasTable("product_external_ids"), "the fixture predates the table")

		require.NoError(t, MigrateDatabase())
		assertUpgradedV120(t)
		assertProductExternalIDSchemaSQLite(t)

		schema := schemaSnapshot(t)
		require.NoError(t, MigrateDatabase())
		assert.Equal(t, schema, schemaSnapshot(t), "the second MigrateDatabase leaves the schema alone")
	})

	t.Run("schema holding products", func(t *testing.T) {
		openProductExternalIDDB(t)
		products := []Product{
			{Name: "Alpha", SKU: "A-1", UnitPriceCents: 1000, Currency: "EUR", IsActive: true},
			{Name: "Beta", SKU: "A-1", UnitPriceCents: 0, Currency: "USD", IsActive: false},
		}
		require.NoError(t, DB.Create(&products).Error)
		require.NoError(t, DB.Delete(&products[1]).Error)
		// The state before #239: the table does not exist yet.
		require.NoError(t, DB.Migrator().DropTable("product_external_ids"))
		require.False(t, DB.Migrator().HasTable("product_external_ids"))

		type productRow struct {
			ID             uint
			Name           string
			SKU            string `gorm:"column:sku"`
			UnitPriceCents int64
			Currency       string
			IsActive       bool
			Deleted        bool
		}
		const selectRows = "id, name, sku, unit_price_cents, currency, is_active, deleted_at IS NOT NULL AS deleted"
		var before []productRow
		require.NoError(t, DB.Table("products").Select(selectRows).Order("id").Scan(&before).Error)
		require.Len(t, before, 2)

		require.NoError(t, MigrateDatabase())
		assertProductExternalIDSchemaSQLite(t)

		var after []productRow
		require.NoError(t, DB.Table("products").Select(selectRows).Order("id").Scan(&after).Error)
		assert.True(t, after[1].Deleted, "the soft-deleted product stays soft-deleted")
		assert.Equal(t, before, after, "the products are unchanged")

		require.NoError(t, DB.Create(&ProductExternalID{ProductID: products[0].ID, Source: "erp", ExternalID: "P-1"}).Error)
		var count int64
		require.NoError(t, DB.Model(&ProductExternalID{}).Count(&count).Error)
		assert.EqualValues(t, 1, count)
	})
}

func TestProductExternalIDJSON(t *testing.T) {
	created := time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)
	mapping := ProductExternalID{
		ID: 9, CreatedAt: created, UpdatedAt: created, ProductID: 42,
		Product: &Product{Name: "Hidden product", SKU: "HIDDEN-SKU"},
		Source:  "erp", Scope: "plant-01", ExternalID: "MAT-100042",
	}
	raw, err := json.Marshal(mapping)
	require.NoError(t, err)

	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	keys := make([]string, 0, len(decoded))
	for key := range decoded {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"created_at", "external_id", "id", "product_id", "scope", "source", "updated_at"}, keys,
		"the mapping carries exactly these fields")
	assert.EqualValues(t, 9, decoded["id"])
	assert.EqualValues(t, 42, decoded["product_id"])
	assert.Equal(t, "erp", decoded["source"])
	assert.Equal(t, "plant-01", decoded["scope"])
	assert.Equal(t, "MAT-100042", decoded["external_id"])
	assert.Equal(t, "2026-11-12T09:30:00Z", decoded["created_at"])
	assert.Equal(t, "2026-11-12T09:30:00Z", decoded["updated_at"])
	assert.NotContains(t, string(raw), "Hidden product", "the product is never embedded")
	assert.NotContains(t, string(raw), "\"product\"")

	raw, err = json.Marshal(ProductExternalID{Source: "erp", ExternalID: "1"})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"scope":""`)
}
