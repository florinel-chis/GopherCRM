package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
)

// Gin panics at registration time when the route tree conflicts (for example a
// static segment competing with a parameter segment registered from another
// group). Every Setup* function is exercised together here, the way
// cmd/main.go mounts them, so a conflicting addition fails this test instead
// of the server boot.
func TestAllRouteSetupsCoexist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1")

	SetupUserRoutes(group, &UserHandler{})
	SetupLeadRoutes(group, &LeadHandler{})
	SetupCustomerRoutes(group, &CustomerHandler{})
	SetupCompanyRoutes(group, &CompanyHandler{})
	SetupDealRoutes(group, &DealHandler{})
	SetupProductRoutes(group, &ProductHandler{})
	SetupQuoteRoutes(group, &QuoteHandler{})
	SetupTicketRoutes(group, &TicketHandler{})
	SetupTaskRoutes(group, &TaskHandler{})
	SetupLabelRoutes(group, &LabelHandler{})
	SetupAPIKeyRoutes(group, &APIKeyHandler{})
	SetupConfigurationRoutes(group, &ConfigurationHandler{})
	SetupDashboardRoutes(group, &DashboardHandler{})
	SetupBulkStatusRoutes(group, &BulkHandler{})
	SetupAEORoutes(group, &AEOHandler{})
	SetupFormRoutes(group, &FormHandler{})
	SetupAutomationRoutes(group, &AutomationHandler{})
	SetupNotificationRoutes(group, &NotificationHandler{})
	// Registered right after SetupCustomerRoutes in cmd/main.go (#212).
	SetupCustomerExternalIDRoutes(group, &CustomerHandler{})
	// The role API (#348) puts the static /roles/audit next to /roles/:id.
	SetupRoleRoutes(group, &RoleHandler{})
	// The forms module registers two groups on the same mount point from two
	// different files: the CRM routes above and the unauthenticated ones here.
	// Between them they put a static segment (/forms/public), a parameter
	// (/forms/:id) and a static-then-parameter path (/forms/submissions/:id)
	// side by side, which is the arrangement most likely to blow up at
	// registration.
	SetupFormPublicRoutes(group, &FormPublicHandler{})
	// The client response link of a quote email (#382) puts the static
	// /quotes/public segment next to /quotes/:id.
	SetupQuotePublicRoutes(group, &QuotePublicHandler{})

	// A request to a static path that shares a prefix with a parameter route
	// must dispatch without a panic; the nil-service handler may then blow up,
	// which is fine — the panic we are guarding against happens at
	// registration, before ServeHTTP is ever reached.
	for _, path := range []string{
		"/api/v1/leads/bulk/status",
		"/api/v1/tickets/bulk/status",
		"/api/v1/tasks/bulk/status",
		"/api/v1/labels",
		// Static segment registered next to /aeo/prompts/:id.
		"/api/v1/aeo/prompts/generate",
		"/api/v1/aeo/runs",
		// The public form group sits under the static /forms/public segment
		// while the CRM group claims /forms/:id.
		"/api/v1/forms",
		"/api/v1/forms/public/confirm",
		"/api/v1/forms/public/abc/submissions",
		"/api/v1/quotes/public/response",
		"/api/v1/quotes/public/render",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()
		func() {
			defer func() { recover() }()
			router.ServeHTTP(w, req)
		}()
		if w.Code == http.StatusNotFound {
			t.Errorf("%s did not match any route", path)
		}
	}

	// The label group mixes a static collection path with a parameter path, and
	// the parameter routes carry role guards the collection ones do not, so
	// every verb is dispatched here rather than only the ones POSTed above.
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/labels"},
		// Roles: the static /roles/audit sits next to /roles/:id on the same verb.
		{http.MethodGet, "/api/v1/roles/audit"},
		{http.MethodGet, "/api/v1/roles/1"},
		{http.MethodPost, "/api/v1/roles/1/clone"},
		{http.MethodGet, "/api/v1/permissions/catalogue"},
		{http.MethodPut, "/api/v1/labels/1"},
		{http.MethodDelete, "/api/v1/labels/1"},
		// The AEO group does the same thing one level deeper: /prompts/generate
		// is static while /prompts/:id and /prompts/:id/answers are parameter
		// routes, and they are registered on three different verbs.
		{http.MethodGet, "/api/v1/aeo/prompts"},
		{http.MethodGet, "/api/v1/aeo/prompts/1/answers"},
		{http.MethodPut, "/api/v1/aeo/prompts/1"},
		{http.MethodDelete, "/api/v1/aeo/prompts/1"},
		{http.MethodGet, "/api/v1/aeo/runs/1"},
		{http.MethodGet, "/api/v1/aeo/dashboard"},
		{http.MethodGet, "/api/v1/aeo/citations"},
		{http.MethodGet, "/api/v1/aeo/providers"},
		{http.MethodGet, "/api/v1/aeo/profile"},
		{http.MethodPut, "/api/v1/aeo/profile"},
		// Forms: the public key parameter and the CRM id parameter share a
		// parent, and both carry a deeper path of their own.
		{http.MethodGet, "/api/v1/forms/public/embed.js"},
		{http.MethodGet, "/api/v1/forms/public/confirm"},
		{http.MethodGet, "/api/v1/forms/public/abc"},
		{http.MethodGet, "/api/v1/forms/public/abc/view"},
		{http.MethodGet, "/api/v1/forms/123"},
		{http.MethodPut, "/api/v1/forms/123"},
		{http.MethodDelete, "/api/v1/forms/123"},
		{http.MethodGet, "/api/v1/forms/123/submissions"},
		{http.MethodGet, "/api/v1/forms/submissions/5"},
		// Companies: the parameter route and its two deeper static segments
		// sit next to each other on the same verb.
		{http.MethodGet, "/api/v1/companies"},
		{http.MethodGet, "/api/v1/companies/1"},
		{http.MethodPut, "/api/v1/companies/1"},
		{http.MethodDelete, "/api/v1/companies/1"},
		{http.MethodGet, "/api/v1/companies/1/customers"},
		{http.MethodGet, "/api/v1/companies/1/leads"},
		// Deals: /:id/stage (POST) and /:id/history (GET) next to /:id on
		// three verbs, and two sub-lists registered from the deals file onto
		// the company and customer parameter paths, next to
		// /companies/:id/customers and /customers/:id/tickets.
		{http.MethodGet, "/api/v1/deals"},
		{http.MethodPost, "/api/v1/deals"},
		{http.MethodGet, "/api/v1/deals/pipeline"},
		{http.MethodGet, "/api/v1/deals/1"},
		{http.MethodPut, "/api/v1/deals/1"},
		{http.MethodDelete, "/api/v1/deals/1"},
		{http.MethodPost, "/api/v1/deals/1/stage"},
		{http.MethodGet, "/api/v1/deals/1/history"},
		{http.MethodGet, "/api/v1/companies/1/deals"},
		{http.MethodGet, "/api/v1/customers/1/deals"},
		{http.MethodGet, "/api/v1/customers/1/tickets"},
		{http.MethodGet, "/api/v1/dashboard/pipeline"},
		// Products: a plain collection next to /:id on five verbs. Quotes:
		// /:id/lines/:lineId two levels down on two verbs, three POST
		// sub-resources, and two sub-lists registered from the quotes file
		// onto the deal and customer parameter paths, next to
		// /deals/:id/stage, /deals/:id/history, /customers/:id/deals and
		// /customers/:id/tickets.
		{http.MethodGet, "/api/v1/products"},
		{http.MethodPost, "/api/v1/products"},
		{http.MethodGet, "/api/v1/products/1"},
		{http.MethodPut, "/api/v1/products/1"},
		{http.MethodDelete, "/api/v1/products/1"},
		{http.MethodGet, "/api/v1/quotes"},
		{http.MethodPost, "/api/v1/quotes"},
		{http.MethodGet, "/api/v1/quotes/1"},
		{http.MethodPut, "/api/v1/quotes/1"},
		{http.MethodDelete, "/api/v1/quotes/1"},
		{http.MethodPost, "/api/v1/quotes/1/lines"},
		{http.MethodPut, "/api/v1/quotes/1/lines/2"},
		{http.MethodDelete, "/api/v1/quotes/1/lines/2"},
		{http.MethodPost, "/api/v1/quotes/1/status"},
		{http.MethodGet, "/api/v1/quotes/1/history"},
		{http.MethodPost, "/api/v1/quotes/1/email"},
		{http.MethodGet, "/api/v1/quotes/1/pdf"},
		// The public response link and the PDF render read next to /quotes/:id.
		{http.MethodGet, "/api/v1/quotes/public/response"},
		{http.MethodPost, "/api/v1/quotes/public/response"},
		{http.MethodPost, "/api/v1/quotes/public/render"},
		{http.MethodGet, "/api/v1/deals/1/quotes"},
		{http.MethodGet, "/api/v1/deals/1/tasks"},
		{http.MethodGet, "/api/v1/customers/1/quotes"},
		// Customer external ids (#212): /customers/:id/external-ids and
		// /:mappingId one level deeper, next to /customers/:id/assign.
		{http.MethodPost, "/api/v1/customers/1/external-ids"},
		{http.MethodDelete, "/api/v1/customers/1/external-ids/2"},
		{http.MethodPost, "/api/v1/customers/1/assign"},
		// Automation: two groups under one static prefix, three POST
		// sub-resources next to /:id on three verbs, and the runs group's
		// admin-only cancel. Notifications: two static segments
		// (/unread-count on GET, /read-all on POST) next to /:id/read.
		{http.MethodGet, "/api/v1/automation/rules"},
		{http.MethodPost, "/api/v1/automation/rules"},
		{http.MethodGet, "/api/v1/automation/rules/1"},
		{http.MethodPut, "/api/v1/automation/rules/1"},
		{http.MethodDelete, "/api/v1/automation/rules/1"},
		{http.MethodPost, "/api/v1/automation/rules/1/enable"},
		{http.MethodPost, "/api/v1/automation/rules/1/disable"},
		{http.MethodPost, "/api/v1/automation/rules/1/dry-run"},
		{http.MethodGet, "/api/v1/automation/runs"},
		{http.MethodGet, "/api/v1/automation/runs/1"},
		{http.MethodPost, "/api/v1/automation/runs/1/cancel"},
		{http.MethodGet, "/api/v1/notifications"},
		{http.MethodGet, "/api/v1/notifications/unread-count"},
		{http.MethodPost, "/api/v1/notifications/read-all"},
		{http.MethodPost, "/api/v1/notifications/1/read"},
	} {
		req := httptest.NewRequest(route.method, route.path, nil)
		w := httptest.NewRecorder()
		func() {
			defer func() { recover() }()
			router.ServeHTTP(w, req)
		}()
		if w.Code == http.StatusNotFound {
			t.Errorf("%s %s did not match any route", route.method, route.path)
		}
	}
}

