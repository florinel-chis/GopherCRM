package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree lays out a fake repository with a cmd and an internal/handler
// package and returns its root.
func writeTree(t *testing.T, cmd, handler string) string {
	t.Helper()
	root := t.TempDir()
	for dir, src := range map[string]string{"cmd": cmd, "internal/handler": handler} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "main.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const goodCmd = `package main

func setupRouter(cfg *Config) *gin.Engine {
	router := gin.New()
	router.Use(cors())
	router.GET("/health", health)
	api := router.Group(cfg.API.Prefix)
	stop := setupDependencies(api, cfg)
	_ = stop
	return router
}

func setupDependencies(router *gin.RouterGroup, cfg *Config) func() {
	h := handler.NewWidgetHandler()
	public := router.Group("")
	{
		auth := public.Group("/auth")
		auth.Use(limit())
		{
			auth.POST("/login", h.Login)
		}
	}
	protected := router.Group("")
	protected.Use(middleware.Auth(svc))
	alias := protected
	handler.SetupWidgetRoutes(alias, h)
	return nil
}
`

const goodHandler = `package handler

func NewWidgetHandler() *WidgetHandler { return &WidgetHandler{} }

func SetupWidgetRoutes(router *gin.RouterGroup, h *WidgetHandler) {
	widgets := router.Group("/widgets")
	{
		widgets.GET("", h.List)
		widgets.GET("/:id", h.Get)
		widgets.DELETE("/:id/parts/:partId", guard(), h.DeletePart)
	}
	router.POST("/widgets/bulk/status", h.Bulk)
}
`

func routeKeys(routes []Route) []string {
	var keys []string
	for _, r := range routes {
		prefix := ""
		if !r.Base {
			prefix = "!"
		}
		keys = append(keys, prefix+r.Method+" "+r.Path)
	}
	return keys
}

func TestExtractFollowsGroupsAliasesAndSetupCalls(t *testing.T) {
	routes, err := Extract(writeTree(t, goodCmd, goodHandler), DefaultSources)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(routeKeys(routes), ",")
	want := "POST /auth/login,GET /widgets,POST /widgets/bulk/status,GET /widgets/{id},DELETE /widgets/{id}/parts/{partId},!GET /health"
	if got != want {
		t.Fatalf("routes\n got: %s\nwant: %s", got, want)
	}
	if routes[0].File != "cmd/main.go" || routes[0].Line != 20 {
		t.Fatalf("position = %s:%d", routes[0].File, routes[0].Line)
	}
}

func TestExtractMarksRoutesBehindTheAuthMiddleware(t *testing.T) {
	handler := strings.Replace(goodHandler, `router.POST("/widgets/bulk/status", h.Bulk)`,
		`router.POST("/widgets/bulk/status", h.Bulk)
	open := router.Group("/open")
	open.GET("", h.Open)`, 1)
	cmd := strings.Replace(goodCmd, `auth.POST("/login", h.Login)`,
		`auth.POST("/login", h.Login)
			auth.POST("/logout", middleware.Auth(svc), h.Logout)`, 1)
	routes, err := Extract(writeTree(t, cmd, handler), DefaultSources)
	if err != nil {
		t.Fatal(err)
	}
	var secured, public []string
	for _, r := range routes {
		if r.Auth {
			secured = append(secured, r.Method+" "+r.Path)
		} else {
			public = append(public, r.Method+" "+r.Path)
		}
	}
	// The whole protected group (aliased, and its subgroups) plus the one
	// route that takes the middleware itself.
	wantSecured := "POST /auth/logout,GET /open,GET /widgets,POST /widgets/bulk/status,GET /widgets/{id},DELETE /widgets/{id}/parts/{partId}"
	if got := strings.Join(secured, ","); got != wantSecured {
		t.Fatalf("secured\n got: %s\nwant: %s", got, wantSecured)
	}
	if got := strings.Join(public, ","); got != "POST /auth/login,GET /health" {
		t.Fatalf("public = %s", got)
	}
}

func TestExtractAuthInstalledAfterADerivedGroupDoesNotReachIt(t *testing.T) {
	cmd := strings.Replace(goodCmd, `	protected.Use(middleware.Auth(svc))
	alias := protected`, `	early := protected.Group("/early")
	early.GET("", h.Early)
	protected.Use(middleware.Auth(svc))
	alias := protected`, 1)
	routes, err := Extract(writeTree(t, cmd, goodHandler), DefaultSources)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.Path == "/early" && r.Auth {
			t.Fatal("a group derived before Use(middleware.Auth) must not be marked as authenticated")
		}
		if r.Path == "/widgets" && !r.Auth {
			t.Fatal("/widgets lost its authentication")
		}
	}
}

func TestExtractRejectsUnsupportedForms(t *testing.T) {
	cases := map[string]struct{ handler, want string }{
		"Any": {
			strings.Replace(goodHandler, `widgets.GET("", h.List)`, `widgets.Any("", h.List)`, 1),
			"unsupported route registration form Any",
		},
		"non-literal path": {
			strings.Replace(goodHandler, `widgets.GET("", h.List)`, `widgets.GET(listPath, h.List)`, 1),
			"is not a string literal",
		},
		"non-literal group": {
			strings.Replace(goodHandler, `router.Group("/widgets")`, `router.Group(prefix)`, 1),
			"group prefix",
		},
		"unreached registration": {
			goodHandler + "\nfunc SetupOrphanRoutes(router *gin.RouterGroup) {\n\trouter.GET(\"/orphan\", h)\n}\n",
			"not reached from main.setupRouter",
		},
		"group passed through an unknown helper": {
			strings.Replace(goodHandler, `widgets := router.Group("/widgets")`, `widgets := wrap(router)`, 1),
			"not reached",
		},
		"group handed to a function outside the sources": {
			strings.Replace(goodHandler, `router.POST("/widgets/bulk/status", h.Bulk)`, `extra.Register(router)`, 1),
			"router group router is passed to extra.Register, which the route walk cannot follow",
		},
		"group built inline and handed to a function outside the sources": {
			strings.Replace(goodHandler, `router.POST("/widgets/bulk/status", h.Bulk)`, `extra.Register(router.Group("/extra"))`, 1),
			`router group router.Group("/extra") is passed to extra.Register`,
		},
		"handler that cannot be identified": {
			strings.Replace(goodHandler, `widgets.GET("", h.List)`, `widgets.GET("", func(c *gin.Context) {})`, 1),
			"GET : cannot identify the handler",
		},
		"group handed to a method": {
			strings.Replace(goodHandler, `router.POST("/widgets/bulk/status", h.Bulk)`, `h.Register(router)`, 1),
			"router group router is passed to h.Register",
		},
		"duplicate": {
			strings.Replace(goodHandler, `router.POST("/widgets/bulk/status", h.Bulk)`, `router.GET("/widgets", h.Again)`, 1),
			"duplicate route GET /widgets",
		},
		"handle": {
			strings.Replace(goodHandler, `widgets.GET("", h.List)`, `widgets.Handle("GET", "", h.List)`, 1),
			"unsupported route registration form Handle",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Extract(writeTree(t, goodCmd, tc.handler), DefaultSources)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestExtractRecordsHandlersAndMiddlewareResponses(t *testing.T) {
	cmd := strings.Replace(goodCmd, `	protected.Use(middleware.Auth(svc))`, `	protected.Use(middleware.Auth(svc))
	protected.Use(middleware.RateLimitModerate())`, 1)
	cmd = strings.Replace(cmd, `auth.Use(limit())`, `auth.Use(middleware.RateLimitStrict())`, 1)
	cmd = strings.Replace(cmd, `auth.POST("/login", h.Login)`, `auth.POST("/login", h.Login)
			auth.GET("/status", handler.Status(svc))
			auth.POST("/register", middleware.RequireRegistrationEnabled(svc), h.Register)`, 1)
	handler := strings.Replace(goodHandler, `widgets.DELETE("/:id/parts/:partId", guard(), h.DeletePart)`,
		`widgets.DELETE("/:id/parts/:partId", admin, h.DeletePart)`, 1)
	handler = strings.Replace(handler, `	widgets := router.Group("/widgets")`, `	admin := middleware.RequireRole(models.RoleAdmin)
	widgets := router.Group("/widgets")`, 1)
	handler += "\nfunc Status(svc Service) gin.HandlerFunc { return nil }\n"
	routes, err := Extract(writeTree(t, cmd, handler), DefaultSources)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range routes {
		got[r.Method+" "+r.Path] = fmt.Sprintf("%s %v", r.Handler, r.Responses)
	}
	want := map[string]string{
		"POST /auth/login":                    "handler.WidgetHandler.Login [429]",
		"GET /auth/status":                    "handler.Status [429]",
		"POST /auth/register":                 "handler.WidgetHandler.Register [403 429]",
		"GET /widgets":                        "handler.WidgetHandler.List [401 429]",
		"GET /widgets/{id}":                   "handler.WidgetHandler.Get [401 429]",
		"DELETE /widgets/{id}/parts/{partId}": "handler.WidgetHandler.DeletePart [401 403 429]",
		"POST /widgets/bulk/status":           "handler.WidgetHandler.Bulk [401 429]",
		"GET /health":                         " []", // health is not a loaded function
	}
	for key, w := range want {
		if got[key] != w {
			t.Errorf("%s = %q, want %q", key, got[key], w)
		}
	}
}

func TestExtractIgnoresTestFiles(t *testing.T) {
	root := writeTree(t, goodCmd, goodHandler)
	test := "package handler\n\nfunc helper(r *gin.RouterGroup) { r.GET(\"/only-in-tests\", nil) }\n"
	if err := os.WriteFile(filepath.Join(root, "internal/handler/x_test.go"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(root, DefaultSources); err != nil {
		t.Fatal(err)
	}
}

func TestExtractRequiresTheEntryFunction(t *testing.T) {
	cmd := strings.Replace(goodCmd, "func setupRouter", "func otherRouter", 1)
	if _, err := Extract(writeTree(t, cmd, goodHandler), DefaultSources); err == nil {
		t.Fatal("expected an error without setupRouter")
	}
}

func TestSpecPath(t *testing.T) {
	for in, want := range map[string]string{
		"":                          "/",
		"/users":                    "/users",
		"/quotes/:id/lines/:lineId": "/quotes/{id}/lines/{lineId}",
		"/files/*rest":              "/files/{rest}",
	} {
		if got := specPath(in); got != want {
			t.Errorf("specPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// The real router: every route is found and only /health sits outside the
// base path.
func TestExtractRepositoryRouter(t *testing.T) {
	routes, err := Extract(filepath.Join("..", "..", ".."), DefaultSources)
	if err != nil {
		t.Fatal(err)
	}
	outside := 0
	for _, r := range routes {
		if !r.Base {
			outside++
			if r.Method != "GET" || r.Path != "/health" {
				t.Errorf("unexpected route outside the base path: %s %s", r.Method, r.Path)
			}
		}
	}
	if outside != 1 || len(routes) < 100 {
		t.Fatalf("got %d routes, %d outside the base path", len(routes), outside)
	}
	for _, r := range routes {
		public := r.Path == "/health" || strings.HasPrefix(r.Path, "/forms/public/") ||
			(strings.HasPrefix(r.Path, "/auth/") && r.Path != "/auth/logout" && r.Path != "/auth/change-password")
		if r.Auth == public {
			t.Errorf("%s %s: auth = %t", r.Method, r.Path, r.Auth)
		}
	}
}
