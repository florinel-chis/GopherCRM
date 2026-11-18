package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// The three routes that read a user by id answer 404 only for a missing
// record; a read that fails is a 500 (#399). The real services run over
// mocked repositories, so the failure is forced where it happens.

const (
	userLookupCallerID = uint(1) // the admin making the request
	userLookupTargetID = uint(7) // the user that is looked up
	userLookupCustomer = uint(3)
)

type userLookupCase struct {
	name   string
	method string
	path   string
	body   string
	// lookedUpID is the id the user repository is asked for.
	lookedUpID uint
}

var userLookupCases = []userLookupCase{
	{name: "GET /users/:id", method: http.MethodGet, path: "/users/7", lookedUpID: userLookupTargetID},
	{name: "GET /users/me", method: http.MethodGet, path: "/users/me", lookedUpID: userLookupCallerID},
	{name: "POST /customers/:id/assign", method: http.MethodPost, path: "/customers/3/assign", body: `{"user_id":7}`, lookedUpID: userLookupTargetID},
}

type userLookupError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// serveUserLookup runs one case with the user repository's GetByID answering
// lookupErr. The customer lookup of the assign route succeeds, and the
// customer repository carries no Update expectation: a write would panic the
// mock, and AssertNotCalled says so explicitly.
func serveUserLookup(t *testing.T, tc userLookupCase, lookupErr error) (*httptest.ResponseRecorder, userLookupError, *logtest.Hook) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ensureTestLogger()

	userRepo := new(mocks.UserRepository)
	customerRepo := new(mocks.CustomerRepository)
	companyRepo := new(mocks.CompanyRepository)
	userRepo.On("GetByID", tc.lookedUpID).Return(nil, lookupErr)
	customerRepo.On("GetByID", userLookupCustomer).Return(&models.Customer{
		BaseModel: models.BaseModel{ID: userLookupCustomer},
		Email:     "customer@example.com",
	}, nil).Maybe()

	userHandler := NewUserHandler(service.NewUserService(userRepo))
	customerHandler := NewCustomerHandler(service.NewCustomerService(customerRepo, userRepo, companyRepo))

	router := gin.New()
	router.Use(middleware.ErrorHandler())
	router.Use(func(c *gin.Context) {
		c.Set("user_id", userLookupCallerID)
		c.Set("user_role", string(models.RoleAdmin))
		c.Set("request_id", "test-request-id")
		c.Next()
	})
	router.GET("/users/me", userHandler.GetMe)
	router.GET("/users/:id", userHandler.Get)
	router.POST("/customers/:id/assign", customerHandler.Assign)

	hook := logtest.NewLocal(utils.Logger)
	t.Cleanup(func() {
		utils.Logger.ReplaceHooks(make(logrus.LevelHooks))
	})

	req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
	if tc.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	userRepo.AssertCalled(t, "GetByID", tc.lookedUpID)
	customerRepo.AssertNotCalled(t, "Update")

	var envelope struct {
		Success bool            `json:"success"`
		Error   userLookupError `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	require.False(t, envelope.Success)
	return rec, envelope.Error, hook
}

func errorLevelEntries(hook *logtest.Hook) int {
	count := 0
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.ErrorLevel {
			count++
		}
	}
	return count
}

func TestUserLookupFailureIsInternalError(t *testing.T) {
	for _, tc := range userLookupCases {
		t.Run(tc.name, func(t *testing.T) {
			dbErr := errors.New("dial tcp 127.0.0.1:3306: connect: connection refused")
			rec, apiErr, hook := serveUserLookup(t, tc, dbErr)

			assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
			assert.Equal(t, "INTERNAL_ERROR", apiErr.Code)
			assert.Equal(t, "An unexpected error occurred", apiErr.Message)
			assert.NotContains(t, rec.Body.String(), "connection refused")
			assert.NotContains(t, rec.Body.String(), "not found")
			assert.GreaterOrEqual(t, errorLevelEntries(hook), 1, "a failed read is logged at error level")
		})
	}
}

func TestUserLookupMissIsNotFound(t *testing.T) {
	for _, tc := range userLookupCases {
		t.Run(tc.name, func(t *testing.T) {
			rec, apiErr, hook := serveUserLookup(t, tc, gorm.ErrRecordNotFound)

			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			assert.Equal(t, "NOT_FOUND", apiErr.Code)
			assert.Equal(t, "User not found", apiErr.Message)
			assert.Zero(t, errorLevelEntries(hook), "a missing user is not logged at error level")
		})
	}
}
