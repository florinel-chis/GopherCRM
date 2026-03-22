package utils

import (
	"fmt"
	"strings"
)

// AllowedSortColumns defines valid sort columns per entity to prevent SQL injection.
var AllowedSortColumns = map[string]map[string]bool{
	"users": {
		"id": true, "email": true, "first_name": true, "last_name": true,
		"role": true, "is_active": true, "created_at": true, "updated_at": true,
	},
	"leads": {
		"id": true, "first_name": true, "last_name": true, "email": true,
		"company": true, "phone": true, "status": true, "classification": true,
		"source": true, "created_at": true, "updated_at": true,
	},
	"customers": {
		"id": true, "first_name": true, "last_name": true, "email": true,
		"company": true, "phone": true, "city": true, "state": true,
		"country": true, "created_at": true, "updated_at": true,
	},
	"tickets": {
		"id": true, "title": true, "status": true, "priority": true,
		"customer_id": true, "assigned_to_id": true, "created_at": true, "updated_at": true,
	},
	"tasks": {
		"id": true, "title": true, "status": true, "priority": true,
		"due_date": true, "assigned_to_id": true, "created_at": true, "updated_at": true,
	},
}

// ValidateSort checks that sortBy is in the allowlist for the given entity and
// sortOrder is either "asc" or "desc". Returns safe values or an error.
func ValidateSort(entity, sortBy, sortOrder string) (string, string, error) {
	cols, ok := AllowedSortColumns[entity]
	if !ok {
		return "", "", fmt.Errorf("unknown entity for sorting: %s", entity)
	}

	if sortBy == "" {
		return "created_at", "desc", nil
	}

	if !cols[sortBy] {
		return "", "", fmt.Errorf("invalid sort column %q for %s", sortBy, entity)
	}

	order := strings.ToLower(strings.TrimSpace(sortOrder))
	if order != "asc" && order != "desc" {
		order = "desc"
	}

	return sortBy, order, nil
}

// SafeOrderClause returns a validated "column direction" string safe for use in
// GORM's Order(). Returns empty string if sortBy is empty (caller should skip ordering).
func SafeOrderClause(entity, sortBy, sortOrder string) (string, error) {
	if sortBy == "" {
		return "", nil
	}
	col, dir, err := ValidateSort(entity, sortBy, sortOrder)
	if err != nil {
		return "", err
	}
	return col + " " + dir, nil
}
