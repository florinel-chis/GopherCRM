package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSort_ValidColumns(t *testing.T) {
	tests := []struct {
		name      string
		entity    string
		sortBy    string
		sortOrder string
		wantCol   string
		wantDir   string
	}{
		{"users by email asc", "users", "email", "asc", "email", "asc"},
		{"users by created_at desc", "users", "created_at", "desc", "created_at", "desc"},
		{"leads by company asc", "leads", "company", "asc", "company", "asc"},
		{"customers by last_name desc", "customers", "last_name", "desc", "last_name", "desc"},
		{"tickets by priority asc", "tickets", "priority", "asc", "priority", "asc"},
		{"tasks by due_date desc", "tasks", "due_date", "desc", "due_date", "desc"},
		{"companies by domain asc", "companies", "domain", "asc", "domain", "asc"},
		{"companies by name desc", "companies", "name", "desc", "name", "desc"},
		{"deals by amount asc", "deals", "amount_cents", "asc", "amount_cents", "asc"},
		{"deals by expected close desc", "deals", "expected_close_date", "desc", "expected_close_date", "desc"},
		{"deals by closed_at asc", "deals", "closed_at", "asc", "closed_at", "asc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			col, dir, err := ValidateSort(tt.entity, tt.sortBy, tt.sortOrder)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCol, col)
			assert.Equal(t, tt.wantDir, dir)
		})
	}
}

func TestValidateSort_EmptySortBy_ReturnsDefault(t *testing.T) {
	col, dir, err := ValidateSort("users", "", "asc")
	require.NoError(t, err)
	assert.Equal(t, "created_at", col)
	assert.Equal(t, "desc", dir)
}

func TestValidateSort_InvalidColumn_ReturnsError(t *testing.T) {
	_, _, err := ValidateSort("users", "nonexistent", "asc")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid sort column")

	// The companies allowlist is the documented columns plus id: the owner
	// and phone columns exist but are not sortable, nor are the computed counts.
	for _, column := range []string{"owner_id", "phone", "customer_count", "lead_count"} {
		_, _, err := ValidateSort("companies", column, "asc")
		assert.Errorf(t, err, "%q must not be a sortable company column", column)
	}

	// Deals: the allowlist is exactly the documented columns plus id. The
	// links and the text columns exist but are not sortable.
	for _, column := range []string{"id", "title", "stage", "amount_cents", "probability", "expected_close_date", "closed_at", "created_at", "updated_at"} {
		_, _, err := ValidateSort("deals", column, "asc")
		assert.NoErrorf(t, err, "%q is a sortable deal column", column)
	}
	for _, column := range []string{"owner_id", "company_id", "customer_id", "lead_id", "currency", "notes", "lost_reason"} {
		_, _, err := ValidateSort("deals", column, "asc")
		assert.Errorf(t, err, "%q must not be a sortable deal column", column)
	}
}

// Products and quotes: the allowlist is exactly the documented columns. The
// links, the free text and the other money columns exist but are not sortable.
func TestValidateSortProductsAndQuotes(t *testing.T) {
	for _, column := range []string{"id", "name", "sku", "unit_price_cents", "currency", "is_active", "created_at", "updated_at"} {
		col, dir, err := ValidateSort("products", column, "asc")
		require.NoErrorf(t, err, "%q is a sortable product column", column)
		assert.Equal(t, column, col)
		assert.Equal(t, "asc", dir)
	}
	for _, column := range []string{"description", "deleted_at", "owner_id", "quote_id", "title", "status", "total_cents", "price"} {
		_, _, err := ValidateSort("products", column, "asc")
		assert.Errorf(t, err, "%q must not be a sortable product column", column)
	}

	for _, column := range []string{"id", "title", "status", "total_cents", "currency", "valid_until", "sent_at", "created_at", "updated_at"} {
		col, dir, err := ValidateSort("quotes", column, "desc")
		require.NoErrorf(t, err, "%q is a sortable quote column", column)
		assert.Equal(t, column, col)
		assert.Equal(t, "desc", dir)
	}
	for _, column := range []string{"subtotal_cents", "discount_cents", "notes", "closed_at", "last_emailed_at", "deal_id", "customer_id", "owner_id", "name", "sku"} {
		_, _, err := ValidateSort("quotes", column, "asc")
		assert.Errorf(t, err, "%q must not be a sortable quote column", column)
	}

	for _, entity := range []string{"products", "quotes"} {
		_, _, err := ValidateSort(entity, "id; DROP TABLE quotes;--", "asc")
		assert.Error(t, err, entity)
		col, dir, err := ValidateSort(entity, "", "")
		require.NoError(t, err)
		assert.Equal(t, "created_at", col, "the default sort is newest first")
		assert.Equal(t, "desc", dir)
	}
}

func TestValidateSort_SQLInjection_ReturnsError(t *testing.T) {
	injections := []string{
		"id; DROP TABLE users;--",
		"id OR 1=1",
		"SLEEP(5)",
		"id,(SELECT password FROM users LIMIT 1)",
		"1; UPDATE users SET role='admin'--",
		"created_at DESC; DROP TABLE users;--",
	}

	for _, payload := range injections {
		t.Run(payload, func(t *testing.T) {
			_, _, err := ValidateSort("users", payload, "asc")
			assert.Error(t, err, "SQL injection payload should be rejected: %s", payload)
		})
	}
}

func TestValidateSort_InvalidSortOrder_DefaultsToDesc(t *testing.T) {
	tests := []string{"invalid", "ASC; DROP TABLE", "1", "", "ascending"}
	for _, order := range tests {
		t.Run(order, func(t *testing.T) {
			_, dir, err := ValidateSort("users", "email", order)
			require.NoError(t, err)
			assert.Equal(t, "desc", dir)
		})
	}
}

func TestValidateSort_SortOrderCaseInsensitive(t *testing.T) {
	_, dir, err := ValidateSort("users", "email", "ASC")
	require.NoError(t, err)
	assert.Equal(t, "asc", dir)

	_, dir, err = ValidateSort("users", "email", "DESC")
	require.NoError(t, err)
	assert.Equal(t, "desc", dir)
}

func TestValidateSort_UnknownEntity_ReturnsError(t *testing.T) {
	_, _, err := ValidateSort("unknown_entity", "id", "asc")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown entity")
}

func TestSafeOrderClause_ValidInput(t *testing.T) {
	clause, err := SafeOrderClause("users", "email", "asc")
	require.NoError(t, err)
	assert.Equal(t, "email asc", clause)
}

func TestSafeOrderClause_EmptySortBy_ReturnsEmpty(t *testing.T) {
	clause, err := SafeOrderClause("users", "", "asc")
	require.NoError(t, err)
	assert.Equal(t, "", clause)
}

func TestSafeOrderClause_InvalidColumn_ReturnsError(t *testing.T) {
	_, err := SafeOrderClause("users", "malicious_col", "asc")
	assert.Error(t, err)
}

func TestAllowedSortColumns_AllEntitiesHaveCreatedAt(t *testing.T) {
	for entity, cols := range AllowedSortColumns {
		assert.True(t, cols["created_at"], "%s should allow sorting by created_at", entity)
		assert.True(t, cols["id"], "%s should allow sorting by id", entity)
	}
}
