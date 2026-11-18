package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// TestPasswordLengthLimit pins the one answer the four routes that take a new
// password give to more than 72 bytes: 400 BAD_REQUEST with the same message,
// decided in the handler before the service is called. A password of exactly
// 72 bytes reaches the service.
func TestPasswordLengthLimit(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)

	const tooLongMessage = "password must be at most 72 bytes long"
	atLimit := "Aa1!" + strings.Repeat("x", 68)
	overLimit := atLimit + "x"
	// Counted in bytes, not characters: 39 characters in 74 bytes.
	overLimitMultibyte := "Aa1!" + strings.Repeat("é", 35)
	atLimitMultibyte := "Aa1!" + strings.Repeat("é", 34)
	require.Len(t, atLimit, 72)
	require.Len(t, overLimit, 73)
	require.Len(t, atLimitMultibyte, 72)
	require.Len(t, overLimitMultibyte, 74)

	type routeCase struct {
		name    string
		path    string
		method  string // the service method the handler must not reach
		body    func(password string) map[string]interface{}
		expect  func(auth *MockAuthService, users *MockUserService, password string)
		success int
	}
	routes := []routeCase{
		{
			name: "POST /auth/register", path: "/auth/register", method: "Register",
			body: func(password string) map[string]interface{} {
				return map[string]interface{}{"email": "long@example.com", "password": password,
					"first_name": "Long", "last_name": "Password"}
			},
			expect: func(auth *MockAuthService, users *MockUserService, password string) {
				users.On("Register", mock.AnythingOfType("*models.User"), password).Return(nil).Once()
				auth.On("GenerateJWT", mock.AnythingOfType("*models.User")).Return("jwt", nil).Once()
			},
			success: http.StatusCreated,
		},
		{
			name: "POST /users", path: "/users", method: "CreateAs",
			body: func(password string) map[string]interface{} {
				return map[string]interface{}{"email": "long@example.com", "password": password,
					"first_name": "Long", "last_name": "Password", "role": "sales"}
			},
			expect: func(_ *MockAuthService, users *MockUserService, password string) {
				users.On("CreateAs", mock.Anything, mock.Anything, mock.AnythingOfType("*models.User"), password).
					Return(nil).Once()
			},
			success: http.StatusCreated,
		},
		{
			name: "POST /auth/change-password", path: "/auth/change-password", method: "ChangePassword",
			body: func(password string) map[string]interface{} {
				return map[string]interface{}{"current_password": "Old!Passw0rd", "new_password": password}
			},
			expect: func(auth *MockAuthService, _ *MockUserService, password string) {
				auth.On("ChangePassword", uint(42), "Old!Passw0rd", password, "").Return(nil, nil).Once()
			},
			success: http.StatusOK,
		},
		{
			name: "POST /auth/password-reset/confirm", path: "/auth/password-reset/confirm", method: "ConfirmPasswordReset",
			body: func(password string) map[string]interface{} {
				return map[string]interface{}{"token": "reset-token", "new_password": password}
			},
			expect: func(auth *MockAuthService, _ *MockUserService, password string) {
				auth.On("ConfirmPasswordReset", "reset-token", password).Return(nil).Once()
			},
			success: http.StatusOK,
		},
	}

	serve := func(t *testing.T, route routeCase, password string, reachesService bool) (*httptest.ResponseRecorder, *MockAuthService, *MockUserService) {
		t.Helper()
		auth, users := new(MockAuthService), new(MockUserService)
		if reachesService {
			route.expect(auth, users, password)
		}
		authHandler := NewAuthHandler(auth, users)
		userHandler := NewUserHandler(users)

		router := gin.New()
		router.Use(func(c *gin.Context) {
			// What middleware.Auth leaves behind for an admin bearer session.
			c.Set("user_id", uint(42))
			c.Set("user_role", string(models.RoleAdmin))
			c.Set(service.AuthMethodContextKey, service.AuthMethodBearer)
			c.Next()
		})
		router.POST("/auth/register", authHandler.Register)
		router.POST("/users", userHandler.Create)
		router.POST("/auth/change-password", authHandler.ChangePassword)
		router.POST("/auth/password-reset/confirm", authHandler.ConfirmPasswordReset)

		body, err := json.Marshal(route.body(password))
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, route.path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec, auth, users
	}

	type refusal struct {
		status  int
		code    string
		message string
	}
	refusals := map[string]refusal{}

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			for label, password := range map[string]string{
				"73 ASCII bytes":              overLimit,
				"74 bytes in 39 characters":   overLimitMultibyte,
				"weak and more than 72 bytes": strings.Repeat("a", 73),
			} {
				rec, auth, users := serve(t, route, password, false)

				var response utils.APIResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
				require.NotNil(t, response.Error, "%s: %s", label, rec.Body.String())
				assert.Equal(t, http.StatusBadRequest, rec.Code, label)
				assert.Equal(t, "BAD_REQUEST", response.Error.Code, label)
				assert.Equal(t, tooLongMessage, response.Error.Message, label)
				assert.False(t, response.Success, label)

				// Refused in the handler: nothing was stored, changed or spent.
				auth.AssertNotCalled(t, route.method, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
				users.AssertNotCalled(t, route.method, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
				assert.Empty(t, auth.Calls, label)
				assert.Empty(t, users.Calls, label)

				if password == overLimit {
					refusals[route.name] = refusal{rec.Code, response.Error.Code, response.Error.Message}
				}
			}

			for label, password := range map[string]string{
				"72 ASCII bytes":            atLimit,
				"72 bytes in 38 characters": atLimitMultibyte,
			} {
				rec, auth, users := serve(t, route, password, true)
				assert.Equal(t, route.success, rec.Code, "%s: %s", label, rec.Body.String())
				auth.AssertExpectations(t)
				users.AssertExpectations(t)
				assert.Len(t, append(append([]mock.Call{}, auth.Calls...), users.Calls...),
					len(auth.ExpectedCalls)+len(users.ExpectedCalls), "%s: each service method is called once", label)
			}
		})
	}

	// One answer on every route.
	require.Len(t, refusals, len(routes))
	for _, route := range routes {
		assert.Equal(t, refusal{http.StatusBadRequest, "BAD_REQUEST", tooLongMessage}, refusals[route.name], route.name)
	}
}
