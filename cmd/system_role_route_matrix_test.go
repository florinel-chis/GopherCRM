package main

// The system-role route matrix (#322, part of #259): what the router that
// production builds answers to each of the four system roles on every
// protected route, recorded in testdata/system_role_route_matrix.json and
// replayed against the live router.
//
// The golden is written by the same harness that replays it, so
// TestRouteMatrixFixtureAnchors pins the fixtures on its own: it reads the
// seeded rows from the database and checks a hand-written table of documented
// ownership rules, without opening the golden.
//
// The golden is frozen: its generator
//
//	go test ./cmd -run '^TestSystemRoleRouteMatrix$' -update-route-matrix
//
// refuses once a route file uses a permission guard (#347), and the replay
// keeps running against the recorded cells. A protected route added after the
// freeze is therefore not recorded. It goes on routeMatrixAfterFreeze, gets
// its matrixRequests entry and its permissions.RouteKeys entry, and is covered
// by the deny-by-default and the own-key tests instead of the replay.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

var updateRouteMatrix = flag.Bool("update-route-matrix", false,
	"write cmd/testdata/system_role_route_matrix.json from the live router instead of comparing with it")

const (
	// routeMatrixBaseCommit is main at the point the golden was generated
	// from. Regenerate and re-stamp both together after a rebase.
	routeMatrixBaseCommit = "cddaf741dc84e690735a2d39d7b572946f7c8f34"
	routeMatrixGenerator  = "go test ./cmd -run '^TestSystemRoleRouteMatrix$' -update-route-matrix"
	routeMatrixGolden     = "testdata/system_role_route_matrix.json"
	routeMatrixPrefix     = "/api/v1"
	routeMatrixPassword   = "R0ute!Matrix-2026"

	caseOwn           = "own"
	caseForeign       = "foreign"
	caseMissing       = "missing"
	caseMalformed     = "malformed"
	caseMalformedBody = "malformed_body"

	matrixMissingID   = "999999"
	matrixMalformedID = "abc"
)

var (
	matrixRoles = []string{"admin", "sales", "support", "customer"}
	matrixCases = []string{caseOwn, caseForeign, caseMissing, caseMalformed, caseMalformedBody}
)

// routeMatrixPublic is every route the engine serves without the Auth
// middleware. A route is either here or in the golden.
var routeMatrixPublic = map[string]bool{
	"GET /health":                                true,
	"GET /api/v1/auth/registration":              true,
	"POST /api/v1/auth/register":                 true,
	"POST /api/v1/auth/login":                    true,
	"POST /api/v1/auth/refresh":                  true,
	"POST /api/v1/auth/password-reset":           true,
	"POST /api/v1/auth/password-reset/confirm":   true,
	"GET /api/v1/forms/public/embed.js":          true,
	"GET /api/v1/forms/public/confirm":           true,
	"POST /api/v1/forms/public/confirm":          true,
	"GET /api/v1/forms/public/:key":              true,
	"GET /api/v1/forms/public/:key/view":         true,
	"POST /api/v1/forms/public/:key/submissions": true,
	"GET /api/v1/quotes/public/response":         true,
	"POST /api/v1/quotes/public/response":        true,
}

// routeMatrixAfterFreeze is every protected route added after the golden was
// frozen: the role API of #348. These routes are served and guarded, but the
// golden cannot record them, so the replay and the recorded-versus-served
// comparison leave them out. TestRouteMatrixCoversEveryProtectedRoute holds
// the list to the router, the golden and permissions.RouteKeys.
var routeMatrixAfterFreeze = map[string]bool{
	"GET /api/v1/permissions/catalogue": true,
	"GET /api/v1/roles":                 true,
	"POST /api/v1/roles":                true,
	"GET /api/v1/roles/audit":           true,
	"GET /api/v1/roles/:id":             true,
	"PUT /api/v1/roles/:id":             true,
	"DELETE /api/v1/roles/:id":          true,
	"POST /api/v1/roles/:id/clone":      true,
}

// matrixRoleNameCounter makes every role name a request creates unique, so no
// cell answers ROLE_NAME_TAKEN whatever ran before it.
var matrixRoleNameCounter atomic.Uint32

// newMatrixRoleName is a role name no other request used: the calling
// principal's label plus a counter.
func newMatrixRoleName(c matrixCtx) string {
	return fmt.Sprintf("Matrix role %s %d", c.role, matrixRoleNameCounter.Add(1))
}

// ---------------------------------------------------------------------------
// Golden file

type matrixCell struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
}

type matrixRoute struct {
	Method string                           `json:"method"`
	Path   string                           `json:"path"`
	Note   string                           `json:"note"`
	Cells  map[string]map[string]matrixCell `json:"cells"`
}

func (r matrixRoute) key() string { return r.Method + " " + r.Path }

type matrixFile struct {
	BaseCommit string        `json:"base_commit"`
	Generator  string        `json:"generator"`
	Routes     []matrixRoute `json:"routes"`
}

// encodeRouteMatrix renders the golden: routes sorted by path then method,
// roles and cases in a fixed order, one cell per line so a route change reads
// as a short diff.
func encodeRouteMatrix(file matrixFile) []byte {
	routes := append([]matrixRoute(nil), file.Routes...)
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	quote := func(s string) string {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(s)
		return strings.TrimSuffix(buf.String(), "\n")
	}
	var out bytes.Buffer
	out.WriteString("{\n")
	fmt.Fprintf(&out, "  \"base_commit\": %s,\n", quote(file.BaseCommit))
	fmt.Fprintf(&out, "  \"generator\": %s,\n", quote(file.Generator))
	out.WriteString("  \"routes\": [\n")
	for i, route := range routes {
		out.WriteString("    {\n")
		fmt.Fprintf(&out, "      \"method\": %s,\n", quote(route.Method))
		fmt.Fprintf(&out, "      \"path\": %s,\n", quote(route.Path))
		fmt.Fprintf(&out, "      \"note\": %s,\n", quote(route.Note))
		out.WriteString("      \"cells\": {\n")
		var roles []string
		for _, role := range matrixRoles {
			if _, ok := route.Cells[role]; ok {
				roles = append(roles, role)
			}
		}
		for r, role := range roles {
			fmt.Fprintf(&out, "        %s: {\n", quote(role))
			var cases []string
			for _, name := range matrixCases {
				if _, ok := route.Cells[role][name]; ok {
					cases = append(cases, name)
				}
			}
			for c, name := range cases {
				cell := route.Cells[role][name]
				fmt.Fprintf(&out, "          %s: {\"status\": %d, \"code\": %s}", quote(name), cell.Status, quote(cell.Code))
				if c < len(cases)-1 {
					out.WriteString(",")
				}
				out.WriteString("\n")
			}
			out.WriteString("        }")
			if r < len(roles)-1 {
				out.WriteString(",")
			}
			out.WriteString("\n")
		}
		out.WriteString("      }\n    }")
		if i < len(routes)-1 {
			out.WriteString(",")
		}
		out.WriteString("\n")
	}
	out.WriteString("  ]\n}\n")
	return out.Bytes()
}

func loadRouteMatrixGolden(t *testing.T) matrixFile {
	t.Helper()
	raw, err := os.ReadFile(routeMatrixGolden)
	require.NoError(t, err, "the golden is missing; generate it with: %s", routeMatrixGenerator)
	var file matrixFile
	require.NoError(t, json.Unmarshal(raw, &file))
	return file
}

// diffRouteMatrix lists every difference between the recorded matrix and the
// observed one, naming route, role, case and both answers.
func diffRouteMatrix(want, got matrixFile) []string {
	var diffs []string
	observed := map[string]matrixRoute{}
	for _, route := range got.Routes {
		observed[route.key()] = route
	}
	recorded := map[string]bool{}
	for _, route := range want.Routes {
		recorded[route.key()] = true
		live, ok := observed[route.key()]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("%s: recorded, but the router no longer serves it", route.key()))
			continue
		}
		if route.Note != live.Note {
			diffs = append(diffs, fmt.Sprintf("%s: note: want %q, got %q", route.key(), route.Note, live.Note))
		}
		for _, role := range matrixRoles {
			for _, name := range matrixCases {
				wantCell, wantOK := route.Cells[role][name]
				gotCell, gotOK := live.Cells[role][name]
				switch {
				case wantOK && !gotOK:
					diffs = append(diffs, fmt.Sprintf("%s %s %s: recorded, but not observed", route.key(), role, name))
				case !wantOK && gotOK:
					diffs = append(diffs, fmt.Sprintf("%s %s %s: observed %d %q, but not recorded", route.key(), role, name, gotCell.Status, gotCell.Code))
				case wantOK && wantCell != gotCell:
					diffs = append(diffs, fmt.Sprintf("%s %s %s: want %d %q, got %d %q",
						route.key(), role, name, wantCell.Status, wantCell.Code, gotCell.Status, gotCell.Code))
				}
			}
		}
	}
	for _, route := range got.Routes {
		if !recorded[route.key()] {
			diffs = append(diffs, fmt.Sprintf("%s: served by the router, but not recorded", route.key()))
		}
	}
	return diffs
}

// ---------------------------------------------------------------------------
// Generator guard

