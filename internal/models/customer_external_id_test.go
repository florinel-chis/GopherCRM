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

// #212: customer_external_ids on SQLite, created by MigrateDatabase.

// openCustomerExternalIDDB opens a fresh SQLite file through the production
// connector (foreign keys on) and runs MigrateDatabase.
func openCustomerExternalIDDB(t *testing.T) {
	t.Helper()
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "external-ids.db"),
	}))
	require.NoError(t, MigrateDatabase())
}

type sqliteColumn struct {
	Name    string  `gorm:"column:name"`
	Type    string  `gorm:"column:type"`
	NotNull int     `gorm:"column:notnull"`
	Dflt    *string `gorm:"column:dflt_value"`
	PK      int     `gorm:"column:pk"`
}

type sqliteIndex struct {
	Name   string `gorm:"column:name"`
	Unique int    `gorm:"column:unique"`
	Origin string `gorm:"column:origin"`
}

func customerExternalIDIndexColumns(t *testing.T, index string) []string {
	t.Helper()
	var columns []string
	require.NoError(t, DB.Raw("SELECT name FROM pragma_index_info(?) ORDER BY seqno", index).Scan(&columns).Error)
	return columns
}

func assertCustomerExternalIDSchemaSQLite(t *testing.T) {
	t.Helper()
	require.True(t, DB.Migrator().HasTable("customer_external_ids"))

	var columns []sqliteColumn
	require.NoError(t, DB.Raw("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info('customer_external_ids')").Scan(&columns).Error)
	byName := map[string]sqliteColumn{}
	names := []string{}
	for _, column := range columns {
		byName[column.Name] = column
		names = append(names, column.Name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"created_at", "customer_id", "external_id", "id", "scope", "source", "updated_at"}, names,
		"exactly these columns, no deleted_at")

	assert.Equal(t, 1, byName["id"].PK)
	assert.Equal(t, 1, byName["customer_id"].NotNull, "customer_id is NOT NULL")
	for name, typ := range map[string]string{"source": "varchar(64)", "scope": "varchar(128)", "external_id": "varchar(255)"} {
		assert.Equal(t, typ, strings.ToLower(byName[name].Type), name)
		assert.Equal(t, 1, byName[name].NotNull, "%s is NOT NULL", name)
	}
	require.NotNil(t, byName["scope"].Dflt, "scope has a default")
	assert.NotEmpty(t, *byName["scope"].Dflt, "scope has a declared default")
	assert.Equal(t, "", strings.Trim(*byName["scope"].Dflt, `'"`), "scope defaults to ''")
	assert.Nil(t, byName["source"].Dflt)
	assert.Nil(t, byName["external_id"].Dflt)

	// The foreign key to customers.id.
	var fks []struct {
		Table string `gorm:"column:table"`
		From  string `gorm:"column:from"`
		To    string `gorm:"column:to"`
	}
	require.NoError(t, DB.Raw("SELECT \"table\", \"from\", \"to\" FROM pragma_foreign_key_list('customer_external_ids')").Scan(&fks).Error)
	require.Len(t, fks, 1)
	assert.Equal(t, "customers", fks[0].Table)
	assert.Equal(t, "customer_id", fks[0].From)
	assert.Equal(t, "id", fks[0].To)

	var indexes []sqliteIndex
	require.NoError(t, DB.Raw("SELECT name, \"unique\", origin FROM pragma_index_list('customer_external_ids')").Scan(&indexes).Error)
	unique := []string{}
	all := map[string]sqliteIndex{}
	for _, index := range indexes {
		all[index.Name] = index
		if index.Unique == 1 {
			unique = append(unique, index.Name)
		}
	}
	assert.Equal(t, []string{"idx_customer_external_ids_identity"}, unique, "the identity index is the only unique index")
	assert.Equal(t, []string{"source", "scope", "external_id"}, customerExternalIDIndexColumns(t, "idx_customer_external_ids_identity"))
	require.Contains(t, all, "idx_customer_external_ids_customer_id")
	assert.Equal(t, 0, all["idx_customer_external_ids_customer_id"].Unique, "the customer index is not unique")
	assert.Equal(t, []string{"customer_id"}, customerExternalIDIndexColumns(t, "idx_customer_external_ids_customer_id"))
}

