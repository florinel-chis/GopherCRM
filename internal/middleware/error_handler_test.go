package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func init() {
	// Ensure logger is initialised for tests that use GetLogger via error_handler.
	if utils.Logger == nil {
		_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})
	}
}

func setupErrorHandlerRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID()) // error handler uses GetLogger which reads request_id
	r.Use(ErrorHandler())
	return r
}

func TestErrorHandler_NoErrors(t *testing.T) {
	r := setupErrorHandlerRouter()
	r.GET("/ok", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req, _ := http.NewRequest("GET", "/ok", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}

func TestErrorHandler_PublicError(t *testing.T) {
	r := setupErrorHandlerRouter()
	r.GET("/public-err", func(c *gin.Context) {
		c.Status(http.StatusBadRequest)
		_ = c.Error(errors.New("bad input")).SetType(gin.ErrorTypePublic)
	})

	req, _ := http.NewRequest("GET", "/public-err", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestErrorHandler_PublicErrorDefaultsTo400WhenStatusOK(t *testing.T) {
	r := setupErrorHandlerRouter()
	r.GET("/pub-default", func(c *gin.Context) {
		// Don't explicitly set status — leave it at default 200
		_ = c.Error(errors.New("oops")).SetType(gin.ErrorTypePublic)
	})

	req, _ := http.NewRequest("GET", "/pub-default", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// ErrorHandler should fall back to 400 when status is still 200
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestErrorHandler_PrivateError_Returns500(t *testing.T) {
	r := setupErrorHandlerRouter()
	r.GET("/internal-err", func(c *gin.Context) {
		_ = c.Error(errors.New("database connection lost")).SetType(gin.ErrorTypePrivate)
	})

	req, _ := http.NewRequest("GET", "/internal-err", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "INTERNAL_ERROR")
	// Must not leak internal error details
	assert.NotContains(t, w.Body.String(), "database connection lost")
}

func TestErrorHandler_BindingError_GenericFormat(t *testing.T) {
	r := setupErrorHandlerRouter()
	r.GET("/bind-err", func(c *gin.Context) {
		_ = c.Error(errors.New("cannot parse JSON")).SetType(gin.ErrorTypeBind)
	})

	req, _ := http.NewRequest("GET", "/bind-err", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "Invalid request format")
}

func TestErrorHandler_AlreadyWritten(t *testing.T) {
	r := setupErrorHandlerRouter()
	r.GET("/written", func(c *gin.Context) {
		c.JSON(http.StatusCreated, gin.H{"created": true})
		// Even though we add an error, the response is already written
		_ = c.Error(errors.New("late error"))
	})

	req, _ := http.NewRequest("GET", "/written", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), "created")
}