// routeMatrixGuardFiles is where routes are registered: cmd/main.go and the
// non-test internal/handler/*routes*.go files (the glob also matches
// routes_test.go, which is left out).
func routeMatrixGuardFiles(root string) ([]string, error) {
	files := []string{filepath.Join(root, "cmd", "main.go")}
	matches, err := filepath.Glob(filepath.Join(root, "internal", "handler", "*routes*.go"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	for _, match := range matches {
		if strings.HasSuffix(match, "_test.go") {
			continue
		}
		files = append(files, match)
	}
	return files, nil
}

// checkNoPermissionGuards refuses when a route file uses a permission guard.
// The matrix characterises the system roles before permission guards exist;
// once one is in place a regenerated golden would record the new model
// instead, so only the replay may run from then on.
func checkNoPermissionGuards(root string) error {
	files, err := routeMatrixGuardFiles(root)
	if err != nil {
		return err
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read route file: %w", err)
		}
		for _, guard := range []string{"RequireAnyPermission(", "RequirePermission("} {
			if bytes.Contains(raw, []byte(guard)) {
				rel, relErr := filepath.Rel(root, file)
				if relErr != nil {
					rel = file
				}
				return fmt.Errorf("%s uses %s...): the system-role route matrix is frozen and is no longer regenerated", rel, guard)
			}
		}
	}
	return nil
}

// writeRouteMatrix is the generator's last step: the guard, then the file.
func writeRouteMatrix(root, goldenPath string, file matrixFile) error {
	if err := checkNoPermissionGuards(root); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(goldenPath, encodeRouteMatrix(file), 0o644)
}

// ---------------------------------------------------------------------------
// Request table

type matrixBody = map[string]interface{}

// matrixCtx is what a request body may depend on: the calling role and the
// seeded fixtures.
type matrixCtx struct {
	role string
	fx   *matrixFixtures
}

// own is the caller's own record of a fixture kind.
func (c matrixCtx) own(kind string) uint { return c.fx.numeric(kind, caseOwn, c.role) }

// user is the id of a seeded user.
func (c matrixCtx) user(name string) uint { return c.fx.users[name].ID }

// matrixRequest declares how one protected route is called.
//
//   - kind: the fixture set that fills the first path parameter.
//   - child: the fixture set that fills the second path parameter; the first
//     is then the caller's own parent in every case.
//   - body: the valid JSON body of the own request, for exactly the routes
//     whose handler binds a body (body: true in scripts/ci/apiroutes). Every
//     case sends it; malformed_body replaces it with invalid JSON.
//   - query: the query string of every case.
//   - note: why admin own is not 2xx, or what the cases mean where the
//     parameter is not a record id.
type matrixRequest struct {
	kind  string
	child string
	body  func(c matrixCtx) interface{}
	query string
	note  string
}

func fixedBody(body matrixBody) func(matrixCtx) interface{} {
	return func(matrixCtx) interface{} { return body }
}

const (
	noteConfigKey = "The parameter is a configuration key, not a record id: own is an existing key (general.company_name), " +
		"foreign a second existing key (ui.theme.primary_color), missing a well-formed key no row has, " +
		"malformed a key with characters no key uses; the handler rejects no key by its form, so malformed is looked up like missing."
	noteConfigCategory = "The parameter is a category name, not a record id: own is an existing category (general), " +
		"foreign a second one (leads), missing a well-formed name no entry has, malformed a name with characters no category uses; " +
		"the handler rejects no name and an unknown category is an empty list."
	noteQuoteLine  = "Two parameters: the quote is the caller's own quote in every case and the line id varies; foreign is an existing line of another user's quote."
	noteCustomerEx = "Two parameters: the customer is the caller's own customer record in every case and the mapping id varies; foreign is an existing mapping of another customer."
	noteProductEx  = "Two parameters: the product is the first seeded product in every case (products have no owner) and the mapping id varies; foreign is an existing mapping of the second product."
)

var (
	automationRuleBody = matrixBody{
		"name": "Matrix rule", "entity_type": "lead", "trigger_type": "lead_created",
		"steps": []matrixBody{{"step_type": "notify", "config": matrixBody{"title": "Hello", "recipient": "owner"}}},
	}
	formFields = []matrixBody{{"name": "email", "label": "Email", "type": "email", "required": true}}
	// formBody names the sales user as default owner: a form that creates
	// leads needs one.
	formBody = func(c matrixCtx) interface{} {
		return matrixBody{"name": "Matrix form (written)", "fields": formFields, "default_owner_id": c.user("sales")}
	}

	externalIDBody = matrixBody{"source": "matrix-new", "external_id": "new-1"}
)

// matrixRequests is keyed by method and path below the API prefix. Every
// protected route has exactly one entry.
var matrixRequests = map[string]matrixRequest{
	// auth (the two authenticated routes)
	"POST /auth/logout": {body: fixedBody(matrixBody{})},
	"POST /auth/change-password": {body: fixedBody(matrixBody{
		"current_password": routeMatrixPassword, "new_password": "N3w!R0ute-Matrix-2026"})},

	// users
	"POST /users": {body: fixedBody(matrixBody{"email": "new-user@route-matrix.test", "password": "Str0ng!Passw0rd",
		"first_name": "New", "last_name": "User", "role": "sales"})},
	"GET /users":     {},
	"GET /users/me":  {},
	"PUT /users/me":  {body: fixedBody(matrixBody{"first_name": "Renamed"})},
	"GET /users/:id": {kind: "user"},
	"PUT /users/:id": {kind: "user", body: fixedBody(matrixBody{"first_name": "Renamed"})},
	"DELETE /users/:id": {kind: "user",
		note: "Admin own is 400: own is the caller's own account and an administrator cannot delete itself; admin foreign shows the route succeeding."},
	"POST /users/:id/activate": {kind: "user"},
	"POST /users/:id/deactivate": {kind: "user",
		note: "Admin own is 400: own is the caller's own account and an administrator cannot deactivate itself; admin foreign shows the route succeeding."},

	// roles (after the freeze, see routeMatrixAfterFreeze). The role fixture
	// is an unassigned custom role without permissions, and every body asks
	// for no permission, so the escalation rule has nothing to refuse: a
	// caller holding roles.manage alone passes all four writes.
	"GET /permissions/catalogue": {},
	"GET /roles":                 {},
	"POST /roles": {body: func(c matrixCtx) interface{} {
		return matrixBody{"name": newMatrixRoleName(c), "permissions": []matrixBody{}}
	}},
	"GET /roles/audit": {},
	"GET /roles/:id":   {kind: "role"},
	"PUT /roles/:id": {kind: "role", body: func(c matrixCtx) interface{} {
		return matrixBody{"name": newMatrixRoleName(c), "permissions": []matrixBody{}}
	}},
	"DELETE /roles/:id": {kind: "role"},
	"POST /roles/:id/clone": {kind: "role", body: func(c matrixCtx) interface{} {
		return matrixBody{"name": newMatrixRoleName(c)}
	}},

	// leads
	// An admin must name the owner; every role names itself.
	"POST /leads": {body: func(c matrixCtx) interface{} {
		return matrixBody{"first_name": "New", "last_name": "Lead", "email": "new-lead@route-matrix.test", "owner_id": c.user(c.role)}
	}},
	"GET /leads":              {},
	"GET /leads/export":       {},
	"GET /leads/:id":          {kind: "lead"},
	"PUT /leads/:id":          {kind: "lead", body: fixedBody(matrixBody{"first_name": "Renamed"})},
	"DELETE /leads/:id":       {kind: "lead"},
	"POST /leads/:id/convert": {kind: "lead", body: fixedBody(matrixBody{"notes": "Converted by the matrix"})},
	// The new owner is a third user, so no caller assigns to itself.
	"POST /leads/:id/assign": {kind: "lead", body: func(c matrixCtx) interface{} {
		return matrixBody{"user_id": c.user("otherSales")}
	}},
	"POST /leads/bulk/status": {body: func(c matrixCtx) interface{} {
		return matrixBody{"lead_ids": []uint{c.own("lead")}, "status": "contacted"}
	}},

	// customers
	"POST /customers":       {body: fixedBody(matrixBody{"first_name": "New", "last_name": "Customer", "email": "new-customer@route-matrix.test"})},
	"GET /customers":        {},
	"GET /customers/export": {},
	"GET /customers/:id":    {kind: "customer"},
	"PUT /customers/:id":    {kind: "customer", body: fixedBody(matrixBody{"first_name": "Renamed"})},
	"DELETE /customers/:id": {kind: "customer"},
	"POST /customers/:id/assign": {kind: "customer", body: func(c matrixCtx) interface{} {
		return matrixBody{"user_id": c.user("otherSales")}
	}},
	"GET /customers/:id/deals":                      {kind: "customer"},
	"GET /customers/:id/quotes":                     {kind: "customer"},
	"GET /customers/:id/tickets":                    {kind: "customer"},
	"POST /customers/:id/external-ids":              {kind: "customer", body: fixedBody(externalIDBody)},
	"DELETE /customers/:id/external-ids/:mappingId": {kind: "customer", child: "customer_external_id", note: noteCustomerEx},

	// companies
	"POST /companies":              {body: fixedBody(matrixBody{"name": "New company"})},
	"GET /companies":               {},
	"GET /companies/:id":           {kind: "company"},
	"PUT /companies/:id":           {kind: "company", body: fixedBody(matrixBody{"name": "Renamed company"})},
	"DELETE /companies/:id":        {kind: "company"},
	"GET /companies/:id/customers": {kind: "company"},
	"GET /companies/:id/leads":     {kind: "company"},
	"GET /companies/:id/deals":     {kind: "company"},

	// deals
	"POST /deals":            {body: fixedBody(matrixBody{"title": "New deal", "amount_cents": 1000})},
	"GET /deals":             {},
	"GET /deals/pipeline":    {},
	"GET /deals/:id":         {kind: "deal"},
	"PUT /deals/:id":         {kind: "deal", body: fixedBody(matrixBody{"title": "Renamed deal", "amount_cents": 2000})},
	"DELETE /deals/:id":      {kind: "deal"},
	"POST /deals/:id/stage":  {kind: "deal", body: fixedBody(matrixBody{"stage": "proposal"})},
	"GET /deals/:id/history": {kind: "deal"},
	"GET /deals/:id/quotes":  {kind: "deal"},
	"GET /deals/:id/tasks":   {kind: "deal"},

	// products
	"POST /products":                               {body: fixedBody(matrixBody{"name": "New product", "unit_price_cents": 1000})},
	"GET /products":                                {},
	"GET /products/:id":                            {kind: "product"},
	"PUT /products/:id":                            {kind: "product", body: fixedBody(matrixBody{"name": "Renamed product"})},
	"DELETE /products/:id":                         {kind: "product"},
	"POST /products/:id/external-ids":              {kind: "product", body: fixedBody(externalIDBody)},
	"DELETE /products/:id/external-ids/:mappingId": {kind: "product", child: "product_external_id", note: noteProductEx},

	// quotes
	"POST /quotes": {body: func(c matrixCtx) interface{} {
		return matrixBody{"title": "New quote", "customer_id": c.own("customer")}
	}},
	"GET /quotes":        {},
	"GET /quotes/:id":    {kind: "quote"},
	"PUT /quotes/:id":    {kind: "quote", body: fixedBody(matrixBody{"title": "Renamed quote"})},
	"DELETE /quotes/:id": {kind: "quote"},
	"POST /quotes/:id/lines": {kind: "quote", body: func(c matrixCtx) interface{} {
		return matrixBody{"product_id": c.own("product"), "quantity": 1}
	}},
	"PUT /quotes/:id/lines/:lineId":    {kind: "quote", child: "quote_line", body: fixedBody(matrixBody{"quantity": 2}), note: noteQuoteLine},
	"DELETE /quotes/:id/lines/:lineId": {kind: "quote", child: "quote_line", note: noteQuoteLine},
	"POST /quotes/:id/status":          {kind: "quote", body: fixedBody(matrixBody{"status": "sent"})},
	"GET /quotes/:id/history":          {kind: "quote"},
	"POST /quotes/:id/email":           {kind: "quote_sent"},

	// tickets
	"POST /tickets": {body: func(c matrixCtx) interface{} {
		return matrixBody{"title": "New ticket", "description": "Opened by the matrix", "customer_id": c.own("customer")}
	}},
	"GET /tickets":               {},
	"GET /tickets/my":            {},
	"GET /tickets/:id":           {kind: "ticket"},
	"PUT /tickets/:id":           {kind: "ticket", body: fixedBody(matrixBody{"title": "Renamed ticket"})},
	"DELETE /tickets/:id":        {kind: "ticket"},
	"GET /tickets/:id/comments":  {kind: "ticket"},
	"POST /tickets/:id/comments": {kind: "ticket", body: fixedBody(matrixBody{"body": "A comment from the matrix"})},
	"POST /tickets/bulk/status": {body: func(c matrixCtx) interface{} {
		return matrixBody{"ticket_ids": []uint{c.own("ticket")}, "status": "in_progress"}
	}},

	// tasks
	"POST /tasks": {body: func(c matrixCtx) interface{} {
		return matrixBody{"title": "New task", "assigned_to_id": c.user(c.role)}
	}},
	"GET /tasks":          {},
	"GET /tasks/my":       {},
	"GET /tasks/upcoming": {},
	"GET /tasks/:id":      {kind: "task"},
	"PUT /tasks/:id":      {kind: "task", body: fixedBody(matrixBody{"title": "Renamed task"})},
	"DELETE /tasks/:id":   {kind: "task"},
	"POST /tasks/bulk/status": {body: func(c matrixCtx) interface{} {
		return matrixBody{"task_ids": []uint{c.own("task")}, "status": "in_progress"}
	}},

	// labels
	"GET /labels":        {},
	"POST /labels":       {body: fixedBody(matrixBody{"name": "New label", "color": "#112233"})},
	"PUT /labels/:id":    {kind: "label", body: fixedBody(matrixBody{"name": "Renamed label"})},
	"DELETE /labels/:id": {kind: "label"},

	// API keys
	"POST /api-keys":       {body: fixedBody(matrixBody{"name": "New key"})},
	"GET /api-keys":        {},
	"GET /api-keys/:id":    {kind: "api_key"},
	"PUT /api-keys/:id":    {kind: "api_key", body: fixedBody(matrixBody{"name": "Renamed key"})},
	"DELETE /api-keys/:id": {kind: "api_key"},

	// configurations
	"GET /configurations":                    {},
	"GET /configurations/ui":                 {},
	"GET /configurations/category/:category": {kind: "config_category", note: noteConfigCategory},
	"GET /configurations/:key":               {kind: "config_key", note: noteConfigKey},
	"PUT /configurations/:key":               {kind: "config_key", body: fixedBody(matrixBody{"value": "Matrix Ltd"}), note: noteConfigKey},
	"POST /configurations/:key/reset":        {kind: "config_key", note: noteConfigKey},

	// dashboard
	"GET /dashboard/stats":               {},
	"GET /dashboard/leads-by-status":     {},
	"GET /dashboard/tickets-by-priority": {},
	"GET /dashboard/tasks-by-status":     {},
	"GET /dashboard/sales-performance":   {},
	"GET /dashboard/activities":          {},
	"GET /dashboard/upcoming-tasks":      {},
	"GET /dashboard/recent-tickets":      {},
	"GET /dashboard/new-leads":           {},
	"GET /dashboard/pipeline":            {},

	// AEO
	"GET /aeo/profile":  {},
	"PUT /aeo/profile":  {body: fixedBody(matrixBody{"brand_name": "Matrix CRM"})},
	"GET /aeo/prompts":  {},
	"POST /aeo/prompts": {body: fixedBody(matrixBody{"prompts": []string{"Which CRM does the matrix recommend?"}})},
	"POST /aeo/prompts/generate": {body: fixedBody(matrixBody{"count": 3}),
		note: "Admin own is 503 PROVIDER_NOT_CONFIGURED: the harness pins every AEO provider key empty, so nothing leaves the process and the generation engine has no key."},
	"GET /aeo/prompts/:id/answers": {kind: "aeo_prompt"},
	"PUT /aeo/prompts/:id":         {kind: "aeo_prompt", body: fixedBody(matrixBody{"text": "A rewritten matrix prompt"})},
	"DELETE /aeo/prompts/:id":      {kind: "aeo_prompt"},
	"POST /aeo/prompts/:id/run": {kind: "aeo_prompt",
		note: "Admin own is 503 PROVIDERS_UNAVAILABLE: the harness pins every AEO provider key empty, so nothing leaves the process and no engine can run the prompt."},
	"POST /aeo/runs": {
		note: "Admin own is 503 PROVIDERS_UNAVAILABLE: the harness pins every AEO provider key empty, so nothing leaves the process and no engine can start a run."},
	"GET /aeo/runs":      {},
	"GET /aeo/runs/:id":  {kind: "aeo_run"},
	"GET /aeo/dashboard": {},
	"GET /aeo/citations": {},
	"GET /aeo/providers": {},
	"GET /aeo/export":    {},

	// forms
	"GET /forms":                       {},
	"POST /forms":                      {body: formBody},
	"GET /forms/deliveries":            {},
	"POST /forms/deliveries/:id/retry": {kind: "form_delivery"},
	"GET /forms/submissions/:id":       {kind: "form_submission"},
	"GET /forms/:id":                   {kind: "form"},
	"PUT /forms/:id":                   {kind: "form", body: formBody},
	"DELETE /forms/:id":                {kind: "form"},
	"GET /forms/:id/submissions":       {kind: "form"},

	// automation
	"POST /automation/rules": {body: fixedBody(automationRuleBody)},
	"POST /automation/rules/dry-run": {body: func(c matrixCtx) interface{} {
		return matrixBody{"rule": automationRuleBody, "entity_type": "lead", "entity_id": c.own("lead")}
	}},
	"GET /automation/rules":              {},
	"GET /automation/rules/:id":          {kind: "automation_rule"},
	"PUT /automation/rules/:id":          {kind: "automation_rule", body: fixedBody(automationRuleBody)},
	"DELETE /automation/rules/:id":       {kind: "automation_rule"},
	"POST /automation/rules/:id/enable":  {kind: "automation_rule"},
	"POST /automation/rules/:id/disable": {kind: "automation_rule"},
	"POST /automation/rules/:id/dry-run": {kind: "automation_rule", body: func(c matrixCtx) interface{} {
		return matrixBody{"entity_type": "lead", "entity_id": c.own("lead")}
	}},
	"GET /automation/runs":             {},
	"GET /automation/runs/:id":         {kind: "automation_run"},
	"POST /automation/runs/:id/cancel": {kind: "automation_run"},

	// notifications
	"GET /notifications":              {},
	"GET /notifications/unread-count": {},
	"POST /notifications/read-all":    {},
	"POST /notifications/:id/read":    {kind: "notification"},

	// webhooks
	"GET /webhooks/endpoints": {},
	"POST /webhooks/endpoints": {body: fixedBody(matrixBody{
		"url": "https://hooks.route-matrix.test/in", "event_types": []string{"lead.created"}, "is_active": false})},
	"GET /webhooks/endpoints/:id":    {kind: "webhook_endpoint"},
	"PUT /webhooks/endpoints/:id":    {kind: "webhook_endpoint", body: fixedBody(matrixBody{"event_types": []string{"ticket.created"}})},
	"DELETE /webhooks/endpoints/:id": {kind: "webhook_endpoint"},
	"GET /webhooks/deliveries":       {},
}

// routeMatrixCases is the case set a route's shape requires.
func routeMatrixCases(path string, hasBody bool) []string {
	cases := []string{caseOwn}
	if strings.Contains(path, ":") {
		cases = append(cases, caseForeign, caseMissing, caseMalformed)
	}
	if hasBody {
		cases = append(cases, caseMalformedBody)
	}
	return cases
}

// ---------------------------------------------------------------------------
// Fixtures

// matrixFixtureSet is the ids of one kind of record: per role the caller's
// own record and an existing record of somebody else, plus an id no row has
// and a malformed one.
type matrixFixtureSet struct {
	own       map[string]string
	foreign   map[string]string
	missing   string
	malformed string
}

type matrixFixtures struct {
	// users: the four callers (admin, sales, support, customer) and the
	// owners of the foreign records (otherSales, otherSupport, otherCustomer).
	users map[string]*models.User
	sets  map[string]*matrixFixtureSet
}

func (f *matrixFixtures) set(t testing.TB, kind string) *matrixFixtureSet {
	t.Helper()
	set, ok := f.sets[kind]
	require.True(t, ok, "no fixture set %q", kind)
	return set
}

// numeric is a seeded record id; it panics on a kind that has none, which is
// a mistake in the request table.
func (f *matrixFixtures) numeric(kind, name, role string) uint {
	set, ok := f.sets[kind]
	if !ok {
		panic("no fixture set " + kind)
	}
	raw := set.own[role]
	if name == caseForeign {
		raw = set.foreign[role]
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		panic(fmt.Sprintf("fixture %s %s %s is not a record id: %q", kind, name, role, raw))
	}
	return uint(id)
}

func newMatrixFixtureSet() *matrixFixtureSet {
	return &matrixFixtureSet{own: map[string]string{}, foreign: map[string]string{},
		missing: matrixMissingID, malformed: matrixMalformedID}
}

// seedRouteMatrix builds every fixture, in a fixed order so the ids are the
// same on every run. "own" is a record the caller owns or is assigned;
// "foreign" is a live record of another user (otherSales, or otherSupport for
// tickets); kinds without an owner have a first and a second record.
func seedRouteMatrix(t *testing.T) *matrixFixtures {
	t.Helper()
	db := models.DB
	create := func(value interface{}) {
		t.Helper()
		require.NoError(t, db.Create(value).Error)
	}
	uid := func(id uint) string { return strconv.FormatUint(uint64(id), 10) }
	fx := &matrixFixtures{users: map[string]*models.User{}, sets: map[string]*matrixFixtureSet{}}

	hash, err := bcrypt.GenerateFromPassword([]byte(routeMatrixPassword), bcrypt.MinCost)
	require.NoError(t, err)
	for _, u := range []struct {
		name string
		role models.UserRole
	}{
		{"admin", models.RoleAdmin}, {"sales", models.RoleSales}, {"support", models.RoleSupport}, {"customer", models.RoleCustomer},
		{"otherSales", models.RoleSales}, {"otherSupport", models.RoleSupport}, {"otherCustomer", models.RoleCustomer},
	} {
		user := &models.User{Email: strings.ToLower(u.name) + "@route-matrix.test", Password: string(hash),
			FirstName: "Matrix", LastName: u.name, Role: u.role, IsActive: true}
		create(user)
		fx.users[u.name] = user
	}
	otherSales, otherSupport, otherCustomer := fx.users["otherSales"], fx.users["otherSupport"], fx.users["otherCustomer"]

	// owned seeds one record per calling role plus one foreign record;
	// build receives "foreign" and otherSales for the latter.
	owned := func(kind string, build func(name string, owner *models.User) uint) {
		set := newMatrixFixtureSet()
		for _, role := range matrixRoles {
			set.own[role] = uid(build(role, fx.users[role]))
		}
		foreign := uid(build(caseForeign, otherSales))
		for _, role := range matrixRoles {
			set.foreign[role] = foreign
		}
		fx.sets[kind] = set
	}
	// shared is a kind without an owner: every role's own record is the
	// first, the foreign one the second.
	shared := func(kind string, first, second string) {
		set := newMatrixFixtureSet()
		for _, role := range matrixRoles {
			set.own[role] = first
			set.foreign[role] = second
		}
		fx.sets[kind] = set
	}
	own := func(kind, name string) uint {
		if name == caseForeign {
			return fx.numeric(kind, caseForeign, "admin")
		}
		return fx.numeric(kind, caseOwn, name)
	}

	// users: the caller's own account; the foreign one is otherSales.
	owned("user", func(name string, owner *models.User) uint { return owner.ID })

	owned("company", func(name string, owner *models.User) uint {
		company := &models.Company{Name: "Matrix company " + name, Domain: name + ".route-matrix.test", OwnerID: &owner.ID}
		create(company)
		return company.ID
	})

	owned("lead", func(name string, owner *models.User) uint {
		lead := &models.Lead{FirstName: "Lead", LastName: name, Email: "lead-" + name + "@route-matrix.test",
			Status: models.LeadStatusQualified, OwnerID: owner.ID}
		create(lead)
		return lead.ID
	})

	// customers: staff own the record assigned to them; the customer login
	// owns the record linked to it; the foreign record is assigned to
	// otherSales and linked to otherCustomer.
	owned("customer", func(name string, owner *models.User) uint {
		customer := &models.Customer{FirstName: "Customer", LastName: name, Email: "customer-" + name + "@route-matrix.test",
			AssignedToID: &owner.ID}
		switch name {
		case "customer":
			customer.UserID = &owner.ID
			customer.AssignedToID = &otherSales.ID
		case caseForeign:
			customer.UserID = &otherCustomer.ID
		}
		create(customer)
		return customer.ID
	})

	owned("deal", func(name string, owner *models.User) uint {
		customerID, companyID := own("customer", name), own("company", name)
		deal := &models.Deal{Title: "Deal " + name, Stage: models.DealStageQualification, AmountCents: 100000, Currency: "EUR",
			Probability: 10, OwnerID: owner.ID, CustomerID: &customerID, CompanyID: &companyID}
		create(deal)
		return deal.ID
	})

	first := &models.Product{Name: "Matrix product one", SKU: "MATRIX-1", UnitPriceCents: 1000, Currency: "EUR", IsActive: true}
	second := &models.Product{Name: "Matrix product two", SKU: "MATRIX-2", UnitPriceCents: 2000, Currency: "EUR", IsActive: true}
	create(first)
	create(second)
	shared("product", uid(first.ID), uid(second.ID))
	firstMapping := &models.ProductExternalID{ProductID: first.ID, Source: "matrix", ExternalID: "product-1"}
	secondMapping := &models.ProductExternalID{ProductID: second.ID, Source: "matrix", ExternalID: "product-2"}
	create(firstMapping)
	create(secondMapping)
	shared("product_external_id", uid(firstMapping.ID), uid(secondMapping.ID))

	owned("customer_external_id", func(name string, _ *models.User) uint {
		mapping := &models.CustomerExternalID{CustomerID: own("customer", name), Source: "matrix", ExternalID: "customer-" + name}
		create(mapping)
		return mapping.ID
	})

	// quotes: a draft with one line, and a sent one for the email route.
	quote := func(status models.QuoteStatus) func(string, *models.User) uint {
		return func(name string, owner *models.User) uint {
			customerID, dealID := own("customer", name), own("deal", name)
			q := &models.Quote{Title: "Quote " + name, Status: status, Currency: "EUR", SubtotalCents: 1000, TotalCents: 1000,
				GrossTotalCents: 1000, OwnerID: owner.ID, CustomerID: &customerID, DealID: &dealID}
			if status == models.QuoteStatusSent {
				sentAt := time.Now().UTC()
				q.SentAt = &sentAt
			}
			create(q)
			return q.ID
		}
	}
	owned("quote", quote(models.QuoteStatusDraft))
	owned("quote_line", func(name string, _ *models.User) uint {
		line := &models.QuoteLine{QuoteID: own("quote", name), ProductID: first.ID, Position: 1, Description: "Matrix line",
			Quantity: models.QuoteQuantityFromUnits(1), UnitPriceCents: 1000, LineTotalCents: 1000}
		create(line)
		return line.ID
	})
	owned("quote_sent", quote(models.QuoteStatusSent))
	for _, name := range append(append([]string(nil), matrixRoles...), caseForeign) {
		create(&models.QuoteLine{QuoteID: own("quote_sent", name), ProductID: first.ID, Position: 1, Description: "Matrix line",
			Quantity: models.QuoteQuantityFromUnits(1), UnitPriceCents: 1000, LineTotalCents: 1000})
	}

	// tickets: staff own the ticket assigned to them (on their own customer
	// record); the customer login owns a ticket of its linked customer record,
	// assigned to otherSupport; the foreign ticket belongs to the foreign
	// customer and is assigned to otherSupport.
	owned("ticket", func(name string, owner *models.User) uint {
		ticket := &models.Ticket{Title: "Ticket " + name, Description: "Seeded by the route matrix", Status: models.TicketStatusOpen,
			Priority: models.TicketPriorityMedium, CustomerID: own("customer", name), AssignedToID: &owner.ID}
		if name == "customer" || name == caseForeign {
			ticket.AssignedToID = &otherSupport.ID
		}
		create(ticket)
		create(&models.TicketComment{TicketID: ticket.ID, AuthorUserID: fx.users["admin"].ID, Body: "A seeded public comment"})
		return ticket.ID
	})

	owned("task", func(name string, owner *models.User) uint {
		task := &models.Task{Title: "Task " + name, Status: models.TaskStatusPending, Priority: models.TaskPriorityMedium,
			AssignedToID: owner.ID}
		create(task)
		return task.ID
	})

	labelOne := &models.Label{Name: "Matrix label one", Color: "#AA0000"}
	labelTwo := &models.Label{Name: "Matrix label two", Color: "#00AA00"}
	create(labelOne)
	create(labelTwo)
	shared("label", uid(labelOne.ID), uid(labelTwo.ID))

	owned("api_key", func(name string, owner *models.User) uint {
		key := &models.APIKey{Name: "Key " + name, KeyHash: "route-matrix-key-hash-" + name, Prefix: "gcrm_rm", UserID: owner.ID, IsActive: true}
		create(key)
		return key.ID
	})

	owned("notification", func(name string, owner *models.User) uint {
		notification := &models.Notification{UserID: owner.ID, Title: "Notification " + name, EntityType: "lead", EntityID: own("lead", name)}
		create(notification)
		return notification.ID
	})

	// AEO: a profile, two prompts and two finished runs; no owner.
	create(&models.AEOProfile{BrandName: "Matrix CRM"})
	promptOne := &models.AEOPrompt{Text: "Which CRM is the first matrix prompt about?", IsActive: true}
	promptTwo := &models.AEOPrompt{Text: "Which CRM is the second matrix prompt about?", IsActive: true}
	create(promptOne)
	create(promptTwo)
	shared("aeo_prompt", uid(promptOne.ID), uid(promptTwo.ID))
	finished := time.Now().UTC()
	runOne := &models.AEORun{Trigger: "manual", Status: "completed", StartedAt: finished, CompletedAt: &finished}
	runTwo := &models.AEORun{Trigger: "manual", Status: "completed", StartedAt: finished, CompletedAt: &finished}
	create(runOne)
	create(runTwo)
	shared("aeo_run", uid(runOne.ID), uid(runTwo.ID))

	// forms: two published forms, each with a confirmed submission and a
	// failed follow-up delivery.
	var formIDs, submissionIDs, deliveryIDs []string
	for _, name := range []string{"one", "two"} {
		form := &models.Form{Name: "Matrix form " + name, PublicID: "routematrix" + name, Status: models.FormStatusPublished,
			Fields:       []models.FormFieldDef{{Name: "email", Label: "Email", Type: "email", Required: true}},
			SubmitAction: "message", CreateLead: true, DefaultOwnerID: fx.users["sales"].ID, CreatedByID: fx.users["admin"].ID,
			FollowUpSubject: "Your download", FollowUpBody: "Here it is."}
		create(form)
		confirmed := time.Now().UTC()
		submission := &models.FormSubmission{FormID: form.ID, Data: map[string]string{"email": "visitor-" + name + "@route-matrix.test"},
			Email: "visitor-" + name + "@route-matrix.test", Status: models.FormSubmissionConfirmed, ConfirmedAt: &confirmed}
		create(submission)
		delivery := &models.FormMailDelivery{SubmissionID: submission.ID, FormID: form.ID, Kind: models.FormMailKindFollowUp,
			Status: models.FormMailStatusFailed, ErrorCode: models.FormMailErrorMailFailed, Attempts: 1, LastAttemptAt: &confirmed}
		create(delivery)
		formIDs, submissionIDs, deliveryIDs = append(formIDs, uid(form.ID)), append(submissionIDs, uid(submission.ID)), append(deliveryIDs, uid(delivery.ID))
	}
	shared("form", formIDs[0], formIDs[1])
	shared("form_submission", submissionIDs[0], submissionIDs[1])
	shared("form_delivery", deliveryIDs[0], deliveryIDs[1])

	// automation: two disabled rules (no owner) and one pending run per lead,
	// so a run is own or foreign as its lead is.
	automationRepo := repository.NewAutomationRepository(db)
	var ruleIDs []string
	var rules []*models.AutomationRule
	for _, name := range []string{"one", "two"} {
		rule := &models.AutomationRule{Name: "Matrix rule " + name, EntityType: "lead", TriggerType: "lead_created", Enabled: false, Version: 1,
			CreatedByID: &fx.users["admin"].ID,
			Steps: []models.AutomationStep{{StepType: models.AutomationStepNotify,
				Config: models.AutomationStepConfig{Title: "Hello", Recipient: models.AutomationRecipientOwner}}}}
		require.NoError(t, automationRepo.CreateRule(rule))
		rules = append(rules, rule)
		ruleIDs = append(ruleIDs, uid(rule.ID))
	}
	shared("automation_rule", ruleIDs[0], ruleIDs[1])
	owned("automation_run", func(name string, _ *models.User) uint {
		run := &models.AutomationRun{RuleID: rules[0].ID, RuleVersion: rules[0].Version, EntityType: "lead", EntityID: own("lead", name),
			DedupeKey: "route-matrix-" + name, Status: models.AutomationRunPending, NextPosition: 1}
		inserted, err := automationRepo.CreateRunIgnoringDuplicate(run)
		require.NoError(t, err)
		require.True(t, inserted)
		return run.ID
	})

	// webhooks: two inactive endpoints, so no event of a cell is ever queued
	// for delivery.
	hookOne := &models.WebhookEndpoint{URL: "https://hooks.route-matrix.test/one", EventTypes: models.WebhookEventTypes{models.WebhookEventLeadCreated},
		SecretSealed: "route-matrix-unused-secret", IsActive: false}
	hookTwo := &models.WebhookEndpoint{URL: "https://hooks.route-matrix.test/two", EventTypes: models.WebhookEventTypes{models.WebhookEventTicketCreated},
		SecretSealed: "route-matrix-unused-secret", IsActive: false}
	create(hookOne)
	create(hookTwo)
	shared("webhook_endpoint", uid(hookOne.ID), uid(hookTwo.ID))

	// roles: two unassigned custom roles without permissions. They are part
	// of the snapshot, so the restore after a cell that renamed or deleted
	// one puts it back under the same id, and removes every role a cell
	// created or cloned.
	roleRepo := repository.NewRoleRepository(db)
	roleOne := &models.Role{Name: "Matrix role fixture one", Description: "Seeded by the route matrix"}
	roleTwo := &models.Role{Name: "Matrix role fixture two", Description: "Seeded by the route matrix"}
	require.NoError(t, roleRepo.Create(roleOne))
	require.NoError(t, roleRepo.Create(roleTwo))
	shared("role", uid(roleOne.ID), uid(roleTwo.ID))

	// configurations: keys and categories, not record ids.
	keys := newMatrixFixtureSet()
	categories := newMatrixFixtureSet()
	for _, role := range matrixRoles {
		keys.own[role], keys.foreign[role] = "general.company_name", "ui.theme.primary_color"
		categories.own[role], categories.foreign[role] = "general", "leads"
	}
	keys.missing, keys.malformed = "route.matrix.missing", "route!matrix~malformed"
	categories.missing, categories.malformed = "nosuchcategory", "route!matrix~malformed"
	fx.sets["config_key"], fx.sets["config_category"] = keys, categories

	return fx
}

// ---------------------------------------------------------------------------
// Harness

// matrixRemoteCounter numbers the requests of the whole test process: the
// authenticated rate tier is live in a normal build and keys on the client
// address, so every request gets an address of its own.
var matrixRemoteCounter atomic.Uint32

func nextMatrixRemoteAddr() string {
	n := matrixRemoteCounter.Add(1)
	return fmt.Sprintf("10.%d.%d.%d:40000", (n>>16)&0xff, (n>>8)&0xff, n&0xff)
}

type matrixHarness struct {
	t      *testing.T
	router *gin.Engine
	fx     *matrixFixtures
	tokens map[string]string
	// tables is every table of the schema; reset restores each from its
	// snapshot copy.
	tables  []string
	changes int64
}

// bootRouteMatrix builds the production router over a seeded SQLite file with
// nothing that could leave the process: no SMTP host, no reCAPTCHA keys, no
// AEO provider keys or egress origins.
func bootRouteMatrix(t *testing.T) *matrixHarness {
	t.Helper()
	for _, name := range []string{
		"SMTP_HOST", "RECAPTCHA_SITE_KEY", "RECAPTCHA_SECRET_KEY",
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "MOONSHOT_API_KEY", "PERPLEXITY_API_KEY",
		"AEO_CUSTOM_NAME", "AEO_CUSTOM_BASE_URL", "AEO_CUSTOM_MODEL", "AEO_CUSTOM_API_KEY",
		"AEO_EGRESS_ALLOWED_ORIGINS", "AEO_EGRESS_LOCAL_ORIGINS",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("DISABLE_RATE_LIMIT", "true")
	boot := bootAutomation(t, false)
	utils.Logger.SetOutput(io.Discard)
	require.Equal(t, routeMatrixPrefix, boot.cfg.API.Prefix)

	h := &matrixHarness{t: t, tokens: map[string]string{}}
	h.fx = seedRouteMatrix(t)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})
	h.router = router

	auth := service.NewAuthService(repository.NewUserRepository(models.DB), repository.NewAPIKeyRepository(models.DB), boot.cfg.JWT)
	for _, role := range matrixRoles {
		token, err := auth.GenerateJWT(h.fx.users[role])
		require.NoError(t, err)
		h.tokens[role] = token
	}
	h.snapshot()
	return h
}

