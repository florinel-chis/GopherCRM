package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestUserRoleIsValid pins the closed set GET /users validates its role filter
// against (#428): exactly the five declared roles, matched case-sensitively.
func TestUserRoleIsValid(t *testing.T) {
	for _, r := range []UserRole{RoleAdmin, RoleSales, RoleSupport, RoleCustomer, RoleCustom} {
		assert.True(t, r.IsValid(), "%q", r)
	}
	assert.Equal(t, []UserRole{"admin", "sales", "support", "customer", "custom"},
		[]UserRole{RoleAdmin, RoleSales, RoleSupport, RoleCustomer, RoleCustom})
	for _, r := range []UserRole{"", "Sales", "ADMIN", "manager", " sales", "sales ", "support,sales"} {
		assert.False(t, r.IsValid(), "%q", r)
	}
}
