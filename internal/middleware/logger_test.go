package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gocrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
)

func TestLogger(t *testing.T) {
	// Save original logger
	originalLogger := utils.Logger
	defer func() {
		utils.Logger = originalLogger
	}()

	// Create test logger with hook
	testLogger, hook := test.NewNullLogger()
	testLogger.SetLevel(logrus.DebugLevel)
	utils.Logger = testLogger

	// Setup test router
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.Use(Logger())

	tests := []struct {
		name           string
		setupRoute     func(r *gin.Engine)
		request        func() *http.Request
		expectedStatus int
		expectedLevel  logrus.Level
		checkLog       func(t *testing.T, entry *logrus.Entry)
	}{
		{
			name: "successful request",
			setupRoute: func(r *gin.Engine) {
				r.GET("/test", func(c *gin.Context) {
					c.JSON(http.StatusOK, gin.H{"message": "success"})
				})
			},
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/test", nil)
			},
			expectedStatus: http.StatusOK,
			expectedLevel:  logrus.InfoLevel,
			checkLog: func(t *testing.T, entry *logrus.Entry) {
				assert.Equal(t, "Request completed", entry.Message)
				assert.Equal(t, "GET", entry.Data["method"])
				assert.Equal(t, "/test", entry.Data["path"])
				assert.Equal(t, 200, entry.Data["status"])
				assert.NotEmpty(t, entry.Data["request_id"])
				assert.NotEmpty(t, entry.Data["client_ip"])
				assert.NotNil(t, entry.Data["latency_ms"])
			},
		},
		{
			name: "request with query params",
			setupRoute: func(r *gin.Engine) {
				r.GET("/search", func(c *gin.Context) {
					c.JSON(http.StatusOK, gin.H{"query": c.Query("q")})
				})
			},
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/search?q=test&page=1", nil)
			},
			expectedStatus: http.StatusOK,
			expectedLevel:  logrus.InfoLevel,
			checkLog: func(t *testing.T, entry *logrus.Entry) {
				assert.Equal(t, "/search?q=test&page=1", entry.Data["path"])
			},
		},
		{
			name: "client error",
			setupRoute: func(r *gin.Engine) {
				r.GET("/notfound", func(c *gin.Context) {
					c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				})
			},
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/notfound", nil)
			},
			expectedStatus: http.StatusNotFound,
			expectedLevel:  logrus.WarnLevel,
			checkLog: func(t *testing.T, entry *logrus.Entry) {
				assert.Equal(t, "Client error", entry.Message)
				assert.Equal(t, 404, entry.Data["status"])
			},
		},
		{
			name: "server error",
			setupRoute: func(r *gin.Engine) {
				r.GET("/error", func(c *gin.Context) {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
				})
			},
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/error", nil)
			},
			expectedStatus: http.StatusInternalServerError,
			expectedLevel:  logrus.ErrorLevel,
			checkLog: func(t *testing.T, entry *logrus.Entry) {
				assert.Equal(t, "Server error", entry.Message)
				assert.Equal(t, 500, entry.Data["status"])
			},
		},
		{
			name: "request with error",
			setupRoute: func(r *gin.Engine) {
				r.GET("/private-error", func(c *gin.Context) {
					c.Error(gin.Error{
						Err:  assert.AnError,
						Type: gin.ErrorTypePrivate,
					})
					c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
				})
			},
			request: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/private-error", nil)
			},
			expectedStatus: http.StatusBadRequest,
			expectedLevel:  logrus.ErrorLevel,
			checkLog: func(t *testing.T, entry *logrus.Entry) {
				assert.Equal(t, "Request failed", entry.Message)
				assert.NotEmpty(t, entry.Data["error"])
			},
		},
		{
			name: "request with user agent",
			setupRoute: func(r *gin.Engine) {
				r.GET("/ua", func(c *gin.Context) {
					c.JSON(http.StatusOK, gin.H{"ua": c.Request.UserAgent()})
				})
			},
			request: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/ua", nil)
				req.Header.Set("User-Agent", "TestBot/1.0")
				return req
			},
			expectedStatus: http.StatusOK,
			expectedLevel:  logrus.InfoLevel,
			checkLog: func(t *testing.T, entry *logrus.Entry) {
				assert.Equal(t, "TestBot/1.0", entry.Data["user_agent"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset hook
			hook.Reset()

			// Setup route
			testRouter := gin.New()
			testRouter.Use(RequestID())
			testRouter.Use(Logger())
			tt.setupRoute(testRouter)

			// Make request
			w := httptest.NewRecorder()
			req := tt.request()
			testRouter.ServeHTTP(w, req)

			// Check response
			assert.Equal(t, tt.expectedStatus, w.Code)

			// Check log entry
			assert.NotEmpty(t, hook.Entries)
			lastEntry := hook.LastEntry()
			assert.Equal(t, tt.expectedLevel, lastEntry.Level)
			tt.checkLog(t, lastEntry)
		})
	}
}

func TestRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())

	t.Run("generates request ID", func(t *testing.T) {
		var capturedID string
		router.GET("/test", func(c *gin.Context) {
			capturedID = c.GetString("request_id")
			c.JSON(http.StatusOK, gin.H{"request_id": capturedID})
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		router.ServeHTTP(w, req)

		assert.NotEmpty(t, capturedID)
		assert.Equal(t, capturedID, w.Header().Get(RequestIDHeader))
	})

	t.Run("uses provided request ID", func(t *testing.T) {
		providedID := "test-request-id-123"
		var capturedID string
		
		router.GET("/test2", func(c *gin.Context) {
			capturedID = c.GetString("request_id")
			c.JSON(http.StatusOK, gin.H{"request_id": capturedID})
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test2", nil)
		req.Header.Set(RequestIDHeader, providedID)
		router.ServeHTTP(w, req)

		assert.Equal(t, providedID, capturedID)
		assert.Equal(t, providedID, w.Header().Get(RequestIDHeader))
	})
}

func TestLoggerDoesNotLogSensitiveData(t *testing.T) {
	// Save original logger
	originalLogger := utils.Logger
	defer func() {
		utils.Logger = originalLogger
	}()

	// Create test logger with hook and capture output
	var buf bytes.Buffer
	testLogger := logrus.New()
	testLogger.SetOutput(&buf)
	testLogger.SetFormatter(&logrus.JSONFormatter{})
	testLogger.SetLevel(logrus.DebugLevel)
	utils.Logger = testLogger

	// Setup test router
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.Use(Logger())

	// Route that accepts sensitive data
	router.POST("/login", func(c *gin.Context) {
		var loginReq struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		c.ShouldBindJSON(&loginReq)
		c.JSON(http.StatusOK, gin.H{"token": "fake-token"})
	})

	// Make request with sensitive data
	body := bytes.NewBufferString(`{"email":"test@example.com","password":"secret123"}`)
	req := httptest.NewRequest(http.MethodPost, "/login", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sensitive-token")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Check that logs don't contain sensitive data
	logOutput := buf.String()
	assert.NotContains(t, logOutput, "secret123")
	assert.NotContains(t, logOutput, "sensitive-token")
	assert.NotContains(t, logOutput, "password")
	assert.Contains(t, logOutput, "/login") // Should log the path
}