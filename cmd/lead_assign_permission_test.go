package main

// POST /leads/:id/assign for a custom role (#377): naming another user as the
// owner asks for leads.assign_owner next to leads.update, and assigning to
// oneself asks for leads.update alone. The requests run on the real router
// over the route-matrix harness, used as it is.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// leadAssignToOwnMessage is the 403 text of the owner rule, as distinct from
// the 'You can only assign your own leads' of the visibility rule.
const leadAssignToOwnMessage = "You can only assign leads to yourself"

func TestLeadAssignRequiresAssignOwnerKey(t *testing.T) {
	h := bootRouteMatrix(t)
	editor := addCustomPrincipal(t, h, "leadeditor", false)
	owner := addCustomPrincipal(t, h, "leadowner", true)
	refreshMatrixSnapshot(t, h)
	roles := repository.NewRoleRepository(models.DB)

	otherSales := h.fx.users["otherSales"].ID
	adminLead := h.fx.numeric("lead", caseOwn, "admin")
	ownLead := h.fx.numeric("lead", caseOwn, owner.name)

	type grant struct {
		key   string
		scope models.PermissionScope
	}
	all := func(keys ...string) []grant {
		grants := make([]grant, 0, len(keys))
		for _, key := range keys {
			grants = append(grants, grant{key: key, scope: models.PermissionScopeAll})
		}
		return grants
	}
	// hold writes exactly these grants onto the principal's role. It runs
	// after the restore of the seeded state, which would undo it.
	hold := func(principal *matrixPrincipal, grants []grant) {
		t.Helper()
		restoreMatrixState(h)
		rows := make([]models.RolePermission, 0, len(grants))
		for _, g := range grants {
			rows = append(rows, models.RolePermission{RoleID: principal.role.ID, PermissionKey: g.key, Scope: g.scope})
		}
		require.NoError(t, roles.ReplacePermissions(principal.role.ID, rows))
	}
	ownerOf := func(leadID uint) uint {
		t.Helper()
		var lead models.Lead
		require.NoError(t, models.DB.Select("owner_id").First(&lead, leadID).Error)
		return lead.OwnerID
	}
	// assign sends the request as the principal and returns the status, the
	// envelope's error code and its error message.
	assign := func(principal *matrixPrincipal, leadID string, body string) (int, string, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, routeMatrixPrefix+"/leads/"+leadID+"/assign", bytes.NewReader([]byte(body)))
		request.RemoteAddr = nextMatrixRemoteAddr()
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+h.tokens[principal.name])
		recorder := httptest.NewRecorder()
		h.router.ServeHTTP(recorder, request)
		var envelope struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(recorder.Body.Bytes(), &envelope) == nil && envelope.Error != nil {
			return recorder.Code, envelope.Error.Code, envelope.Error.Message
		}
		return recorder.Code, "", ""
	}
	id := func(leadID uint) string { return fmt.Sprintf("%d", leadID) }
	to := func(userID uint) string { return fmt.Sprintf(`{"user_id": %d}`, userID) }

	t.Run("leads.update at all without leads.assign_owner is refused for another user", func(t *testing.T) {
		hold(editor, all(permissions.LeadsUpdate))
		before := ownerOf(adminLead)
		require.NotEqual(t, otherSales, before, "the fixture lead is not the named user's yet")

		status, code, message := assign(editor, id(adminLead), to(otherSales))
		assert.Equal(t, http.StatusForbidden, status)
		assert.Equal(t, "FORBIDDEN", code)
		assert.Equal(t, leadAssignToOwnMessage, message)
		assert.Equal(t, before, ownerOf(adminLead), "the committed owner is unchanged")

		// The request is checked for its shape first, as for every caller.
		status, code, _ = assign(editor, "not-a-number", to(otherSales))
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "BAD_REQUEST", code)
		status, code, _ = assign(editor, id(adminLead), `{"user_id": 0}`)
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "VALIDATION_ERROR", code)
		assert.Equal(t, before, ownerOf(adminLead))
	})

	t.Run("leads.update at all with leads.assign_owner assigns to another user", func(t *testing.T) {
		hold(editor, all(permissions.LeadsUpdate, permissions.LeadsAssignOwner))
		require.NotEqual(t, otherSales, ownerOf(adminLead))

		status, code, _ := assign(editor, id(adminLead), to(otherSales))
		assert.Equal(t, http.StatusOK, status)
		assert.Empty(t, code)
		assert.Equal(t, otherSales, ownerOf(adminLead), "the committed owner is the named user")
	})

	t.Run("leads.update at all without leads.assign_owner assigns to itself", func(t *testing.T) {
		// assignment.form_owner makes the caller a user a lead may be given to.
		hold(editor, all(permissions.LeadsUpdate, permissions.AssignmentFormOwner))
		require.NotEqual(t, editor.user.ID, ownerOf(adminLead))

		status, code, _ := assign(editor, id(adminLead), to(editor.user.ID))
		assert.Equal(t, http.StatusOK, status)
		assert.Empty(t, code)
		assert.Equal(t, editor.user.ID, ownerOf(adminLead), "the committed owner is the caller")
	})

	t.Run("leads.update at own without leads.assign_owner is refused on its own lead", func(t *testing.T) {
		hold(owner, []grant{{key: permissions.LeadsUpdate, scope: models.PermissionScopeOwn}})
		require.Equal(t, owner.user.ID, ownerOf(ownLead), "the lead is the caller's")

		status, code, message := assign(owner, id(ownLead), to(otherSales))
		assert.Equal(t, http.StatusForbidden, status)
		assert.Equal(t, "FORBIDDEN", code)
		assert.Equal(t, leadAssignToOwnMessage, message)
		assert.Equal(t, owner.user.ID, ownerOf(ownLead), "the committed owner is unchanged")
	})
}