// snapshot copies every table once, after seeding and the router's boot.
func (h *matrixHarness) snapshot() {
	h.t.Helper()
	require.NoError(h.t, models.DB.Raw(
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name").Scan(&h.tables).Error)
	require.NotEmpty(h.t, h.tables)
	for _, table := range append(append([]string(nil), h.tables...), "sqlite_sequence") {
		require.NoError(h.t, models.DB.Exec(fmt.Sprintf(`CREATE TABLE "matrix_snapshot_%s" AS SELECT * FROM "%s"`, table, table)).Error)
	}
	h.changes = h.totalChanges()
}

// totalChanges is SQLite's count of rows written on the pool's single
// connection; a cell that leaves it unchanged wrote nothing.
func (h *matrixHarness) totalChanges() int64 {
	h.t.Helper()
	var changes int64
	require.NoError(h.t, models.DB.Raw("SELECT total_changes()").Scan(&changes).Error)
	return changes
}

// reset puts every table back to the seeded state, row ids and
// AUTOINCREMENT counters included, in one transaction on one connection with
// foreign-key enforcement off for its duration.
func (h *matrixHarness) reset() {
	h.t.Helper()
	ctx := context.Background()
	sqlDB, err := models.DB.DB()
	require.NoError(h.t, err)
	conn, err := sqlDB.Conn(ctx)
	require.NoError(h.t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF")
	require.NoError(h.t, err)
	defer func() {
		_, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON")
		require.NoError(h.t, err)
	}()
	tx, err := conn.BeginTx(ctx, nil)
	require.NoError(h.t, err)
	for _, table := range append(append([]string(nil), h.tables...), "sqlite_sequence") {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM "%s"`, table)); err != nil {
			break
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO "%s" SELECT * FROM "matrix_snapshot_%s"`, table, table)); err != nil {
			break
		}
	}
	if err != nil {
		_ = tx.Rollback()
		require.NoError(h.t, err, "restore the seeded state")
	}
	require.NoError(h.t, tx.Commit())
}

// send performs one request as a role and returns the status and the
// envelope's error code ("" on success or a body that is not an envelope).
func (h *matrixHarness) send(role, method, target string, body []byte) matrixCell {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	request.RemoteAddr = nextMatrixRemoteAddr()
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+h.tokens[role])
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)

	cell := matrixCell{Status: recorder.Code}
	var envelope struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(recorder.Body.Bytes(), &envelope) == nil && envelope.Error != nil {
		cell.Code = envelope.Error.Code
	}

	return cell
}

