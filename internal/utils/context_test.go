package utils

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func init() {
	// Ensure Logger is initialised for context tests
	if Logger == nil {
		_ = InitLogger(&config.LoggingConfig{Level: "debug", Format: "text"})
	}
}

func newTestGinContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("GET", "/test", nil)
	return c
}

func TestGetLogger_WithRequestID(t *testing.T) {
	c := newTestGinContext()
	c.Set("request_id", "req-abc-123")

	entry := GetLogger(c)
	assert.NotNil(t, entry)
	assert.Equal(t, "req-abc-123", entry.Data["request_id"])
}

func TestGetLogger_WithUserContext(t *testing.T) {
	c := newTestGinContext()
	c.Set("request_id", "req-456")
	c.Set("user_id", uint(42))
	c.Set("user_role", "admin")

	entry := GetLogger(c)
	assert.Equal(t, uint(42), entry.Data["user_id"])
	assert.Equal(t, "admin", entry.Data["user_role"])
}

func TestGetLogger_WithoutUserContext(t *testing.T) {
	c := newTestGinContext()
	c.Set("request_id", "req-789")

	entry := GetLogger(c)
	_, hasUserID := entry.Data["user_id"]
	_, hasUserRole := entry.Data["user_role"]
	assert.False(t, hasUserID)
	assert.False(t, hasUserRole)
}

func TestLogServiceCall(t *testing.T) {
	c := newTestGinContext()
	c.Set("request_id", "req-svc")
	logger := GetLogger(c)

	entry := LogServiceCall(logger, "UserService", "GetByID", uint(1))
	assert.NotNil(t, entry)
	assert.Equal(t, "UserService", entry.Data["service"])
	assert.Equal(t, "GetByID", entry.Data["method"])
}

func TestLogServiceCall_NoArgs(t *testing.T) {
	c := newTestGinContext()
	logger := GetLogger(c)

	entry := LogServiceCall(logger, "AuthService", "Login")
	assert.NotNil(t, entry)
	assert.Equal(t, "AuthService", entry.Data["service"])
	_, hasArgs := entry.Data["args"]
	assert.False(t, hasArgs)
}

func TestLogServiceResponse_WithError(t *testing.T) {
	c := newTestGinContext()
	logger := GetLogger(c)
	entry := LogServiceCall(logger, "Svc", "Method")

	// Should not panic
	LogServiceResponse(entry, assert.AnError)
}

func TestLogServiceResponse_WithoutError(t *testing.T) {
	c := newTestGinContext()
	logger := GetLogger(c)
	entry := LogServiceCall(logger, "Svc", "Method")

	// Should not panic
	LogServiceResponse(entry, nil, "some-result")
}

func TestLogHandlerStart(t *testing.T) {
	c := newTestGinContext()
	c.Set("request_id", "req-handler")

	entry := LogHandlerStart(c, "UserHandler.Create")
	assert.Equal(t, "UserHandler.Create", entry.Data["handler"])
}

func TestLogHandlerResponse(t *testing.T) {
	c := newTestGinContext()
	logger := GetLogger(c)

	// Should not panic
	LogHandlerResponse(logger, 200, gin.H{"id": 1})
}

func TestGetLogger_ReturnsLogrusEntry(t *testing.T) {
	c := newTestGinContext()
	c.Set("request_id", "type-check")

	entry := GetLogger(c)
	// Verify it's a *logrus.Entry we can chain further
	chained := entry.WithField("extra", "value")
	assert.IsType(t, &logrus.Entry{}, chained)
}

func TestContextLoggingPrivacy_OmitsArgumentsAndPayloads(t *testing.T) {
	old := Logger
	defer func() { Logger = old }()
	var output bytes.Buffer
	Logger = logrus.New()
	Logger.SetLevel(logrus.DebugLevel)
	Logger.SetFormatter(&logrus.JSONFormatter{})
	Logger.SetOutput(&output)

	const email = "context-email-canary@example.test"
	const token = "context-token-canary"
	const content = "context-private-body-canary"
	c := newTestGinContext()
	c.Set("request_id", "request-privacy")
	c.Set("user_id", uint(12))
	payload := gin.H{"email": email, "token": token, "body": content}
	entry := LogServiceCall(GetLogger(c).WithField("customer_id", uint(43)), "CustomerService", "Create", payload)
	LogServiceResponse(entry, nil, payload)
	LogServiceResponse(entry, fmt.Errorf("duplicate email %q", email))
	logger := LogHandlerStart(c, "AuthHandler.Register")
	LogHandlerResponse(logger, http.StatusCreated, payload)

	for _, sensitive := range []string{email, token, content} {
		assert.NotContains(t, output.String(), sensitive)
	}
	for _, diagnostic := range []string{"CustomerService", "Create", "customer_id", "43", "AuthHandler.Register", "status_code", "201", "request-privacy", "user_id"} {
		assert.Contains(t, output.String(), diagnostic)
	}
	for _, field := range []string{`"args":`, `"result":`, `"response":`} {
		assert.NotContains(t, output.String(), field)
	}
}
