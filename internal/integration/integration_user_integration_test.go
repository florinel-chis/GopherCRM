package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
)

// The least-privilege integration user (#211) is the setup the section of the
// same name in docs/AUTHORIZATION.md describes: a custom role holding only
// what the integration needs, a dedicated user on that role and an API key
// issued to that user. The suite builds all three through the documented
// routes and sends every request of the integration with the key, so what it
// shows is what an operator following the guide gets.

func TestLeastPrivilegeIntegrationUserSQLite(t *testing.T) {
	runLeastPrivilegeIntegrationUser(t, false)
}

func TestLeastPrivilegeIntegrationUserMySQL(t *testing.T) {
	runLeastPrivilegeIntegrationUser(t, true)
}

// issuedKey is the answer of POST /api-keys.
type issuedKey struct {
	Key    string `json:"key"`
	APIKey struct {
		ID     uint `json:"id"`
		UserID uint `json:"user_id"`
	} `json:"api_key"`
}

func decodeIssuedKey(t *testing.T, response permissionResponse) issuedKey {
	t.Helper()
	requireAnswer(t, response, http.StatusCreated, "")
	var issued issuedKey
	require.NoError(t, json.Unmarshal(response.Data, &issued), response.Raw)
	require.NotEmpty(t, issued.Key, "the plaintext key is returned once")
	require.NotZero(t, issued.APIKey.ID)
	return issued
}

