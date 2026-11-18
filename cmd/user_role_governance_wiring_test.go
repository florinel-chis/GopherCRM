package main

// The governed user writes on the router production builds (#348): setupRouter
// must hand the user service the role governance option, or the last-admin
// invariant, the escalation rule and the audit trail would exist only in the
// suites that wire the option themselves.

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func TestUserWritesAreRoleGovernedOnTheRealRouter(t *testing.T) {
	h := bootRouteMatrix(t)
	admin := h.fx.users["admin"]

	// The fixtures hold exactly one active admin.
	var admins int64
	require.NoError(t, models.DB.Model(&models.User{}).
		Where("role = ? AND is_active = ?", models.RoleAdmin, true).Count(&admins).Error)
	require.EqualValues(t, 1, admins, "the fixture's only admin")

	auditRows := func() int64 {
		var count int64
		require.NoError(t, models.DB.Model(&models.RoleAuditEntry{}).Count(&count).Error)
		return count
	}
	before := auditRows()

	body, err := json.Marshal(matrixBody{"role": "sales"})
	require.NoError(t, err)
	cell := h.send("admin", "PUT", routeMatrixPrefix+"/users/"+strconv.FormatUint(uint64(admin.ID), 10), body)
	assert.Equal(t, matrixCell{Status: 409, Code: "LAST_ADMIN"}, cell, "the only admin cannot demote itself")

	var stored models.User
	require.NoError(t, models.DB.First(&stored, admin.ID).Error)
	assert.Equal(t, models.RoleAdmin, stored.Role, "the role is unchanged")
	assert.Nil(t, stored.RoleID)
	assert.True(t, stored.IsActive)
	assert.Equal(t, before, auditRows(), "a refused change writes no audit row")
}