// Not matching at all is only half of the risk with a static segment next to
// a parameter: /deals/pipeline must also reach the pipeline route and not be
// taken by /deals/:id (which would answer "Invalid deal ID"). A recorder in
// front of every route notes which registered path gin matched.
func TestStaticDealRoutesWinOverTheIDParameter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var matched string
	group := router.Group("/api/v1", func(c *gin.Context) {
		matched = c.FullPath()
		c.Abort()
	})
	SetupDealRoutes(group, &DealHandler{})
	SetupQuoteRoutes(group, &QuoteHandler{})
	SetupTaskRoutes(group, &TaskHandler{})
	SetupDashboardRoutes(group, &DashboardHandler{})
	SetupNotificationRoutes(group, &NotificationHandler{})

	for path, want := range map[string]string{
		"/api/v1/deals/pipeline":             "/api/v1/deals/pipeline",
		"/api/v1/deals/7":                    "/api/v1/deals/:id",
		"/api/v1/deals/7/history":            "/api/v1/deals/:id/history",
		"/api/v1/deals/7/quotes":             "/api/v1/deals/:id/quotes",
		"/api/v1/deals/7/tasks":              "/api/v1/deals/:id/tasks",
		"/api/v1/quotes/7/history":           "/api/v1/quotes/:id/history",
		"/api/v1/dashboard/pipeline":         "/api/v1/dashboard/pipeline",
		"/api/v1/notifications/unread-count": "/api/v1/notifications/unread-count",
	} {
		matched = ""
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if matched != want {
			t.Errorf("GET %s matched %q, want %q", path, matched, want)
		}
	}
}