func TestCustomerExternalIDSchemaSQLite(t *testing.T) {
	openCustomerExternalIDDB(t)
	assertCustomerExternalIDSchemaSQLite(t)

	customer := &Customer{FirstName: "Ext", LastName: "Id", Email: "ext-schema@example.com"}
	require.NoError(t, DB.Create(customer).Error)

	first := &CustomerExternalID{CustomerID: customer.ID, Source: "Shop-A", Scope: "", ExternalID: "X1"}
	require.NoError(t, DB.Create(first).Error)
	assert.NotZero(t, first.ID)

	// The same triple twice is refused by the unique index.
	err := DB.Create(&CustomerExternalID{CustomerID: customer.ID, Source: "Shop-A", Scope: "", ExternalID: "X1"}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNIQUE constraint failed")

	// Triples that differ only in letter case are distinct identities.
	require.NoError(t, DB.Create(&CustomerExternalID{CustomerID: customer.ID, Source: "shop-a", Scope: "", ExternalID: "x1"}).Error)
	require.NoError(t, DB.Create(&CustomerExternalID{CustomerID: customer.ID, Source: "Shop-A", Scope: "", ExternalID: "x1"}).Error)
	require.NoError(t, DB.Create(&CustomerExternalID{CustomerID: customer.ID, Source: "Shop-A", Scope: "Site", ExternalID: "X1"}).Error)
	require.NoError(t, DB.Create(&CustomerExternalID{CustomerID: customer.ID, Source: "Shop-A", Scope: "site", ExternalID: "X1"}).Error)
	var count int64
	require.NoError(t, DB.Model(&CustomerExternalID{}).Count(&count).Error)
	assert.EqualValues(t, 5, count)

	// The scope column default applies to a raw insert that omits it.
	require.NoError(t, DB.Exec("INSERT INTO customer_external_ids (customer_id, source, external_id) VALUES (?, 'raw', 'r1')", customer.ID).Error)
	var scope string
	require.NoError(t, DB.Raw("SELECT scope FROM customer_external_ids WHERE source = 'raw'").Scan(&scope).Error)
	assert.Equal(t, "", scope)

	// The foreign key is enforced.
	err = DB.Create(&CustomerExternalID{CustomerID: customer.ID + 1000, Source: "s", ExternalID: "orphan"}).Error
	require.Error(t, err, "a mapping to a customer that does not exist is refused")

	// A second MigrateDatabase leaves the schema alone.
	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t))
}

// TestCustomerExternalIDSQLiteUpgrade starts from the v1.2.0 fixture, which
// predates the table: the next MigrateDatabase creates it.
func TestCustomerExternalIDSQLiteUpgrade(t *testing.T) {
	openV120Fixture(t)
	require.False(t, DB.Migrator().HasTable("customer_external_ids"), "the fixture predates the table")

	require.NoError(t, MigrateDatabase())
	assertUpgradedV120(t)
	assertCustomerExternalIDSchemaSQLite(t)

	// The table takes a mapping of an existing customer.
	var customerID uint
	require.NoError(t, DB.Raw("SELECT id FROM customers ORDER BY id LIMIT 1").Scan(&customerID).Error)
	require.NotZero(t, customerID)
	require.NoError(t, DB.Create(&CustomerExternalID{CustomerID: customerID, Source: "erp", ExternalID: "C-1"}).Error)

	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t), "the second MigrateDatabase leaves the schema alone")
}

func TestCustomerExternalIDJSON(t *testing.T) {
	created := time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC)
	mapping := CustomerExternalID{
		ID: 9, CreatedAt: created, UpdatedAt: created, CustomerID: 42,
		Customer: &Customer{FirstName: "Hidden", Email: "hidden@example.com"},
		Source:   "shopify", Scope: "store-eu", ExternalID: "gid-1001",
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
	assert.Equal(t, []string{"created_at", "customer_id", "external_id", "id", "scope", "source", "updated_at"}, keys,
		"the mapping carries exactly these fields")
	assert.EqualValues(t, 9, decoded["id"])
	assert.EqualValues(t, 42, decoded["customer_id"])
	assert.Equal(t, "shopify", decoded["source"])
	assert.Equal(t, "store-eu", decoded["scope"])
	assert.Equal(t, "gid-1001", decoded["external_id"])
	assert.Equal(t, "2026-11-11T09:30:00Z", decoded["created_at"])
	assert.NotContains(t, string(raw), "hidden@example.com", "the customer is never embedded")
	assert.NotContains(t, string(raw), "\"customer\"")

	// An empty scope is serialised, not omitted.
	raw, err = json.Marshal(CustomerExternalID{Source: "erp", ExternalID: "1"})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"scope":""`)
}
