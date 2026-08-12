package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func corsTestRouter(extraOrigins ...string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS(extraOrigins...))
	router.POST("/ping", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	return router
}

func doCORSRequest(router *gin.Engine, origin string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ping", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	router.ServeHTTP(w, req)
	return w
}

func TestCORS_DefaultOriginAllowed(t *testing.T) {
	w := doCORSRequest(corsTestRouter(), "http://localhost:5173")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "http://localhost:5173", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_UnlistedOriginRejected(t *testing.T) {
	w := doCORSRequest(corsTestRouter(), "http://localhost:3001")
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestCORS_ExtraOriginAllowed(t *testing.T) {
	router := corsTestRouter("http://localhost:3001")
	w := doCORSRequest(router, "http://localhost:3001")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "http://localhost:3001", w.Header().Get("Access-Control-Allow-Origin"))

	// The defaults must survive the addition.
	w = doCORSRequest(router, "http://localhost:3000")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestCORS_NoOriginHeaderPasses(t *testing.T) {
	// Non-browser clients (curl, the Go tests, API keys) send no Origin.
	w := doCORSRequest(corsTestRouter(), "")
	assert.Equal(t, http.StatusOK, w.Code)
}

const publicPrefix = "/api/v1/forms/public"

// publicPrefixRouter mounts the same probe handler under a public-prefixed path
// and under a regular API path, so one router exercises both branches.
func publicPrefixRouter(extraOrigins ...string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORSWithPublicPrefixes([]string{publicPrefix}, extraOrigins...))
	probe := func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	}
	router.GET(publicPrefix+"/acme/view", probe)
	router.POST(publicPrefix+"/acme/submissions", probe)
	router.GET("/api/v1/leads", probe)
	return router
}

func doRequest(router *gin.Engine, method, path, origin string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	router.ServeHTTP(w, req)
	return w
}

func TestCORSWithPublicPrefixes_ForeignOriginAllowedWithoutCredentials(t *testing.T) {
	w := doRequest(publicPrefixRouter(), http.MethodPost,
		publicPrefix+"/acme/submissions", "https://random-customer-site.example")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "pong", w.Body.String())
	assert.Equal(t, "https://random-customer-site.example", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "Origin", w.Header().Get("Vary"))
	assert.Equal(t, "GET, POST, OPTIONS", w.Header().Get("Access-Control-Allow-Methods"))
	assert.Equal(t, "Content-Type", w.Header().Get("Access-Control-Allow-Headers"))
	// Credential-less by design: a public endpoint must never invite cookies.
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"))
}

func TestCORSWithPublicPrefixes_PreflightReturns204(t *testing.T) {
	w := doRequest(publicPrefixRouter(), http.MethodOptions,
		publicPrefix+"/acme/submissions", "https://random-customer-site.example")

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "https://random-customer-site.example", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "GET, POST, OPTIONS", w.Header().Get("Access-Control-Allow-Methods"))
	assert.Equal(t, "Content-Type", w.Header().Get("Access-Control-Allow-Headers"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"))
	assert.Empty(t, w.Body.String())
}

func TestCORSWithPublicPrefixes_NoOriginSkipsHeaders(t *testing.T) {
	w := doRequest(publicPrefixRouter(), http.MethodGet, publicPrefix+"/acme/view", "")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "pong", w.Body.String())
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, w.Header().Get("Vary"))
}

func TestCORSWithPublicPrefixes_StrictPathKeepsRejectingForeignOrigin(t *testing.T) {
	w := doRequest(publicPrefixRouter(), http.MethodGet, "/api/v1/leads",
		"https://random-customer-site.example")

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSWithPublicPrefixes_StrictPathKeepsAllowlistedOrigin(t *testing.T) {
	router := publicPrefixRouter("http://localhost:3001")

	w := doRequest(router, http.MethodGet, "/api/v1/leads", "http://localhost:3001")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "http://localhost:3001", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))

	w = doRequest(router, http.MethodGet, "/api/v1/leads", "http://localhost:5173")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "http://localhost:5173", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_DelegatesWithoutPublicPrefixes(t *testing.T) {
	// CORS is CORSWithPublicPrefixes(nil, ...): no path is carved out.
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS())
	router.POST(publicPrefix+"/acme/submissions", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := doRequest(router, http.MethodPost, publicPrefix+"/acme/submissions",
		"https://random-customer-site.example")
	assert.Equal(t, http.StatusForbidden, w.Code)
}
