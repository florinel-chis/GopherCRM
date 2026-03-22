package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func setupRecoveryRouter() *gin.Engine {
	// Ensure logger is initialised
	if utils.Logger == nil {
		_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID()) // Recovery() uses GetLogger which reads request_id
	r.Use(Recovery())
	return r
}

func TestRecovery_PanicReturns500(t *testing.T) {
	r := setupRecoveryRouter()
	r.GET("/panic", func(c *gin.Context) {
		panic("something went wrong")
	})

	req, _ := http.NewRequest("GET", "/panic", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestRecovery_NoPanicPassesThrough(t *testing.T) {
	r := setupRecoveryRouter()
	r.GET("/ok", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req, _ := http.NewRequest("GET", "/ok", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}

func TestRecovery_PanicWithNilError(t *testing.T) {
	r := setupRecoveryRouter()
	r.GET("/nil-panic", func(c *gin.Context) {
		panic(nil)
	})

	req, _ := http.NewRequest("GET", "/nil-panic", nil)
	w := httptest.NewRecorder()

	// panic(nil) in Go < 1.21 is recovered as nil; in Go 1.21+ it becomes *runtime.PanicNilError.
	// Either way, the recovery middleware should not crash.
	r.ServeHTTP(w, req)
	// We just verify the server didn't blow up; status depends on Go version behaviour.
	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusInternalServerError)
}

func TestRecovery_PanicWithIntegerValue(t *testing.T) {
	r := setupRecoveryRouter()
	r.GET("/int-panic", func(c *gin.Context) {
		panic(42)
	})

	req, _ := http.NewRequest("GET", "/int-panic", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