// mePermissions is the permission list of GET /users/me as "key:scope".
func mePermissions(t *testing.T, response permissionResponse) []string {
	t.Helper()
	requireAnswer(t, response, http.StatusOK, "")
	var me struct {
		Permissions []struct {
			Key   string `json:"key"`
			Scope string `json:"scope"`
		} `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal(response.Data, &me), response.Raw)
	out := make([]string, 0, len(me.Permissions))
	for _, permission := range me.Permissions {
		out = append(out, permission.Key+":"+permission.Scope)
	}
	return out
}

func createdID(t *testing.T, response permissionResponse) uint {
	t.Helper()
	requireAnswer(t, response, http.StatusCreated, "")
	var record struct {
		ID uint `json:"id"`
	}
	require.NoError(t, json.Unmarshal(response.Data, &record), response.Raw)
	require.NotZero(t, record.ID, response.Raw)
	return record.ID
}

func (e *permissionEnv) rowCount(model interface{}) int64 {
	e.t.Helper()
	var count int64
	require.NoError(e.t, e.db.Unscoped().Model(model).Count(&count).Error)
	return count
}

func runLeastPrivilegeIntegrationUser(t *testing.T, useMySQL bool) {
	env := newPermissionEnv(t, useMySQL)
	admin := env.user("admin", models.RoleAdmin)
	support := env.user("support", models.RoleSupport)
	sales := env.user("sales", models.RoleSales)

	intakeGrants := []string{permissions.CustomersCreate, permissions.TicketsCreate}
	intakePermissions := []string{permissions.CustomersCreate + ":all", permissions.TicketsCreate + ":all"}

	// --- setup, through the three calls of the guide ---------------------------

	created := env.do(admin, http.MethodPost, "/roles", roleBody("Ticket intake", intakeGrants...))
	requireAnswer(t, created, http.StatusCreated, "")
	role := decodeRole(t, created)
	require.Equal(t, intakePermissions, role.grants())

	created = env.do(admin, http.MethodPost, "/users", env.newUserBody("custom", &role.ID))
	requireAnswer(t, created, http.StatusCreated, "")
	account := decodeUser(t, created)
	require.Equal(t, "custom", account.Role)
	require.NotNil(t, account.RoleID)
	require.Equal(t, role.ID, *account.RoleID)
	integration, err := env.userRepo.GetByID(account.ID)
	require.NoError(t, err)

	// POST /api-keys creates a key for the caller: it is issued signed in as
	// the integration user.
	first := decodeIssuedKey(t, env.do(integration, http.MethodPost, "/api-keys", map[string]interface{}{"name": "intake"}))
	require.Equal(t, integration.ID, first.APIKey.UserID)
	key := "ApiKey " + first.Key

	customerBody := func() map[string]interface{} {
		return map[string]interface{}{"first_name": "Intake", "last_name": "Customer",
			"email": fmt.Sprintf("intake-%d@permissions.test", env.next())}
	}
	requireMe := func(t *testing.T, authorization string) {
		t.Helper()
		assert.Equal(t, intakePermissions, mePermissions(t, env.send(authorization, http.MethodGet, "/users/me", nil)))
	}

	t.Run("me_lists_exactly_the_two_keys", func(t *testing.T) {
		requireMe(t, key)
	})

	// --- what the role opens ---------------------------------------------------

	var customerID, ticketID uint
	firstCustomer := customerBody()
	t.Run("creates_a_customer", func(t *testing.T) {
		customerID = createdID(t, env.send(key, http.MethodPost, "/customers", firstCustomer))
		stored, err := env.customerRepo.GetByID(customerID)
		require.NoError(t, err)
		assert.Equal(t, firstCustomer["email"], stored.Email)
	})
	require.NotZero(t, customerID)

	ticketBody := func(assignee *uint) map[string]interface{} {
		body := map[string]interface{}{"title": "Intake ticket", "description": "Opened by the integration", "customer_id": customerID}
		if assignee != nil {
			body["assigned_to_id"] = *assignee
		}
		return body
	}
	t.Run("creates_a_ticket_for_a_named_assignee", func(t *testing.T) {
		ticketID = createdID(t, env.send(key, http.MethodPost, "/tickets", ticketBody(&support.ID)))
		var stored models.Ticket
		require.NoError(t, env.db.First(&stored, ticketID).Error)
		assert.Equal(t, customerID, stored.CustomerID)
		require.NotNil(t, stored.AssignedToID)
		assert.Equal(t, support.ID, *stored.AssignedToID)
	})
	require.NotZero(t, ticketID)

	// Without assigned_to_id the handler assigns the ticket to the caller,
	// who does not hold assignment.ticket_assignee.
	t.Run("ticket_without_assignee_is_refused", func(t *testing.T) {
		tickets := env.rowCount(&models.Ticket{})
		requireAnswer(t, env.send(key, http.MethodPost, "/tickets", ticketBody(nil)), http.StatusBadRequest, "BAD_REQUEST")
		assert.Equal(t, tickets, env.rowCount(&models.Ticket{}), "no ticket row is added")
	})

	// A duplicate email is a conflict that does not name the existing record.
	t.Run("duplicate_customer_email_carries_no_id", func(t *testing.T) {
		customers := env.rowCount(&models.Customer{})
		response := env.send(key, http.MethodPost, "/customers", firstCustomer)
		requireAnswer(t, response, http.StatusConflict, "CONFLICT")
		assert.NotContains(t, response.Raw, `"id"`)
		assert.Contains(t, []string{"", "null"}, string(response.Data), "no data in the conflict answer")
		assert.Equal(t, customers, env.rowCount(&models.Customer{}))
	})

	// --- everything else is refused --------------------------------------------

	seededCustomer := env.customer(sales)
	seededTicket := env.ticket(support, seededCustomer, models.TicketStatusOpen)
	c, tk := "/customers/"+uintPath(seededCustomer.ID), "/tickets/"+uintPath(seededTicket.ID)
	u, r := userRoutePath(support.ID), roleRoutePath(role.ID)
	require.NotEqual(t, integration.ID, support.ID, "the user routes are tried on another account")

	t.Run("denied", func(t *testing.T) {
		denied := []struct {
			method, path string
			body         interface{}
		}{
			{http.MethodGet, "/customers", nil},
			{http.MethodGet, "/customers/export", nil},
			{http.MethodGet, c, nil},
			{http.MethodPut, c, map[string]interface{}{"first_name": "Changed"}},
			{http.MethodDelete, c, nil},
			{http.MethodPost, c + "/assign", map[string]interface{}{"user_id": sales.ID}},
			{http.MethodGet, c + "/tickets", nil},
			// The permission check comes before ownership: the records the
			// integration created itself are closed to it as well.
			{http.MethodGet, "/customers/" + uintPath(customerID), nil},
			{http.MethodGet, "/tickets/" + uintPath(ticketID), nil},
			{http.MethodGet, "/tickets", nil},
			{http.MethodGet, "/tickets/my", nil},
			{http.MethodGet, tk, nil},
			{http.MethodPut, tk, map[string]interface{}{"status": "closed"}},
			{http.MethodDelete, tk, nil},
			{http.MethodGet, tk + "/comments", nil},
			{http.MethodPost, tk + "/comments", map[string]interface{}{"body": "A comment"}},
			{http.MethodPost, "/tickets/bulk/status", map[string]interface{}{"ticket_ids": []uint{seededTicket.ID}, "status": "closed"}},
			{http.MethodGet, "/users", nil},
			{http.MethodPost, "/users", env.newUserBody("admin", nil)},
			{http.MethodGet, u, nil},
			{http.MethodPut, u, map[string]interface{}{"first_name": "Changed"}},
			{http.MethodDelete, u, nil},
			{http.MethodPost, u + "/activate", nil},
			{http.MethodPost, u + "/deactivate", nil},
			{http.MethodGet, "/roles", nil},
			{http.MethodPost, "/roles", roleBody("Minted by the integration", permissions.CustomersCreate)},
			{http.MethodGet, "/roles/audit", nil},
			{http.MethodGet, r, nil},
			{http.MethodPut, r, roleBody("Widened", permissions.CustomersCreate, permissions.TicketsCreate, permissions.CustomersView)},
			{http.MethodDelete, r, nil},
			{http.MethodPost, r + "/clone", map[string]interface{}{"name": "Clone"}},
			{http.MethodGet, "/permissions/catalogue", nil},
			{http.MethodGet, "/leads", nil},
			{http.MethodGet, "/leads/export", nil},
			{http.MethodPost, "/leads", map[string]interface{}{"first_name": "Lead", "last_name": "Refused", "email": "refused-lead@permissions.test"}},
			{http.MethodGet, "/dashboard/stats", nil},
			{http.MethodGet, "/labels", nil},
			{http.MethodGet, "/tasks", nil},
			{http.MethodGet, "/deals", nil},
			{http.MethodGet, "/companies", nil},
			{http.MethodGet, "/quotes", nil},
			{http.MethodGet, "/automation/rules", nil},
		}
		require.GreaterOrEqual(t, len(denied), 40)

		users, roles := env.userCount(), env.roleCount()
		customers, tickets, leads := env.rowCount(&models.Customer{}), env.rowCount(&models.Ticket{}), env.rowCount(&models.Lead{})
		supportState, roleState := env.userState(support.ID), env.roleState(role.ID)

		for _, route := range denied {
			t.Run(route.method+" "+route.path, func(t *testing.T) {
				response := env.send(key, route.method, route.path, route.body)
				assert.Equal(t, http.StatusForbidden, response.Status, response.Raw)
				assert.Equal(t, "FORBIDDEN", response.Code, response.Raw)
			})
		}

		assert.Equal(t, users, env.userCount(), "users")
		assert.Equal(t, roles, env.roleCount(), "roles")
		assert.Equal(t, customers, env.rowCount(&models.Customer{}), "customers")
		assert.Equal(t, tickets, env.rowCount(&models.Ticket{}), "tickets")
		assert.Equal(t, leads, env.rowCount(&models.Lead{}), "leads")
		assert.Equal(t, supportState, env.userState(support.ID), "the other account is unchanged")
		assert.Equal(t, roleState, env.roleState(role.ID), "the integration's role is unchanged")
	})

	// The own account is self-service (GET and PUT answer 200), which must
	// not be a way to another role.
	t.Run("cannot_raise_its_own_role", func(t *testing.T) {
		other := env.role("Wider", permissions.CustomersCreate, permissions.TicketsCreate, permissions.CustomersView)
		own := userRoutePath(integration.ID)
		state, audits := env.userState(integration.ID), env.auditCount()

		requireAnswer(t, env.send(key, http.MethodGet, own, nil), http.StatusOK, "")
		// Without users.assign_role and users.activate the handler ignores
		// role, role_id and is_active: the answer is 200 and nothing changes.
		for _, body := range []map[string]interface{}{
			{"role": "admin"},
			{"role": "custom", "role_id": other.ID},
			{"is_active": false},
		} {
			answer := env.send(key, http.MethodPut, own, body)
			requireAnswer(t, answer, http.StatusOK, "")
			after := decodeUser(t, answer)
			assert.Equal(t, "custom", after.Role)
			require.NotNil(t, after.RoleID)
			assert.Equal(t, role.ID, *after.RoleID)
			assert.True(t, after.IsActive)
		}
		// PUT /users/me has no role field: the key is ignored.
		requireAnswer(t, env.send(key, http.MethodPut, "/users/me", map[string]interface{}{"role": "admin", "role_id": other.ID}),
			http.StatusOK, "")

		assert.Equal(t, state, env.userState(integration.ID), "role and role_id are what the administrator set")
		assert.Equal(t, audits, env.auditCount(), "no role change was written")
		requireMe(t, key)
	})

	// The 403 is the missing key and nothing else: the administrator adds
	// customers.view to the role and the same key reads on its next request.
	t.Run("a_granted_key_applies_on_the_next_request", func(t *testing.T) {
		requireAnswer(t, env.send(key, http.MethodGet, "/customers", nil), http.StatusForbidden, "FORBIDDEN")
		requireAnswer(t, env.do(admin, http.MethodPut, r, roleBody(role.Name, append(intakeGrants, permissions.CustomersView)...)),
			http.StatusOK, "")
		listed := env.send(key, http.MethodGet, "/customers", nil)
		requireAnswer(t, listed, http.StatusOK, "")
		assert.Contains(t, listed.ids(t), customerID)

		requireAnswer(t, env.do(admin, http.MethodPut, r, roleBody(role.Name, intakeGrants...)), http.StatusOK, "")
		requireAnswer(t, env.send(key, http.MethodGet, "/customers", nil), http.StatusForbidden, "FORBIDDEN")
	})

	// What a form handler that writes leads through POST /leads needs.
	t.Run("lead_intake_needs_an_eligible_owner", func(t *testing.T) {
		stored := &models.Role{ID: role.ID}
		leadBody := func(owner *uint) map[string]interface{} {
			body := map[string]interface{}{"first_name": "Form", "last_name": "Lead",
				"email": fmt.Sprintf("form-lead-%d@permissions.test", env.next())}
			if owner != nil {
				body["owner_id"] = *owner
			}
			return body
		}
		ownerOf := func(id uint) uint {
			var lead models.Lead
			require.NoError(t, env.db.First(&lead, id).Error)
			return lead.OwnerID
		}
		leads := env.rowCount(&models.Lead{})

		// leads.create alone: the lead would be owned by the caller, who
		// cannot own leads.
		env.setGrants(stored, permissions.LeadsCreate)
		requireAnswer(t, env.send(key, http.MethodPost, "/leads", leadBody(nil)), http.StatusBadRequest, "INVALID_REFERENCE")
		requireAnswer(t, env.send(key, http.MethodPost, "/leads", leadBody(&sales.ID)), http.StatusForbidden, "FORBIDDEN")
		assert.Equal(t, leads, env.rowCount(&models.Lead{}))

		// With assignment.form_owner the integration user owns its leads.
		env.setGrants(stored, permissions.LeadsCreate, permissions.AssignmentFormOwner)
		assert.Equal(t, integration.ID, ownerOf(createdID(t, env.send(key, http.MethodPost, "/leads", leadBody(nil)))))

		// With leads.assign_owner the body names the owner, and must.
		env.setGrants(stored, permissions.LeadsCreate, permissions.LeadsAssignOwner)
		assert.Equal(t, sales.ID, ownerOf(createdID(t, env.send(key, http.MethodPost, "/leads", leadBody(&sales.ID)))))
		requireAnswer(t, env.send(key, http.MethodPost, "/leads", leadBody(nil)), http.StatusBadRequest, "BAD_REQUEST")
		requireAnswer(t, env.send(key, http.MethodPost, "/leads", leadBody(&support.ID)), http.StatusBadRequest, "INVALID_REFERENCE")
		assert.Equal(t, leads+2, env.rowCount(&models.Lead{}))

		env.setGrants(stored, intakeGrants...)
		requireMe(t, key)
	})

	// An administrator has no route to another user's keys.
	t.Run("admin_cannot_list_or_revoke_the_key", func(t *testing.T) {
		path := "/api-keys/" + uintPath(first.APIKey.ID)
		listed := env.do(admin, http.MethodGet, "/api-keys", nil)
		requireAnswer(t, listed, http.StatusOK, "")
		assert.NotContains(t, listed.ids(t), first.APIKey.ID, "the list holds the caller's own keys only")
		requireAnswer(t, env.do(admin, http.MethodGet, path, nil), http.StatusForbidden, "FORBIDDEN")
		requireAnswer(t, env.do(admin, http.MethodPut, path, map[string]interface{}{"is_active": false}), http.StatusForbidden, "FORBIDDEN")
		requireAnswer(t, env.do(admin, http.MethodDelete, path, nil), http.StatusForbidden, "FORBIDDEN")
		requireMe(t, key)
	})

	// --- rotation and revocation -------------------------------------------------

	var successor string
	t.Run("rotation", func(t *testing.T) {
		// The current key creates its successor.
		second := decodeIssuedKey(t, env.send(key, http.MethodPost, "/api-keys", map[string]interface{}{"name": "intake rotated"}))
		require.Equal(t, integration.ID, second.APIKey.UserID)
		successor = "ApiKey " + second.Key
		requireMe(t, successor)

		// Both work until the old one is revoked.
		createdID(t, env.send(key, http.MethodPost, "/customers", customerBody()))
		requireAnswer(t, env.send(successor, http.MethodDelete, "/api-keys/"+uintPath(first.APIKey.ID), nil), http.StatusOK, "")

		customers := env.rowCount(&models.Customer{})
		requireAnswer(t, env.send(key, http.MethodPost, "/customers", customerBody()), http.StatusUnauthorized, "UNAUTHORIZED")
		assert.Equal(t, customers, env.rowCount(&models.Customer{}), "the revoked key wrote nothing")
		createdID(t, env.send(successor, http.MethodPost, "/customers", customerBody()))
	})
	require.NotEmpty(t, successor)

	// PUT is_active is the same switch as DELETE, in both directions: a
	// revoked key is a flag its owner can set back.
	t.Run("is_active_stops_and_restores_a_key", func(t *testing.T) {
		third := decodeIssuedKey(t, env.send(successor, http.MethodPost, "/api-keys", map[string]interface{}{"name": "intake spare"}))
		spare, path := "ApiKey "+third.Key, "/api-keys/"+uintPath(third.APIKey.ID)
		requireMe(t, spare)

		requireAnswer(t, env.send(successor, http.MethodPut, path, map[string]interface{}{"is_active": false}), http.StatusOK, "")
		requireAnswer(t, env.send(spare, http.MethodGet, "/users/me", nil), http.StatusUnauthorized, "UNAUTHORIZED")
		requireAnswer(t, env.send(successor, http.MethodPut, path, map[string]interface{}{"is_active": true}), http.StatusOK, "")
		requireMe(t, spare)
		requireAnswer(t, env.send(successor, http.MethodDelete, path, nil), http.StatusOK, "")
		requireAnswer(t, env.send(spare, http.MethodGet, "/users/me", nil), http.StatusUnauthorized, "UNAUTHORIZED")
	})

	// The administrator's switch: deactivating the user stops every key.
	t.Run("deactivating_the_user_stops_its_keys", func(t *testing.T) {
		requireMe(t, successor)
		requireAnswer(t, env.do(admin, http.MethodPost, userRoutePath(integration.ID)+"/deactivate", nil), http.StatusOK, "")
		requireAnswer(t, env.send(successor, http.MethodGet, "/users/me", nil), http.StatusUnauthorized, "UNAUTHORIZED")
		requireAnswer(t, env.send(successor, http.MethodPost, "/customers", customerBody()), http.StatusUnauthorized, "UNAUTHORIZED")
		requireAnswer(t, env.send(key, http.MethodGet, "/users/me", nil), http.StatusUnauthorized, "UNAUTHORIZED")
	})
}