// target is the request path of one case of a route.
func (h *matrixHarness) target(path string, request matrixRequest, role, name string) string {
	h.t.Helper()
	pick := func(kind, which string) string {
		set := h.fx.set(h.t, kind)
		switch which {
		case caseForeign:
			return set.foreign[role]
		case caseMissing:
			return set.missing
		case caseMalformed:
			return set.malformed
		default:
			return set.own[role]
		}
	}
	segments := strings.Split(path, "/")
	parameter := 0
	for i, segment := range segments {
		if !strings.HasPrefix(segment, ":") {
			continue
		}
		parameter++
		switch {
		case parameter == 1 && request.child != "":
			// The parent of a two-parameter route is always the caller's own.
			segments[i] = pick(request.kind, caseOwn)
		case parameter == 1:
			require.NotEmpty(h.t, request.kind, "%s has a parameter but no fixture kind", path)
			segments[i] = pick(request.kind, name)
		case parameter == 2:
			require.NotEmpty(h.t, request.child, "%s has two parameters but no child fixture kind", path)
			segments[i] = pick(request.child, name)
		default:
			h.t.Fatalf("%s has more than two parameters", path)
		}
	}
	target := strings.Join(segments, "/")
	if request.query != "" {
		target += "?" + request.query
	}
	return target
}

