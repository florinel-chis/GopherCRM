package main

// Deny by default on the real router (#259 AC-5, #347): a custom role without
// a single permission is refused on every protected route except the
// self-service allowlist, even on records it owns.
//
// The file also holds what the permission tests of this package share: a
// custom-role principal added to the route-matrix harness. The harness, its
// fixtures and its request table are used as they are and never changed.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
)

// matrixPrincipal is a seeded active user with role custom, a role row of its
// own and a Bearer token made like the harness tokens.
type matrixPrincipal struct {
	name string
	user *models.User
	role *models.Role
}

// addCustomPrincipal seeds a custom-role user and registers it with the
// harness under name, so h.send, h.target and the request-table bodies work
// for it as they do for a system role. Kinds without an owner, and the foreign
// records, are the ones the admin fixtures use. withOwnRecords also gives the
// principal its own record of every owned kind.
func addCustomPrincipal(t *testing.T, h *matrixHarness, name string, withOwnRecords bool) *matrixPrincipal {
	t.Helper()
	db := models.DB
	create := func(value interface{}) {
		t.Helper()
		require.NoError(t, db.Create(value).Error)
	}
	uid := func(id uint) string { return strconv.FormatUint(uint64(id), 10) }

	role := &models.Role{Name: "Matrix role " + name, Description: "Seeded by the permission tests"}
	require.NoError(t, repository.NewRoleRepository(db).Create(role))

	hash, err := bcrypt.GenerateFromPassword([]byte(routeMatrixPassword), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{Email: name + "@route-matrix.test", Password: string(hash), FirstName: "Matrix", LastName: name,
		Role: models.RoleCustom, RoleID: &role.ID, IsActive: true}
	create(user)
	h.fx.users[name] = user

	for _, set := range h.fx.sets {
		set.own[name] = set.own["admin"]
		set.foreign[name] = set.foreign["admin"]
	}
	// The own user is the principal itself; the foreign one stays otherSales.
	h.fx.sets["user"].own[name] = uid(user.ID)

	if withOwnRecords {
		own := func(kind string, id uint) uint {
			h.fx.sets[kind].own[name] = uid(id)
			return id
		}
		company := &models.Company{Name: "Matrix company " + name, Domain: name + ".route-matrix.test", OwnerID: &user.ID}
		create(company)
		own("company", company.ID)

		lead := &models.Lead{FirstName: "Lead", LastName: name, Email: "lead-" + name + "@route-matrix.test",
			Status: models.LeadStatusQualified, OwnerID: user.ID}
		create(lead)
		own("lead", lead.ID)

		customer := &models.Customer{FirstName: "Customer", LastName: name, Email: "customer-" + name + "@route-matrix.test",
			AssignedToID: &user.ID}
		create(customer)
		own("customer", customer.ID)
		mapping := &models.CustomerExternalID{CustomerID: customer.ID, Source: "matrix", ExternalID: "customer-" + name}
		create(mapping)
		own("customer_external_id", mapping.ID)

		deal := &models.Deal{Title: "Deal " + name, Stage: models.DealStageQualification, AmountCents: 100000, Currency: "EUR",
			Probability: 10, OwnerID: user.ID, CustomerID: &customer.ID, CompanyID: &company.ID}
		create(deal)
		own("deal", deal.ID)

		productID := h.fx.numeric("product", caseOwn, "admin")
		for kind, status := range map[string]models.QuoteStatus{"quote": models.QuoteStatusDraft, "quote_sent": models.QuoteStatusSent} {
			quote := &models.Quote{Title: "Quote " + name, Status: status, Currency: "EUR", SubtotalCents: 1000, TotalCents: 1000,
				GrossTotalCents: 1000, OwnerID: user.ID, CustomerID: &customer.ID, DealID: &deal.ID}
			if status == models.QuoteStatusSent {
				sentAt := db.NowFunc().UTC()
				quote.SentAt = &sentAt
			}
			create(quote)
			own(kind, quote.ID)
			line := &models.QuoteLine{QuoteID: quote.ID, ProductID: productID, Position: 1, Description: "Matrix line",
				Quantity: models.QuoteQuantityFromUnits(1), UnitPriceCents: 1000, LineTotalCents: 1000}
			create(line)
			if kind == "quote" {
				own("quote_line", line.ID)
			}
		}

		ticket := &models.Ticket{Title: "Ticket " + name, Description: "Seeded by the permission tests", Status: models.TicketStatusOpen,
			Priority: models.TicketPriorityMedium, CustomerID: customer.ID, AssignedToID: &user.ID}
		create(ticket)
		own("ticket", ticket.ID)

		task := &models.Task{Title: "Task " + name, Status: models.TaskStatusPending, Priority: models.TaskPriorityMedium,
			AssignedToID: user.ID}
		create(task)
		own("task", task.ID)

		key := &models.APIKey{Name: "Key " + name, KeyHash: "route-matrix-key-hash-" + name, Prefix: "gcrm_rm", UserID: user.ID, IsActive: true}
		create(key)
		own("api_key", key.ID)

		notification := &models.Notification{UserID: user.ID, Title: "Notification " + name, EntityType: "lead", EntityID: lead.ID}
		create(notification)
		own("notification", notification.ID)

		var rule models.AutomationRule
		require.NoError(t, db.First(&rule, h.fx.numeric("automation_rule", caseOwn, "admin")).Error)
		run := &models.AutomationRun{RuleID: rule.ID, RuleVersion: rule.Version, EntityType: "lead", EntityID: lead.ID,
			DedupeKey: "route-matrix-" + name, Status: models.AutomationRunPending, NextPosition: 1}
		inserted, err := repository.NewAutomationRepository(db).CreateRunIgnoringDuplicate(run)
		require.NoError(t, err)
		require.True(t, inserted)
		own("automation_run", run.ID)
	}

	// The token is signed like the harness tokens: same secret, same service.
	auth := service.NewAuthService(repository.NewUserRepository(db), repository.NewAPIKeyRepository(db), cfgForBoot(t, false).JWT)
	token, err := auth.GenerateJWT(user)
	require.NoError(t, err)
	h.tokens[name] = token

	return &matrixPrincipal{name: name, user: user, role: role}
}

// refreshMatrixSnapshot makes the rows seeded after the harness booted part
// of the state h.reset restores.
func refreshMatrixSnapshot(t *testing.T, h *matrixHarness) {
	t.Helper()
	for _, table := range append(append([]string(nil), h.tables...), "sqlite_sequence") {
		require.NoError(t, models.DB.Exec(fmt.Sprintf(`DELETE FROM "matrix_snapshot_%s"`, table)).Error)
		require.NoError(t, models.DB.Exec(fmt.Sprintf(`INSERT INTO "matrix_snapshot_%s" SELECT * FROM "%s"`, table, table)).Error)
	}
	h.changes = h.totalChanges()
}

// restoreMatrixState puts the seeded state back when anything was written.
func restoreMatrixState(h *matrixHarness) {
	if changes := h.totalChanges(); changes != h.changes {
		h.reset()
		h.changes = h.totalChanges()
	}
}

func TestZeroPermissionRoleIsDeniedEverywhere(t *testing.T) {
	h := bootRouteMatrix(t)
	zero := addCustomPrincipal(t, h, "zero", true)
	refreshMatrixSnapshot(t, h)

	stored, err := repository.NewRoleRepository(models.DB).GetByID(zero.role.ID)
	require.NoError(t, err)
	require.Empty(t, stored.Permissions, "the role holds no permission")

	// The principal's own records are what the request addresses: checked
	// here from the database, so a 403 below is a refusal on an owned record.
	for _, owned := range []struct{ kind, table, column string }{
		{"task", "tasks", "assigned_to_id"}, {"ticket", "tickets", "assigned_to_id"},
		{"lead", "leads", "owner_id"}, {"deal", "deals", "owner_id"}, {"quote", "quotes", "owner_id"},
		{"quote_sent", "quotes", "owner_id"}, {"company", "companies", "owner_id"}, {"customer", "customers", "assigned_to_id"},
		{"notification", "notifications", "user_id"}, {"api_key", "api_keys", "user_id"},
	} {
		var value uint
		require.NoError(t, models.DB.Raw(fmt.Sprintf("SELECT %s FROM %s WHERE id = ?", owned.column, owned.table),
			h.fx.numeric(owned.kind, caseOwn, zero.name)).Scan(&value).Error)
		assert.Equal(t, zero.user.ID, value, "%s own of the principal: %s.%s", owned.kind, owned.table, owned.column)
	}
	var runLead uint
	require.NoError(t, models.DB.Raw("SELECT entity_id FROM automation_runs WHERE id = ?",
		h.fx.numeric("automation_run", caseOwn, zero.name)).Scan(&runLead).Error)
	assert.Equal(t, h.fx.numeric("lead", caseOwn, zero.name), runLead, "the principal's automation run is on its own lead")

	// Only on these two routes is a foreign id used: the caller's own account
	// is self-service there, so the refusal is shown on another account.
	foreignID := map[string]bool{"GET /users/:id": true, "PUT /users/:id": true}

	denied, allowed := 0, 0
	routes := h.protectedRoutes()
	for _, route := range routes {
		request := requestFor(t, route)
		key := route.Method + " " + strings.TrimPrefix(route.Path, routeMatrixPrefix)
		name := caseOwn
		if foreignID[key] {
			name = caseForeign
		}
		cell := h.cell(route.Method, route.Path, request, zero.name, name)
		if permissions.SelfServiceRoutes[key] {
			allowed++
			assert.True(t, cell.Status >= 200 && cell.Status <= 299, "%s is self-service: want 2xx, got %d %q", key, cell.Status, cell.Code)
			continue
		}
		denied++
		if cell.Status != 403 || cell.Code != "FORBIDDEN" {
			t.Errorf("%s: a role without permissions must get 403 FORBIDDEN, got %d %q", key, cell.Status, cell.Code)
		}
	}
	assert.Len(t, routes, 168, "every protected route: the 157 of the matrix and the 11 added after its freeze")
	assert.Equal(t, len(permissions.SelfServiceRoutes), allowed, "the self-service routes")
	assert.Equal(t, len(routes)-len(permissions.SelfServiceRoutes), denied, "every other route is denied")

	// Every self-service entry is a served route.
	served := map[string]bool{}
	for _, route := range routes {
		served[route.Method+" "+strings.TrimPrefix(route.Path, routeMatrixPrefix)] = true
	}
	var selfService []string
	for key := range permissions.SelfServiceRoutes {
		selfService = append(selfService, key)
	}
	sort.Strings(selfService)
	for _, key := range selfService {
		assert.True(t, served[key], "permissions.SelfServiceRoutes names %s, which the router does not serve", key)
	}
}
