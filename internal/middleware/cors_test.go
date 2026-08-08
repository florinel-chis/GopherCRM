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
