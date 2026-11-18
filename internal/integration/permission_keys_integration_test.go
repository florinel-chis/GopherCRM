package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func TestSeparatePermissionKeysSQLite(t *testing.T) {
	runSeparatePermissionKeys(t, false)
}

func TestSeparatePermissionKeysMySQL(t *testing.T) {
	runSeparatePermissionKeys(t, true)
}

// runSeparatePermissionKeys (#259 AC-25): each permission key gates on its
// own. A service role argument used to stand for several of these decisions at
// once; a custom role holding one of them must not get the others with it.
func runSeparatePermissionKeys(t *testing.T, useMySQL bool) {
	env := newPermissionEnv(t, useMySQL)
	admin := env.user("admin", models.RoleAdmin)
	// The other owner or assignee every refused request names: a sales user,
	// eligible for all of it, so a refusal is about the caller's permission.
	other := env.user("other", models.RoleSales)
	role := env.role("Separate keys")
	caller := env.customUser("caller", role)

	forbidden := func(t *testing.T, response permissionResponse) {
		t.Helper()
		assert.Equal(t, http.StatusForbidden, response.Status, response.Raw)
		assert.Equal(t, "FORBIDDEN", response.Code)
	}

	t.Run("leads.delete at scope all does not erase a converted lead without leads.erase_converted", func(t *testing.T) {
		customer := env.customer(other)
		lead := env.lead(other, models.LeadStatusConverted, nil)
		require.NoError(t, env.db.Model(&models.Lead{}).Where("id = ?", lead.ID).Update("customer_id", customer.ID).Error)
		plain := env.lead(other, models.LeadStatusNew, nil)

		var leadBefore models.Lead
		var customerBefore models.Customer
		require.NoError(t, env.db.First(&leadBefore, lead.ID).Error)
		require.NoError(t, env.db.First(&customerBefore, customer.ID).Error)

		env.setGrants(role, "leads.view", "leads.delete")
		forbidden(t, env.do(caller, http.MethodDelete, "/leads/"+uintPath(lead.ID), nil))

		var leadAfter models.Lead
		var customerAfter models.Customer
		require.NoError(t, env.db.Unscoped().First(&leadAfter, lead.ID).Error)
		require.NoError(t, env.db.Unscoped().First(&customerAfter, customer.ID).Error)
		assert.False(t, leadAfter.DeletedAt.Valid, "the converted lead is not erased")
		assert.Equal(t, leadBefore.Email, leadAfter.Email)
		assert.Equal(t, leadBefore.FirstName, leadAfter.FirstName)
		assert.Equal(t, leadBefore.Status, leadAfter.Status)
		assert.False(t, customerAfter.DeletedAt.Valid, "its customer is not erased")
		assert.Equal(t, customerBefore.Email, customerAfter.Email)
		assert.Equal(t, customerBefore.FirstName, customerAfter.FirstName)

		// The same scope does delete another owner's lead that was never converted.
		deleted := env.do(caller, http.MethodDelete, "/leads/"+uintPath(plain.ID), nil)
		assert.Equal(t, http.StatusNoContent, deleted.Status, deleted.Raw)

		// With the key the erasure reaches the lead and the customer it became.
		env.setGrants(role, "leads.view", "leads.delete", "leads.erase_converted")
		erased := env.do(caller, http.MethodDelete, "/leads/"+uintPath(lead.ID), nil)
		require.Equal(t, http.StatusNoContent, erased.Status, erased.Raw)
		require.NoError(t, env.db.Unscoped().First(&leadAfter, lead.ID).Error)
		require.NoError(t, env.db.Unscoped().First(&customerAfter, customer.ID).Error)
		assert.True(t, leadAfter.DeletedAt.Valid)
		assert.True(t, customerAfter.DeletedAt.Valid)

		// leads.erase_converted without leads.delete opens nothing.
		another := env.lead(other, models.LeadStatusNew, nil)
		env.setGrants(role, "leads.view", "leads.erase_converted")
		forbidden(t, env.do(caller, http.MethodDelete, "/leads/"+uintPath(another.ID), nil))
	})

	// Naming another owner or assignee: refused without the key on create and
	// on update, accepted with it. base is what the caller holds either way.
	customer := env.customer(other)
	for _, c := range []struct {
		key    string
		base   []string
		create func() (path string, body map[string]interface{})
		// update returns a record of the caller's and the body handing it over.
		update func() (path string, body map[string]interface{})
		// owner reads the stored owner or assignee of a record id.
		table, column string
	}{
		{
			key: "quotes.assign_owner", base: []string{"quotes.view", "quotes.create", "quotes.update"},
			create: func() (string, map[string]interface{}) {
				return "/quotes", map[string]interface{}{"title": "Assigned quote", "customer_id": customer.ID, "owner_id": other.ID}
			},
			update: func() (string, map[string]interface{}) {
				return "/quotes/" + uintPath(env.quote(caller, customer, nil).ID), map[string]interface{}{"owner_id": other.ID}
			},
			table: "quotes", column: "owner_id",
		},
		{
			// assignment.form_owner makes the caller itself an eligible lead
			// owner; it says nothing about naming somebody else.
			key: "leads.assign_owner", base: []string{"leads.view", "leads.create", "leads.update", "assignment.form_owner"},
			create: func() (string, map[string]interface{}) {
				return "/leads", map[string]interface{}{"first_name": "Assigned", "last_name": "Lead",
					"email": fmt.Sprintf("assigned-%d@permissions.test", env.next()), "owner_id": other.ID}
			},
			update: func() (string, map[string]interface{}) {
				return "/leads/" + uintPath(env.lead(caller, models.LeadStatusNew, nil).ID), map[string]interface{}{"owner_id": other.ID}
			},
			table: "leads", column: "owner_id",
		},
		{
			key: "deals.assign_owner", base: []string{"deals.view", "deals.create", "deals.update"},
			create: func() (string, map[string]interface{}) {
				return "/deals", map[string]interface{}{"title": "Assigned deal", "amount_cents": 1000, "owner_id": other.ID}
			},
			update: func() (string, map[string]interface{}) {
				return "/deals/" + uintPath(env.deal(caller, nil, nil).ID), map[string]interface{}{"title": "Handed over", "amount_cents": 1000, "owner_id": other.ID}
			},
			table: "deals", column: "owner_id",
		},
		{
			key: "companies.assign_owner", base: []string{"companies.view", "companies.create", "companies.update"},
			create: func() (string, map[string]interface{}) {
				return "/companies", map[string]interface{}{"name": fmt.Sprintf("Assigned company %d", env.next()), "owner_id": other.ID}
			},
			update: func() (string, map[string]interface{}) {
				return "/companies/" + uintPath(env.company(caller).ID), map[string]interface{}{"name": fmt.Sprintf("Handed over %d", env.next()), "owner_id": other.ID}
			},
			table: "companies", column: "owner_id",
		},
		{
			key: "tasks.assign_others", base: []string{"tasks.view", "tasks.create", "tasks.update"},
			create: func() (string, map[string]interface{}) {
				return "/tasks", map[string]interface{}{"title": "Assigned task", "assigned_to_id": other.ID}
			},
			update: func() (string, map[string]interface{}) {
				return "/tasks/" + uintPath(env.task(caller, models.TaskStatusPending).ID), map[string]interface{}{"assigned_to_id": other.ID}
			},
			table: "tasks", column: "assigned_to_id",
		},
	} {
		t.Run("another owner or assignee needs "+c.key, func(t *testing.T) {
			count := func() int64 {
				var n int64
				require.NoError(t, env.db.Table(c.table).Where(c.column+" = ? AND deleted_at IS NULL", other.ID).Count(&n).Error)
				return n
			}
			before := count()

			// Without the key: both the create and the update are refused
			// and nothing is written.
			env.setGrants(role, c.base...)
			path, body := c.create()
			forbidden(t, env.do(caller, http.MethodPost, path, body))
			path, body = c.update()
			forbidden(t, env.do(caller, http.MethodPut, path, body))
			assert.Equal(t, before, count(), "nothing was handed to the other user")

			// Naming itself needs no extra key.
			path, body = c.create()
			body[c.column] = caller.ID
			own := env.do(caller, http.MethodPost, path, body)
			assert.Equal(t, http.StatusCreated, own.Status, own.Raw)

			// With the key: both go through.
			env.setGrants(role, append(append([]string(nil), c.base...), c.key)...)
			path, body = c.create()
			created := env.do(caller, http.MethodPost, path, body)
			assert.Equal(t, http.StatusCreated, created.Status, created.Raw)
			path, body = c.update()
			updated := env.do(caller, http.MethodPut, path, body)
			assert.Equal(t, http.StatusOK, updated.Status, updated.Raw)
			assert.Equal(t, before+2, count(), "the created and the updated record now belong to the other user")

			// The key alone, without the create or update key, opens nothing.
			env.setGrants(role, c.key)
			path, body = c.create()
			forbidden(t, env.do(caller, http.MethodPost, path, body))
		})
	}

	t.Run("customers.export is its own key", func(t *testing.T) {
		env.setGrants(role, "customers.view", "customers.create", "customers.update", "customers.delete", "customers.assign")
		forbidden(t, env.do(caller, http.MethodGet, "/customers/export", nil))
		assert.Equal(t, http.StatusOK, env.do(caller, http.MethodGet, "/customers", nil).Status)

		env.setGrants(role, "customers.export")
		exported := env.do(caller, http.MethodGet, "/customers/export", nil)
		assert.Equal(t, http.StatusOK, exported.Status, exported.Raw)
		assert.True(t, strings.Contains(exported.Raw, customer.Email), "the export holds the customers")
		forbidden(t, env.do(caller, http.MethodGet, "/customers", nil))
	})

	t.Run("quotes.create with deals.view at scope own quotes only its own deals", func(t *testing.T) {
		ownDeal := env.deal(caller, customer, nil)
		foreignDeal := env.deal(other, customer, nil)
		quoteFor := func(deal *models.Deal) permissionResponse {
			return env.do(caller, http.MethodPost, "/quotes", map[string]interface{}{"title": "Deal quote", "deal_id": deal.ID})
		}
		quotesOf := func(deal *models.Deal) int64 {
			var n int64
			require.NoError(t, env.db.Model(&models.Quote{}).Where("deal_id = ?", deal.ID).Count(&n).Error)
			return n
		}

		env.setGrants(role, "quotes.create", "deals.view:own")
		created := quoteFor(ownDeal)
		assert.Equal(t, http.StatusCreated, created.Status, created.Raw)
		forbidden(t, quoteFor(foreignDeal))
		assert.Zero(t, quotesOf(foreignDeal), "no quote was written on another owner's deal")

		// quotes.view and quotes.update at scope all do not widen it: the
		// deals.view scope alone decides which deals may be quoted.
		env.setGrants(role, "quotes.create", "quotes.view", "quotes.update", "quotes.delete", "deals.view:own")
		forbidden(t, quoteFor(foreignDeal))
		// Nor does quotes.assign_owner, the other decision of the same call.
		env.setGrants(role, "quotes.create", "quotes.assign_owner", "deals.view:own")
		forbidden(t, quoteFor(foreignDeal))
		assert.Zero(t, quotesOf(foreignDeal))

		env.setGrants(role, "quotes.create", "deals.view")
		widened := quoteFor(foreignDeal)
		assert.Equal(t, http.StatusCreated, widened.Status, widened.Raw)
		assert.EqualValues(t, 1, quotesOf(foreignDeal))
	})

	t.Run("automation.runs_view decides the runs, not leads.view", func(t *testing.T) {
		rule := env.automationRule(admin)
		ownLead := env.lead(caller, models.LeadStatusNew, nil)
		ownRun := env.automationRun(rule, ownLead)
		foreignRuns := []*models.AutomationRun{
			env.automationRun(rule, env.lead(other, models.LeadStatusNew, nil)),
			env.automationRun(rule, env.lead(admin, models.LeadStatusNew, nil)),
		}

		// Every lead is visible, the runs are not.
		env.setGrants(role, "leads.view", "automation.runs_view:own")
		assert.Equal(t, http.StatusOK, env.do(caller, http.MethodGet, "/leads/"+uintPath(foreignRuns[0].EntityID), nil).Status)
		list := env.do(caller, http.MethodGet, "/automation/runs", nil)
		require.Equal(t, http.StatusOK, list.Status, list.Raw)
		assert.Equal(t, []uint{ownRun.ID}, list.ids(t), "only the runs on the caller's own records")
		assert.EqualValues(t, 1, list.Total)
		assert.Equal(t, http.StatusOK, env.do(caller, http.MethodGet, "/automation/runs/"+uintPath(ownRun.ID), nil).Status)
		for _, run := range foreignRuns {
			hidden := env.do(caller, http.MethodGet, "/automation/runs/"+uintPath(run.ID), nil)
			assert.Equal(t, http.StatusNotFound, hidden.Status, hidden.Raw)
		}

		// The reverse: every run, whatever the lead scope.
		env.setGrants(role, "leads.view:own", "automation.runs_view")
		list = env.do(caller, http.MethodGet, "/automation/runs", nil)
		require.Equal(t, http.StatusOK, list.Status, list.Raw)
		assert.ElementsMatch(t, []uint{ownRun.ID, foreignRuns[0].ID, foreignRuns[1].ID}, list.ids(t))
		assert.EqualValues(t, 3, list.Total)

		// leads.view alone opens no run at all.
		env.setGrants(role, "leads.view")
		forbidden(t, env.do(caller, http.MethodGet, "/automation/runs", nil))
		forbidden(t, env.do(caller, http.MethodGet, "/automation/runs/"+uintPath(ownRun.ID), nil))
	})
}
