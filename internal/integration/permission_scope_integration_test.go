package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func TestCustomRoleScopeSQLite(t *testing.T) {
	runCustomRoleScope(t, false)
}

func TestCustomRoleScopeMySQL(t *testing.T) {
	runCustomRoleScope(t, true)
}

// runCustomRoleScope (#259 AC-7): a custom role at scope own lists, reads and
// changes only its own or assigned records, with the narrowing inside the
// query so totals and pages match; the same role at scope all reaches every
// record. Every fixture holds more foreign records than own ones, so a leak
// shows as a wrong total, a wrong page count or a foreign id.
func runCustomRoleScope(t *testing.T, useMySQL bool) {
	env := newPermissionEnv(t, useMySQL)
	admin := env.user("admin", models.RoleAdmin)
	otherSales := env.user("other-sales", models.RoleSales)
	otherSupport := env.user("other-support", models.RoleSupport)
	role := env.role("Scoped")
	caller := env.customUser("caller", role)

	company := env.company(otherSales)
	customer := env.customer(otherSales)
	due := time.Now().UTC().Add(24 * time.Hour)

	// own and foreign hold the ids of one kind of record.
	type records struct{ own, foreign []uint }
	all := func(r records) []uint { return append(append([]uint(nil), r.own...), r.foreign...) }

	var leads, deals, quotes, tickets, tasks, runs records
	rule := env.automationRule(admin)

	// Leads: 3 own (one converted), 4 foreign (two converted), all in the company.
	for i, status := range []models.LeadStatus{models.LeadStatusNew, models.LeadStatusContacted, models.LeadStatusConverted} {
		lead := env.lead(caller, status, company)
		leads.own = append(leads.own, lead.ID)
		if i < 2 {
			runs.own = append(runs.own, env.automationRun(rule, lead).ID)
		}
	}
	for i, status := range []models.LeadStatus{models.LeadStatusNew, models.LeadStatusNew, models.LeadStatusConverted, models.LeadStatusConverted} {
		lead := env.lead(otherSales, status, company)
		leads.foreign = append(leads.foreign, lead.ID)
		if i < 3 {
			runs.foreign = append(runs.foreign, env.automationRun(rule, lead).ID)
		}
	}

	// Deals: 3 own, 4 foreign, all of the customer and the company.
	var ownDeal, foreignDeal *models.Deal
	for i := 0; i < 3; i++ {
		ownDeal = env.deal(caller, customer, company)
		deals.own = append(deals.own, ownDeal.ID)
	}
	for i := 0; i < 4; i++ {
		foreignDeal = env.deal(otherSales, customer, company)
		deals.foreign = append(deals.foreign, foreignDeal.ID)
	}

	// Quotes: 3 own (two on the caller's deal), 4 foreign (one on the
	// caller's deal, three on the other owner's), all of the customer.
	var quotesOnOwnDeal records
	for i := 0; i < 3; i++ {
		var deal *models.Deal
		if i < 2 {
			deal = ownDeal
		}
		quote := env.quote(caller, customer, deal)
		quotes.own = append(quotes.own, quote.ID)
		if deal != nil {
			quotesOnOwnDeal.own = append(quotesOnOwnDeal.own, quote.ID)
		}
	}
	var quotesOnForeignDeal []uint
	for i := 0; i < 4; i++ {
		deal := foreignDeal
		if i == 0 {
			deal = ownDeal
		}
		quote := env.quote(otherSales, customer, deal)
		quotes.foreign = append(quotes.foreign, quote.ID)
		if i == 0 {
			quotesOnOwnDeal.foreign = append(quotesOnOwnDeal.foreign, quote.ID)
		} else {
			quotesOnForeignDeal = append(quotesOnForeignDeal, quote.ID)
		}
	}

	// Tickets: 3 assigned to the caller (two open), 4 to a support user (all open).
	for _, status := range []models.TicketStatus{models.TicketStatusOpen, models.TicketStatusOpen, models.TicketStatusResolved} {
		tickets.own = append(tickets.own, env.ticket(caller, customer, status).ID)
	}
	for i := 0; i < 4; i++ {
		tickets.foreign = append(tickets.foreign, env.ticket(otherSupport, customer, models.TicketStatusOpen).ID)
	}

	// Tasks: 3 assigned to the caller (two not completed), 4 to the sales user.
	newTask := func(assignee *models.User, status models.TaskStatus) uint {
		task := env.task(assignee, status)
		require.NoError(t, env.db.Model(&models.Task{}).Where("id = ?", task.ID).Update("due_date", due).Error)
		return task.ID
	}
	for _, status := range []models.TaskStatus{models.TaskStatusPending, models.TaskStatusPending, models.TaskStatusCompleted} {
		tasks.own = append(tasks.own, newTask(caller, status))
	}
	for i := 0; i < 4; i++ {
		tasks.foreign = append(tasks.foreign, newTask(otherSales, models.TaskStatusPending))
	}

	scoped := func(scope string) []string {
		grants := []string{"companies.view", "customers.view", "dashboard.view"}
		for _, key := range []string{"leads.view", "leads.update", "deals.view", "deals.update", "quotes.view", "quotes.update",
			"tickets.view", "tickets.update", "tasks.view", "tasks.update", "automation.runs_view"} {
			grants = append(grants, key+":"+scope)
		}
		return grants
	}

	get := func(t *testing.T, path string) permissionResponse {
		t.Helper()
		response := env.do(caller, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, response.Status, "GET %s: %s", path, response.Raw)
		return response
	}
	// expectList holds a list route to exactly the wanted records: the full
	// set, the total, and a first page of two with the page count that total
	// gives.
	expectList := func(t *testing.T, path string, want []uint) {
		t.Helper()
		separator := "?"
		if containsQuery(path) {
			separator = "&"
		}
		full := get(t, path+separator+"limit=100")
		assert.ElementsMatch(t, want, full.ids(t), "GET %s: the records", path)
		assert.EqualValues(t, len(want), full.Total, "GET %s: meta.total", path)

		page := get(t, path+separator+"limit=2")
		assert.EqualValues(t, len(want), page.Total, "GET %s?limit=2: meta.total", path)
		assert.EqualValues(t, (len(want)+1)/2, page.Pages, "GET %s?limit=2: meta.total_pages", path)
		pageIDs := page.ids(t)
		assert.Len(t, pageIDs, min(2, len(want)), "GET %s?limit=2: the page is full of permitted records", path)
		assert.Subset(t, want, pageIDs, "GET %s?limit=2: only permitted records", path)
	}
	column := func(t *testing.T, table, name string, id uint) string {
		t.Helper()
		var value string
		require.NoError(t, env.db.Raw(fmt.Sprintf("SELECT %s FROM %s WHERE id = ?", name, table), id).Scan(&value).Error)
		return value
	}
	chartSum := func(t *testing.T, path string) int64 {
		t.Helper()
		var chart struct {
			Datasets []struct {
				Data []int64 `json:"data"`
			} `json:"datasets"`
		}
		require.NoError(t, json.Unmarshal(get(t, path).Data, &chart))
		require.Len(t, chart.Datasets, 1)
		var sum int64
		for _, value := range chart.Datasets[0].Data {
			sum += value
		}
		return sum
	}

	// entity is one of the five primary record kinds.
	type entity struct {
		name, path, table, column string
		records                   records
		body                      map[string]interface{}
	}
	entities := []entity{
		{"lead", "/leads", "leads", "first_name", leads, map[string]interface{}{"first_name": "Changed"}},
		{"deal", "/deals", "deals", "title", deals, map[string]interface{}{"title": "Changed", "amount_cents": 2000}},
		{"quote", "/quotes", "quotes", "title", quotes, map[string]interface{}{"title": "Changed"}},
		{"ticket", "/tickets", "tickets", "title", tickets, map[string]interface{}{"title": "Changed"}},
		{"task", "/tasks", "tasks", "title", tasks, map[string]interface{}{"title": "Changed"}},
	}

	for _, phase := range []struct {
		scope string
		own   bool
	}{{"own", true}, {"all", false}} {
		t.Run("scope "+phase.scope, func(t *testing.T) {
			env.setGrants(role, scoped(phase.scope)...)
			// visible is what the caller may reach of a kind at this scope.
			visible := func(r records) []uint {
				if phase.own {
					return r.own
				}
				return all(r)
			}

			t.Run("primary routes", func(t *testing.T) {
				for _, e := range entities {
					expectList(t, e.path, visible(e.records))

					ownID, foreignID := e.records.own[0], e.records.foreign[0]
					assert.Equal(t, http.StatusOK, env.do(caller, http.MethodGet, e.path+"/"+uintPath(ownID), nil).Status, "GET own %s", e.name)
					changed := env.do(caller, http.MethodPut, e.path+"/"+uintPath(ownID), e.body)
					assert.Equal(t, http.StatusOK, changed.Status, "PUT own %s: %s", e.name, changed.Raw)
					assert.Equal(t, "Changed", column(t, e.table, e.column, ownID))

					before := column(t, e.table, e.column, foreignID)
					read := env.do(caller, http.MethodGet, e.path+"/"+uintPath(foreignID), nil)
					write := env.do(caller, http.MethodPut, e.path+"/"+uintPath(foreignID), e.body)
					if phase.own {
						assert.Equal(t, http.StatusForbidden, read.Status, "GET foreign %s: %s", e.name, read.Raw)
						assert.Equal(t, "FORBIDDEN", read.Code)
						assert.Equal(t, http.StatusForbidden, write.Status, "PUT foreign %s: %s", e.name, write.Raw)
						assert.Equal(t, "FORBIDDEN", write.Code)
						assert.Equal(t, before, column(t, e.table, e.column, foreignID), "a refused change writes nothing")
					} else {
						assert.Equal(t, http.StatusOK, read.Status, "GET foreign %s: %s", e.name, read.Raw)
						assert.Equal(t, http.StatusOK, write.Status, "PUT foreign %s: %s", e.name, write.Raw)
						assert.Equal(t, "Changed", column(t, e.table, e.column, foreignID))
					}
				}
			})

			t.Run("sub-lists", func(t *testing.T) {
				expectList(t, "/companies/"+uintPath(company.ID)+"/leads", visible(leads))
				expectList(t, "/customers/"+uintPath(customer.ID)+"/tickets", visible(tickets))
				expectList(t, "/customers/"+uintPath(customer.ID)+"/deals", visible(deals))
				expectList(t, "/customers/"+uintPath(customer.ID)+"/quotes", visible(quotes))
				expectList(t, "/companies/"+uintPath(company.ID)+"/deals", visible(deals))
				expectList(t, "/deals/"+uintPath(ownDeal.ID)+"/quotes", visible(quotesOnOwnDeal))

				onForeign := env.do(caller, http.MethodGet, "/deals/"+uintPath(foreignDeal.ID)+"/quotes?limit=100", nil)
				if phase.own {
					assert.Equal(t, http.StatusForbidden, onForeign.Status, onForeign.Raw)
				} else {
					require.Equal(t, http.StatusOK, onForeign.Status, onForeign.Raw)
					assert.ElementsMatch(t, quotesOnForeignDeal, onForeign.ids(t))
				}
			})

			t.Run("dashboard", func(t *testing.T) {
				// Open tickets and unfinished tasks: two of the caller's
				// three, all four of the other assignee's.
				wantOpenTickets, wantPendingTasks, wantConverted := int64(2), int64(2), int64(1)
				if !phase.own {
					wantOpenTickets, wantPendingTasks, wantConverted = 6, 6, 3
				}
				var stats struct {
					TotalLeads   int64 `json:"total_leads"`
					OpenTickets  int64 `json:"open_tickets"`
					PendingTasks int64 `json:"pending_tasks"`
				}
				require.NoError(t, json.Unmarshal(get(t, "/dashboard/stats").Data, &stats))
				assert.EqualValues(t, len(visible(leads)), stats.TotalLeads, "stats.total_leads")
				assert.Equal(t, wantOpenTickets, stats.OpenTickets, "stats.open_tickets")
				assert.Equal(t, wantPendingTasks, stats.PendingTasks, "stats.pending_tasks")

				assert.EqualValues(t, len(visible(leads)), chartSum(t, "/dashboard/leads-by-status"), "leads-by-status")
				assert.EqualValues(t, len(visible(tickets)), chartSum(t, "/dashboard/tickets-by-priority"), "tickets-by-priority")
				assert.EqualValues(t, len(visible(tasks)), chartSum(t, "/dashboard/tasks-by-status"), "tasks-by-status")
				assert.Equal(t, wantConverted, chartSum(t, "/dashboard/sales-performance?period=month"), "sales-performance")

				activities := get(t, "/dashboard/activities?limit=50").Raw
				for _, id := range leads.own {
					assert.Contains(t, activities, fmt.Sprintf(`"lead-%d-created"`, id))
				}
				for _, id := range tickets.own {
					assert.Contains(t, activities, fmt.Sprintf(`"ticket-%d-created"`, id))
				}
				assert.Contains(t, activities, fmt.Sprintf(`"task-%d-completed"`, tasks.own[2]))
				for _, foreign := range []struct {
					format string
					ids    []uint
				}{{`"lead-%d-created"`, leads.foreign}, {`"lead-%d-converted"`, leads.foreign[2:]}, {`"ticket-%d-created"`, tickets.foreign}} {
					for _, id := range foreign.ids {
						if phase.own {
							assert.NotContains(t, activities, fmt.Sprintf(foreign.format, id))
						} else {
							assert.Contains(t, activities, fmt.Sprintf(foreign.format, id))
						}
					}
				}

				// Upcoming tasks: the unfinished ones with a due date.
				upcoming := tasks.own[:2]
				if !phase.own {
					upcoming = append(append([]uint(nil), upcoming...), tasks.foreign...)
				}
				assert.ElementsMatch(t, upcoming, get(t, "/dashboard/upcoming-tasks?limit=50").ids(t), "upcoming-tasks")
				assert.ElementsMatch(t, visible(tickets), get(t, "/dashboard/recent-tickets?limit=50").ids(t), "recent-tickets")
				assert.ElementsMatch(t, visible(leads), get(t, "/dashboard/new-leads?limit=50").ids(t), "new-leads")

				var pipeline struct {
					Stages []struct {
						Stage string `json:"stage"`
						Count int64  `json:"count"`
					} `json:"stages"`
				}
				require.NoError(t, json.Unmarshal(get(t, "/dashboard/pipeline").Data, &pipeline))
				var inPipeline int64
				for _, stage := range pipeline.Stages {
					inPipeline += stage.Count
				}
				assert.EqualValues(t, len(visible(deals)), inPipeline, "pipeline: the deals counted")
			})

			t.Run("automation runs", func(t *testing.T) {
				expectList(t, "/automation/runs", visible(runs))
				assert.Equal(t, http.StatusOK, env.do(caller, http.MethodGet, "/automation/runs/"+uintPath(runs.own[0]), nil).Status)
				foreign := env.do(caller, http.MethodGet, "/automation/runs/"+uintPath(runs.foreign[0]), nil)
				if phase.own {
					assert.Equal(t, http.StatusNotFound, foreign.Status, foreign.Raw)
				} else {
					assert.Equal(t, http.StatusOK, foreign.Status, foreign.Raw)
				}
			})

			// The bulk routes are all-or-nothing: one foreign id refuses the
			// whole batch at scope own. The statuses are chosen so that no
			// counter asserted above moves between the phases.
			t.Run("bulk status", func(t *testing.T) {
				for _, bulk := range []struct {
					path, field, table, initial, status string
					records                             records
				}{
					{"/leads/bulk/status", "lead_ids", "leads", "new", "contacted", records{own: leads.own[:1], foreign: leads.foreign[:1]}},
					{"/tickets/bulk/status", "ticket_ids", "tickets", "open", "in_progress", records{own: tickets.own[:1], foreign: tickets.foreign[:1]}},
					{"/tasks/bulk/status", "task_ids", "tasks", "pending", "in_progress", records{own: tasks.own[:1], foreign: tasks.foreign[:1]}},
				} {
					ownID, foreignID := bulk.records.own[0], bulk.records.foreign[0]
					ownOnly := env.do(caller, http.MethodPost, bulk.path, map[string]interface{}{bulk.field: []uint{ownID}, "status": bulk.status})
					assert.Equal(t, http.StatusOK, ownOnly.Status, "%s on an own record: %s", bulk.path, ownOnly.Raw)
					assert.Equal(t, bulk.status, column(t, bulk.table, "status", ownID))

					// Put the own record back, then send it with a foreign one.
					require.NoError(t, env.db.Table(bulk.table).Where("id = ?", ownID).Update("status", bulk.initial).Error)
					foreignBefore := column(t, bulk.table, "status", foreignID)
					mixed := env.do(caller, http.MethodPost, bulk.path,
						map[string]interface{}{bulk.field: []uint{ownID, foreignID}, "status": bulk.status})
					if phase.own {
						assert.Equal(t, http.StatusForbidden, mixed.Status, "%s with a foreign record: %s", bulk.path, mixed.Raw)
						assert.Equal(t, "FORBIDDEN", mixed.Code)
						assert.Equal(t, foreignBefore, column(t, bulk.table, "status", foreignID), "the foreign record is unchanged")
						assert.Equal(t, bulk.initial, column(t, bulk.table, "status", ownID), "all or nothing: the own record is unchanged too")
						// Restore the status the dashboard assertions of the next phase count.
						require.NoError(t, env.db.Table(bulk.table).Where("id = ?", ownID).Update("status", bulk.status).Error)
					} else {
						assert.Equal(t, http.StatusOK, mixed.Status, "%s with a foreign record: %s", bulk.path, mixed.Raw)
						assert.Equal(t, bulk.status, column(t, bulk.table, "status", foreignID))
						assert.Equal(t, bulk.status, column(t, bulk.table, "status", ownID))
					}
				}
			})
		})
	}

	t.Run("without the key a scoped role reaches nothing", func(t *testing.T) {
		env.setGrants(role, "companies.view", "customers.view", "dashboard.view")
		for _, e := range entities {
			for _, request := range []struct{ method, path string }{
				{http.MethodGet, e.path}, {http.MethodGet, e.path + "/" + uintPath(e.records.own[0])},
				{http.MethodPut, e.path + "/" + uintPath(e.records.own[0])},
			} {
				refused := env.do(caller, request.method, request.path, e.body)
				assert.Equal(t, http.StatusForbidden, refused.Status, "%s %s on an own record: %s", request.method, request.path, statusText(refused))
			}
		}
		// The dashboard still loads and counts nothing of the kinds it lacks.
		var stats struct {
			TotalLeads   int64 `json:"total_leads"`
			OpenTickets  int64 `json:"open_tickets"`
			PendingTasks int64 `json:"pending_tasks"`
		}
		require.NoError(t, json.Unmarshal(get(t, "/dashboard/stats").Data, &stats))
		assert.Zero(t, stats.TotalLeads)
		assert.Zero(t, stats.OpenTickets)
		assert.Zero(t, stats.PendingTasks)
		assert.Empty(t, get(t, "/dashboard/new-leads").ids(t))
		assert.Empty(t, get(t, "/dashboard/recent-tickets").ids(t))
		assert.Empty(t, get(t, "/dashboard/upcoming-tasks").ids(t))
		assert.Equal(t, http.StatusForbidden, env.do(caller, http.MethodGet, "/dashboard/pipeline", nil).Status)
	})
}

func containsQuery(path string) bool {
	for _, r := range path {
		if r == '?' {
			return true
		}
	}
	return false
}
