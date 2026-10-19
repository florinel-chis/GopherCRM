package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomerWebsiteUpgradeFromSQLiteFixture(t *testing.T) {
	// A frozen, populated schema without website; never generated from current models.
	openV120Fixture(t)
	require.False(t, DB.Migrator().HasColumn(&Customer{}, "website"))
	require.NoError(t, MigrateDatabase())
	var customers []Customer
	require.NoError(t, DB.Find(&customers).Error)
	require.NotEmpty(t, customers)
	for _, customer := range customers {
		assert.Empty(t, customer.Website)
	}
	id := customers[0].ID
	require.NoError(t, DB.Model(&Customer{}).Where("id = ?", id).Update("website", "https://example.com").Error)
	before := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, before, schemaSnapshot(t))
	var stored Customer
	require.NoError(t, DB.First(&stored, id).Error)
	assert.Equal(t, "https://example.com", stored.Website)
	assert.Equal(t, customers[0].Email, stored.Email)
}
