package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func setupRequestIDRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.GET("/test", func(c *gin.Context) {
		reqID := c.GetString("request_id")
		c.JSON(http.StatusOK, gin.H{"request_id": reqID})
	})
	return r
}

func TestRequestID_GeneratesID(t *testing.T) {
	r := setupRequestIDRouter()

	req, _ := http.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Should be set in response header
	headerID := w.Header().Get(RequestIDHeader)
	assert.NotEmpty(t, headerID)

	// Should also be in the response body (set via context)
	assert.Contains(t, w.Body.String(), headerID)
}

func TestRequestID_UniquePerRequest(t *testing.T) {
	r := setupRequestIDRouter()

	ids := make(map[string]bool)
	for i := 0; i < 50; i++ {
		req, _ := http.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		id := w.Header().Get(RequestIDHeader)
		assert.NotEmpty(t, id)
		assert.False(t, ids[id], "duplicate request ID detected: %s", id)
		ids[id] = true
	}
}

func TestRequestID_PreservesClientID(t *testing.T) {
	r := setupRequestIDRouter()

	clientID := "client-provided-request-id-12345"
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set(RequestIDHeader, clientID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, clientID, w.Header().Get(RequestIDHeader))
	assert.Contains(t, w.Body.String(), clientID)
}

func TestRequestID_EmptyHeaderGeneratesNew(t *testing.T) {
	r := setupRequestIDRouter()

	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set(RequestIDHeader, "")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	id := w.Header().Get(RequestIDHeader)
	assert.NotEmpty(t, id)
}