// cell runs one case of a route as one role against the seeded state and
// restores that state when the request wrote anything.
func (h *matrixHarness) cell(method, path string, request matrixRequest, role, name string) matrixCell {
	h.t.Helper()
	which := name
	if name == caseMalformedBody {
		which = caseOwn
	}
	var body []byte
	if request.body != nil {
		var err error
		body, err = json.Marshal(request.body(matrixCtx{role: role, fx: h.fx}))
		require.NoError(h.t, err)
	}
	if name == caseMalformedBody {
		body = []byte(`{"malformed":`)
	}
	cell := h.send(role, method, h.target(path, request, role, which), body)
	if cell.Status == http.StatusUnauthorized || cell.Status == http.StatusTooManyRequests {
		h.t.Fatalf("harness failure: %s %s as %s (%s) answered %d %q; an authenticated cell is never 401 or 429",
			method, path, role, name, cell.Status, cell.Code)
	}
	if changes := h.totalChanges(); changes != h.changes {
		h.reset()
		h.changes = h.totalChanges()
	}
	return cell
}

// protectedRoutes is the engine's route list minus the public list.
func (h *matrixHarness) protectedRoutes() []gin.RouteInfo {
	var routes []gin.RouteInfo
	for _, route := range h.router.Routes() {
		if routeMatrixPublic[route.Method+" "+route.Path] {
			continue
		}
		routes = append(routes, route)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes
}

// requestFor is the request-table entry of a served route.
func requestFor(t *testing.T, route gin.RouteInfo) matrixRequest {
	t.Helper()
	require.True(t, strings.HasPrefix(route.Path, routeMatrixPrefix+"/"),
		"%s %s is outside the API prefix: add it to the public list or mount it under %s", route.Method, route.Path, routeMatrixPrefix)
	request, ok := matrixRequests[route.Method+" "+strings.TrimPrefix(route.Path, routeMatrixPrefix)]
	require.True(t, ok, "%s %s has no entry in matrixRequests: declare how it is called (or list it as public), then regenerate with: %s",
		route.Method, route.Path, routeMatrixGenerator)
	return request
}

// observe runs every cell of every protected route the golden can record:
// the routes added after the freeze are left out.
func (h *matrixHarness) observe() matrixFile {
	h.t.Helper()
	file := matrixFile{BaseCommit: routeMatrixBaseCommit, Generator: routeMatrixGenerator}
	for _, route := range h.protectedRoutes() {
		if routeMatrixAfterFreeze[route.Method+" "+route.Path] {
			continue
		}
		request := requestFor(h.t, route)
		entry := matrixRoute{Method: route.Method, Path: route.Path, Note: request.note, Cells: map[string]map[string]matrixCell{}}
		for _, role := range matrixRoles {
			entry.Cells[role] = map[string]matrixCell{}
			for _, name := range routeMatrixCases(route.Path, request.body != nil) {
				entry.Cells[role][name] = h.cell(route.Method, route.Path, request, role, name)
			}
		}
		file.Routes = append(file.Routes, entry)
	}
	return file
}

// ---------------------------------------------------------------------------
// Tests

// TestSystemRoleRouteMatrix replays every recorded cell against the router
// production builds. With -update-route-matrix it writes the golden instead,
// unless a route file already uses a permission guard.
func TestSystemRoleRouteMatrix(t *testing.T) {
	h := bootRouteMatrix(t)
	observed := h.observe()

	if *updateRouteMatrix {
		for _, route := range observed.Routes {
			status := route.Cells["admin"][caseOwn].Status
			if (status < 200 || status > 299) && route.Note == "" {
				t.Errorf("%s: admin own answers %d and the route has no note: give the own request a valid body, or say in the note why it cannot succeed", route.key(), status)
			}
		}
		if t.Failed() {
			t.Fatal("the golden was not written")
		}
		require.NoError(t, writeRouteMatrix("..", routeMatrixGolden, observed))
		t.Logf("wrote %s (%d routes)", routeMatrixGolden, len(observed.Routes))
		return
	}

	diffs := diffRouteMatrix(loadRouteMatrixGolden(t), observed)
	for _, diff := range diffs {
		t.Error(diff)
	}
	if len(diffs) > 0 {
		t.Logf("%d cell(s) differ from %s. If the change is intended, regenerate with: %s", len(diffs), routeMatrixGolden, routeMatrixGenerator)
	}
}

// TestRouteMatrixCoversEveryProtectedRoute holds the golden, the request
// table and the router's route list to one another.
func TestRouteMatrixCoversEveryProtectedRoute(t *testing.T) {
	h := bootRouteMatrix(t)
	golden := loadRouteMatrixGolden(t)

	// served is every protected route the golden records; afterFreeze the
	// protected routes added since, which every one of them still has a
	// request-table entry for.
	served := map[string]matrixRequest{}
	afterFreeze := map[string]bool{}
	for _, route := range h.protectedRoutes() {
		request := requestFor(t, route)
		if key := route.Method + " " + route.Path; routeMatrixAfterFreeze[key] {
			afterFreeze[key] = true
		} else {
			served[key] = request
		}
	}
	for key := range matrixRequests {
		method, path, _ := strings.Cut(key, " ")
		full := method + " " + routeMatrixPrefix + path
		_, ok := served[full]
		assert.True(t, ok || afterFreeze[full], "matrixRequests declares %s, which the router does not serve", key)
	}

	// The after-freeze list names served, guarded routes the golden does not
	// hold: each has a permissions.RouteKeys entry, so the own-key test and
	// the deny-by-default test cover it.
	goldenKeys := map[string]bool{}
	for _, route := range golden.Routes {
		goldenKeys[route.key()] = true
	}
	for key := range routeMatrixAfterFreeze {
		assert.True(t, afterFreeze[key], "routeMatrixAfterFreeze names %s, which is not a protected route of the router", key)
		assert.False(t, goldenKeys[key], "%s is on routeMatrixAfterFreeze and in the golden: a route belongs to one of them", key)
		method, path, _ := strings.Cut(key, " ")
		_, keyed := permissions.RouteKeys[method+" "+strings.TrimPrefix(path, routeMatrixPrefix)]
		assert.True(t, keyed, "%s is on routeMatrixAfterFreeze without a permissions.RouteKeys entry", key)
	}
	assert.Len(t, routeMatrixAfterFreeze, 8)
	for key := range routeMatrixPublic {
		found := false
		for _, route := range h.router.Routes() {
			if route.Method+" "+route.Path == key {
				found = true
			}
		}
		assert.True(t, found, "the public list names %s, which the router does not serve", key)
	}

	recorded := map[string]bool{}
	for _, route := range golden.Routes {
		recorded[route.key()] = true
		request, ok := served[route.key()]
		if !assert.True(t, ok, "%s is recorded, but is not a protected route of the router", route.key()) {
			continue
		}
		want := routeMatrixCases(route.Path, request.body != nil)
		sort.Strings(want)
		var roles []string
		for role, cells := range route.Cells {
			roles = append(roles, role)
			var got []string
			for name := range cells {
				got = append(got, name)
			}
			sort.Strings(got)
			assert.Equal(t, want, got, "%s %s: the cases its shape requires", route.key(), role)
		}
		sort.Strings(roles)
		assert.Equal(t, []string{"admin", "customer", "sales", "support"}, roles, "%s: the four system roles", route.key())
	}
	for key := range served {
		assert.True(t, recorded[key], "%s is a protected route, but is not recorded; regenerate with: %s", key, routeMatrixGenerator)
	}
	assert.Len(t, golden.Routes, len(served))
}

// TestRouteMatrixFixtureAnchors proves, without reading the golden, that the
// fixtures are what their case names claim: first from the database, then
// through a hand-written table of documented ownership rules sent to the
// live router. Extend the table by hand; never derive it from the golden.
func TestRouteMatrixFixtureAnchors(t *testing.T) {
	h := bootRouteMatrix(t)
	fx := h.fx

	t.Run("fixtures are what their case names claim", func(t *testing.T) {
		column := func(table, name string, id uint) (value *uint, live bool) {
			t.Helper()
			var rows []struct{ Value *uint }
			softDelete := " AND deleted_at IS NULL"
			if table == "notifications" || table == "quote_lines" || table == "customer_external_ids" ||
				table == "product_external_ids" || table == "automation_runs" {
				softDelete = ""
			}
			require.NoError(t, models.DB.Raw(fmt.Sprintf("SELECT %s AS value FROM %s WHERE id = ?%s", name, table, softDelete), id).Scan(&rows).Error)
			if len(rows) != 1 {
				return nil, false
			}
			return rows[0].Value, true
		}
		is := func(value *uint, want uint) bool { return value != nil && *value == want }

		// Owner or assignee columns: own is the caller's, foreign is a live
		// row of somebody else.
		for _, owned := range []struct {
			kind, table, column string
			roles               []string
		}{
			{"lead", "leads", "owner_id", matrixRoles},
			{"deal", "deals", "owner_id", matrixRoles},
			{"quote", "quotes", "owner_id", matrixRoles},
			{"quote_sent", "quotes", "owner_id", matrixRoles},
			{"company", "companies", "owner_id", matrixRoles},
			{"task", "tasks", "assigned_to_id", matrixRoles},
			{"api_key", "api_keys", "user_id", matrixRoles},
			{"notification", "notifications", "user_id", matrixRoles},
			{"user", "users", "id", matrixRoles},
			// Staff own what is assigned to them; the customer login is
			// checked through its linked customer record below.
			{"ticket", "tickets", "assigned_to_id", []string{"admin", "sales", "support"}},
			{"customer", "customers", "assigned_to_id", []string{"admin", "sales", "support"}},
		} {
			for _, role := range owned.roles {
				caller := fx.users[role].ID
				value, live := column(owned.table, owned.column, fx.numeric(owned.kind, caseOwn, role))
				assert.True(t, live && is(value, caller), "%s own of %s: %s.%s is the caller", owned.kind, role, owned.table, owned.column)
				value, live = column(owned.table, owned.column, fx.numeric(owned.kind, caseForeign, role))
				assert.True(t, live, "%s foreign of %s is a live row", owned.kind, role)
				assert.False(t, is(value, caller), "%s foreign of %s: %s.%s is another user", owned.kind, role, owned.table, owned.column)
			}
		}

		// The customer login: its linked customer record is own, and that
		// record's ticket is own; the foreign ones belong to another login.
		login := fx.users["customer"].ID
		linked := fx.numeric("customer", caseOwn, "customer")
		value, live := column("customers", "user_id", linked)
		assert.True(t, live && is(value, login), "customer own of customer is the record linked to the login")
		value, live = column("customers", "user_id", fx.numeric("customer", caseForeign, "customer"))
		assert.True(t, live, "customer foreign of customer is a live row")
		assert.False(t, is(value, login), "customer foreign of customer is not linked to the login")
		value, live = column("tickets", "customer_id", fx.numeric("ticket", caseOwn, "customer"))
		assert.True(t, live && is(value, linked), "ticket own of customer belongs to the linked customer record")
		value, live = column("tickets", "customer_id", fx.numeric("ticket", caseForeign, "customer"))
		assert.True(t, live, "ticket foreign of customer is a live row")
		assert.False(t, is(value, linked), "ticket foreign of customer belongs to another customer record")
		// The foreign ticket is nobody's: not assigned to a caller either.
		value, _ = column("tickets", "assigned_to_id", fx.numeric("ticket", caseForeign, "customer"))
		for _, role := range matrixRoles {
			assert.False(t, is(value, fx.users[role].ID), "ticket foreign is not assigned to %s", role)
		}

		// Automation runs follow their lead.
		for _, role := range matrixRoles {
			value, live = column("automation_runs", "entity_id", fx.numeric("automation_run", caseOwn, role))
			assert.True(t, live && is(value, fx.numeric("lead", caseOwn, role)), "automation_run own of %s runs on the caller's own lead", role)
			value, live = column("automation_runs", "entity_id", fx.numeric("automation_run", caseForeign, role))
			assert.True(t, live && is(value, fx.numeric("lead", caseForeign, role)), "automation_run foreign of %s runs on the foreign lead", role)
		}

		// Two-parameter routes: the own child hangs off the caller's own
		// parent, the foreign child off another parent.
		for _, child := range []struct {
			kind, table, column, parent string
		}{
			{"quote_line", "quote_lines", "quote_id", "quote"},
			{"customer_external_id", "customer_external_ids", "customer_id", "customer"},
			{"product_external_id", "product_external_ids", "product_id", "product"},
		} {
			for _, role := range matrixRoles {
				parent := fx.numeric(child.parent, caseOwn, role)
				value, live = column(child.table, child.column, fx.numeric(child.kind, caseOwn, role))
				assert.True(t, live && is(value, parent), "%s own of %s belongs to the caller's own %s", child.kind, role, child.parent)
				value, live = column(child.table, child.column, fx.numeric(child.kind, caseForeign, role))
				assert.True(t, live, "%s foreign of %s is an existing row", child.kind, role)
				assert.False(t, is(value, parent), "%s foreign of %s belongs to another %s", child.kind, role, child.parent)
			}
		}

		// Kinds without an owner: two distinct live rows.
		tables := map[string]string{
			"user": "users", "lead": "leads", "customer": "customers", "company": "companies", "deal": "deals",
			"product": "products", "product_external_id": "product_external_ids", "customer_external_id": "customer_external_ids",
			"quote": "quotes", "quote_sent": "quotes", "quote_line": "quote_lines", "ticket": "tickets", "task": "tasks",
			"label": "labels", "api_key": "api_keys", "notification": "notifications", "aeo_prompt": "aeo_prompts",
			"aeo_run": "aeo_runs", "form": "forms", "form_submission": "form_submissions", "form_delivery": "form_mail_deliveries",
			"automation_rule": "automation_rules", "automation_run": "automation_runs", "webhook_endpoint": "webhook_endpoints",
			"role": "roles",
		}
		for kind, set := range fx.sets {
			table, numeric := tables[kind]
			if !numeric {
				continue
			}
			for _, role := range matrixRoles {
				assert.NotEqual(t, set.own[role], set.foreign[role], "%s: own and foreign of %s are different records", kind, role)
				for _, id := range []string{set.own[role], set.foreign[role]} {
					var count int64
					require.NoError(t, models.DB.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ?", table), id).Scan(&count).Error)
					assert.EqualValues(t, 1, count, "%s %s of %s exists", kind, id, role)
				}
			}
			// missing: no row at all, soft-deleted ones included (raw SQL
			// applies no soft-delete scope).
			var count int64
			require.NoError(t, models.DB.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ?", table), set.missing).Scan(&count).Error)
			assert.Zero(t, count, "%s missing id %s has no row", kind, set.missing)
			_, err := strconv.ParseUint(set.malformed, 10, 64)
			assert.Error(t, err, "%s malformed id is not numeric", kind)
		}
		assert.Len(t, tables, len(fx.sets)-2, "every fixture kind but the two configuration ones maps to a table")

		// Configuration keys and categories.
		for _, c := range []struct {
			kind, column string
		}{{"config_key", "config_key"}, {"config_category", "category"}} {
			set := fx.sets[c.kind]
			count := func(value string) int64 {
				var n int64
				require.NoError(t, models.DB.Raw(fmt.Sprintf("SELECT COUNT(*) FROM configurations WHERE %s = ?", c.column), value).Scan(&n).Error)
				return n
			}
			assert.Positive(t, count(set.own["admin"]), "%s own exists", c.kind)
			assert.Positive(t, count(set.foreign["admin"]), "%s foreign exists", c.kind)
			assert.NotEqual(t, set.own["admin"], set.foreign["admin"])
			assert.Zero(t, count(set.missing), "%s missing has no row", c.kind)
			assert.Zero(t, count(set.malformed), "%s malformed has no row", c.kind)
		}
	})

	// The documented ownership rules. The admin rows prove the foreign record
	// exists, so a refusal for the scoped role is a refusal and not a miss.
	const (
		exactly = iota
		success // any 2xx
		refused // anything but 2xx
	)
	type expectation struct {
		role, name string
		kind, want int
	}
	anchors := []struct {
		method, path string
		expect       []expectation
	}{
		{"GET", "/leads/:id", []expectation{
			{"sales", caseOwn, exactly, 200}, {"sales", caseForeign, refused, 0}, {"admin", caseForeign, exactly, 200}}},
		{"GET", "/deals/:id", []expectation{
			{"sales", caseOwn, exactly, 200}, {"sales", caseForeign, exactly, 403}, {"admin", caseForeign, exactly, 200}}},
		{"GET", "/quotes/:id", []expectation{
			{"sales", caseOwn, exactly, 200}, {"sales", caseForeign, exactly, 403}, {"admin", caseForeign, exactly, 200}}},
		{"GET", "/tickets/:id", []expectation{
			{"support", caseOwn, exactly, 200}, {"support", caseForeign, refused, 0},
			{"customer", caseOwn, exactly, 200}, {"customer", caseForeign, exactly, 403},
			{"admin", caseForeign, exactly, 200}}},
		{"PUT", "/tickets/:id", []expectation{
			{"support", caseOwn, success, 0}, {"support", caseForeign, exactly, 403},
			{"sales", caseOwn, exactly, 403}, {"sales", caseForeign, exactly, 403}}},
		{"GET", "/tasks/:id", []expectation{
			{"sales", caseOwn, exactly, 200}, {"sales", caseForeign, exactly, 403}, {"admin", caseForeign, exactly, 200}}},
	}
	for _, anchor := range anchors {
		request, ok := matrixRequests[anchor.method+" "+anchor.path]
		require.True(t, ok, "%s %s", anchor.method, anchor.path)
		for _, expect := range anchor.expect {
			t.Run(fmt.Sprintf("%s %s %s %s", anchor.method, anchor.path, expect.role, expect.name), func(t *testing.T) {
				cell := h.cell(anchor.method, routeMatrixPrefix+anchor.path, request, expect.role, expect.name)
				switch expect.kind {
				case exactly:
					assert.Equal(t, expect.want, cell.Status, "error code %q", cell.Code)
				case success:
					assert.True(t, cell.Status >= 200 && cell.Status <= 299, "want 2xx, got %d %q", cell.Status, cell.Code)
				case refused:
					assert.False(t, cell.Status >= 200 && cell.Status <= 299, "want a refusal, got %d", cell.Status)
				}
			})
		}
	}
}

// TestRouteMatrixGoldenProvenance pins the base commit and the generator's
// guard. It does not assert that the real tree is guard-free: a successful
// generation proves that while it holds, and the replay does not depend on it.
func TestRouteMatrixGoldenProvenance(t *testing.T) {
	assert.Equal(t, routeMatrixBaseCommit, loadRouteMatrixGolden(t).BaseCommit, "base_commit is the constant the generator stamps")
	assert.Equal(t, routeMatrixGenerator, loadRouteMatrixGolden(t).Generator)

	// tree lays out guard-free stand-ins for the route files under a temp
	// directory, with one extra line appended to routes.go. The real files
	// carry permission guards since #347, so a copy of them could no longer
	// show the guard-free case.
	tree := func(t *testing.T, extra string) string {
		t.Helper()
		root := t.TempDir()
		write := func(rel, content string) {
			t.Helper()
			target := filepath.Join(root, filepath.FromSlash(rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
			require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
		}
		write("cmd/main.go", "package main\n\nfunc main() {}\n")
		for _, name := range []string{"aeo_routes.go", "form_public_routes.go", "form_routes.go"} {
			write("internal/handler/"+name, "package handler\n")
		}
		write("internal/handler/routes.go", "package handler\n"+extra)
		// A test file the glob matches: a guard named there must not count.
		write("internal/handler/routes_test.go", "package handler\n\n// middleware.RequirePermission(\"leads.view\")\n")
		return root
	}
	file := matrixFile{BaseCommit: routeMatrixBaseCommit, Generator: routeMatrixGenerator}

	t.Run("the scan covers cmd/main.go and the non-test route files", func(t *testing.T) {
		files, err := routeMatrixGuardFiles("..")
		require.NoError(t, err)
		var rel []string
		for _, f := range files {
			r, err := filepath.Rel("..", f)
			require.NoError(t, err)
			rel = append(rel, filepath.ToSlash(r))
		}
		assert.Contains(t, rel, "cmd/main.go")
		assert.Contains(t, rel, "internal/handler/routes.go")
		assert.Contains(t, rel, "internal/handler/aeo_routes.go")
		assert.Contains(t, rel, "internal/handler/form_routes.go")
		assert.Contains(t, rel, "internal/handler/form_public_routes.go")
		for _, r := range rel {
			assert.False(t, strings.HasSuffix(r, "_test.go"), "%s is a test file", r)
		}
	})

	for name, line := range map[string]string{
		"refuses a route file with RequirePermission(":    "\nvar _ = middleware.RequirePermission(\"leads.view\")\n",
		"refuses a route file with RequireAnyPermission(": "\nvar _ = middleware.RequireAnyPermission(\"leads.view\", \"leads.view:own\")\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := tree(t, line)
			golden := filepath.Join(root, "cmd", "testdata", "system_role_route_matrix.json")
			require.Error(t, checkNoPermissionGuards(root))
			err := writeRouteMatrix(root, golden, file)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "internal/handler/routes.go")
			assert.NoFileExists(t, golden, "a refused generation writes nothing")
		})
	}

	t.Run("accepts a guard-free copy", func(t *testing.T) {
		root := tree(t, "\n// RequireRole stays the only guard.\n")
		golden := filepath.Join(root, "cmd", "testdata", "system_role_route_matrix.json")
		require.NoError(t, checkNoPermissionGuards(root))
		require.NoError(t, writeRouteMatrix(root, golden, file))
		assert.FileExists(t, golden)
	})
}

// TestRouteMatrixDiffReportsChangedCell covers the comparison on its own.
func TestRouteMatrixDiffReportsChangedCell(t *testing.T) {
	build := func() matrixFile {
		return matrixFile{BaseCommit: routeMatrixBaseCommit, Generator: routeMatrixGenerator, Routes: []matrixRoute{
			{Method: "GET", Path: "/api/v1/leads/:id", Cells: map[string]map[string]matrixCell{
				"admin": {caseOwn: {Status: 200}, caseForeign: {Status: 200}},
				"sales": {caseOwn: {Status: 200}, caseForeign: {Status: 404, Code: "NOT_FOUND"}},
			}},
			{Method: "DELETE", Path: "/api/v1/leads/:id", Cells: map[string]map[string]matrixCell{
				"support": {caseOwn: {Status: 403, Code: "FORBIDDEN"}},
			}},
		}}
	}
	assert.Empty(t, diffRouteMatrix(build(), build()), "equal matrices have no difference")

	changedStatus := build()
	changedStatus.Routes[0].Cells["sales"][caseForeign] = matrixCell{Status: 200, Code: "NOT_FOUND"}
	assert.Equal(t, []string{`GET /api/v1/leads/:id sales foreign: want 404 "NOT_FOUND", got 200 "NOT_FOUND"`},
		diffRouteMatrix(build(), changedStatus))

	changedCode := build()
	changedCode.Routes[1].Cells["support"][caseOwn] = matrixCell{Status: 403, Code: "INSUFFICIENT_PERMISSIONS"}
	assert.Equal(t, []string{`DELETE /api/v1/leads/:id support own: want 403 "FORBIDDEN", got 403 "INSUFFICIENT_PERMISSIONS"`},
		diffRouteMatrix(build(), changedCode))

	missingCell := build()
	delete(missingCell.Routes[0].Cells["admin"], caseForeign)
	assert.Equal(t, []string{"GET /api/v1/leads/:id admin foreign: recorded, but not observed"}, diffRouteMatrix(build(), missingCell))

	extraRoute := build()
	extraRoute.Routes = append(extraRoute.Routes, matrixRoute{Method: "GET", Path: "/api/v1/new"})
	assert.Equal(t, []string{"GET /api/v1/new: served by the router, but not recorded"}, diffRouteMatrix(build(), extraRoute))
	assert.Equal(t, []string{"GET /api/v1/new: recorded, but the router no longer serves it"}, diffRouteMatrix(extraRoute, build()))

	// The encoding is stable and ends with a newline.
	encoded := encodeRouteMatrix(build())
	assert.Equal(t, encoded, encodeRouteMatrix(build()))
	assert.True(t, bytes.HasSuffix(encoded, []byte("}\n")))
	var decoded matrixFile
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Empty(t, diffRouteMatrix(build(), decoded))
}