// POST /automation/rules/dry-run (an unsaved draft) sits next to the :id
// routes of the same group; it must reach the draft handler, and the saved
// rule's dry run must keep reaching the :id route.
func TestAutomationDraftDryRunRouteIsStatic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var matched string
	group := router.Group("/api/v1", func(c *gin.Context) {
		matched = c.FullPath()
		c.Abort()
	})
	SetupAutomationRoutes(group, &AutomationHandler{})

	for _, route := range []struct{ method, path, want string }{
		{http.MethodPost, "/api/v1/automation/rules/dry-run", "/api/v1/automation/rules/dry-run"},
		{http.MethodPost, "/api/v1/automation/rules/7/dry-run", "/api/v1/automation/rules/:id/dry-run"},
		{http.MethodPost, "/api/v1/automation/rules", "/api/v1/automation/rules"},
		{http.MethodGet, "/api/v1/automation/rules/7", "/api/v1/automation/rules/:id"},
		{http.MethodPut, "/api/v1/automation/rules/7", "/api/v1/automation/rules/:id"},
	} {
		matched = ""
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(route.method, route.path, nil))
		if matched != route.want {
			t.Errorf("%s %s matched %q, want %q", route.method, route.path, matched, route.want)
		}
	}
}

// The client response link (#382) and the PDF render read (#384) are mounted
// on the public group while /quotes/:id sits on the protected one. The static
// /quotes/public/response must reach the public handler on both verbs, and
// /quotes/public/render on POST, without a credential; the guarded routes
// must stay guarded, and nothing else under /quotes/public may become
// reachable: /quotes/public is still the :id route behind the guard. A render
// credential is not a session: as a Bearer token it is refused by the real
// Auth middleware on every guarded quote route.
func TestQuotePublicRoutesWinOverTheIDParameter(t *testing.T) {
	ensureHandlerTestLogger(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := router.Group("/api/v1")

	var matched string
	note := func(c *gin.Context) { matched = c.FullPath() }
	public := api.Group("", note)
	protected := api.Group("", note, func(c *gin.Context) {
		// Stands in for middleware.Auth: no credential, no entry.
		c.AbortWithStatus(http.StatusUnauthorized)
	})

	quotes := new(mocks.QuoteService)
	view := &models.QuotePublicView{Number: "Q-000001", Status: models.QuoteStatusSent, Currency: "EUR", Lines: []models.QuotePublicLine{}}
	quotes.On("GetPublicResponse", "tok").Return(view, nil).Once()
	quotes.On("RespondPublic", "tok", "accept").Return(view, nil).Once()
	quotes.On("GetRenderView", "tok").Return(&models.QuoteRenderView{Quote: &models.Quote{BaseModel: models.BaseModel{ID: 1}}}, nil).Once()
	SetupQuotePublicRoutes(public, NewQuotePublicHandler(quotes))
	SetupQuoteRoutes(protected, &QuoteHandler{})

	for _, route := range []struct {
		method, path, body, want string
		status                   int
	}{
		{http.MethodGet, "/api/v1/quotes/public/response?token=tok", "", "/api/v1/quotes/public/response", http.StatusOK},
		{http.MethodPost, "/api/v1/quotes/public/response", `{"token":"tok","decision":"accept"}`, "/api/v1/quotes/public/response", http.StatusOK},
		{http.MethodPost, "/api/v1/quotes/public/render", `{"token":"tok"}`, "/api/v1/quotes/public/render", http.StatusOK},
		{http.MethodGet, "/api/v1/quotes/1", "", "/api/v1/quotes/:id", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/quotes/1/pdf", "", "/api/v1/quotes/:id/pdf", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/quotes/public", "", "/api/v1/quotes/:id", http.StatusUnauthorized},
		{http.MethodPut, "/api/v1/quotes/public", "", "/api/v1/quotes/:id", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/quotes/public/pdf", "", "/api/v1/quotes/:id/pdf", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/quotes/1/history", "", "/api/v1/quotes/:id/history", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/quotes/1/email", "", "/api/v1/quotes/:id/email", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/quotes", "", "/api/v1/quotes", http.StatusUnauthorized},
	} {
		matched = ""
		req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if matched != route.want || w.Code != route.status {
			t.Errorf("%s %s matched %q with %d, want %q with %d", route.method, route.path, matched, w.Code, route.want, route.status)
		}
	}
	// No route exists under /quotes/public but the three: response on GET
	// and POST, render on POST.
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/quotes/public/other"},
		{http.MethodPost, "/api/v1/quotes/public/other"},
		{http.MethodDelete, "/api/v1/quotes/public/response"},
		{http.MethodGet, "/api/v1/quotes/public/render"},
		{http.MethodPut, "/api/v1/quotes/public/render"},
		{http.MethodDelete, "/api/v1/quotes/public/render"},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
		if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s answered %d, want no route", route.method, route.path, w.Code)
		}
	}
	quotes.AssertExpectations(t)
	public3 := 0
	for _, info := range router.Routes() {
		if strings.HasPrefix(info.Path, "/api/v1/quotes/public") {
			public3++
		}
	}
	if public3 != 3 {
		t.Errorf("%d routes are registered under /quotes/public, want 3", public3)
	}

	// The render credential presented as a session: with the real service,
	// the real routes and the real Auth middleware, the credential of a
	// running render is 401 on another quote's read and on a write, while the
	// public route accepts it at that very moment.
	chain := newQuotePDFChain(t)
	var asRead, asWrite, asOwnRead, asPDF, asRender *httptest.ResponseRecorder
	chain.renderer.during = func(token string) {
		asRead = chain.bearer(http.MethodGet, fmt.Sprintf("/api/v1/quotes/%d", chain.quoteB.ID), token, "")
		asWrite = chain.bearer(http.MethodPut, fmt.Sprintf("/api/v1/quotes/%d", chain.quoteA.ID), token, `{"title":"Taken over"}`)
		asOwnRead = chain.bearer(http.MethodGet, fmt.Sprintf("/api/v1/quotes/%d", chain.quoteA.ID), token, "")
		asPDF = chain.bearer(http.MethodGet, fmt.Sprintf("/api/v1/quotes/%d/pdf", chain.quoteA.ID), token, "")
		asRender = chain.post(fmt.Sprintf(`{"token":%q}`, token))
	}
	if _, err := chain.svc.RenderPDF(chain.quoteA.ID); err != nil {
		t.Fatalf("render: %v", err)
	}
	if chain.quoteA.ID != 1 || chain.quoteB.ID != 2 {
		t.Fatalf("the chain's quotes are %d and %d, want 1 and 2", chain.quoteA.ID, chain.quoteB.ID)
	}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"GET /api/v1/quotes/2": asRead, "PUT /api/v1/quotes/1": asWrite,
		"GET /api/v1/quotes/1": asOwnRead, "GET /api/v1/quotes/1/pdf": asPDF,
	} {
		if w == nil || w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"UNAUTHORIZED"`) {
			t.Errorf("%s with the render credential as Bearer: %v, want 401 UNAUTHORIZED", name, w)
		}
	}
	if asRender == nil || asRender.Code != http.StatusOK {
		t.Errorf("POST /quotes/public/render with the same credential: %v, want 200", asRender)
	}
	var title string
	if err := chain.db.Model(&models.Quote{}).Where("id = ?", chain.quoteA.ID).Pluck("title", &title).Error; err != nil || title != "Quote A" {
		t.Errorf("quote A's title is %q (%v), want it untouched", title, err)
	}
}
