package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	servicemocks "github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type DashboardHandlerTestSuite struct {
	suite.Suite
	mockLeadService     *servicemocks.LeadService
	mockCustomerService *mocks.CustomerService
	mockTicketService   *mocks.TicketService
	mockTaskService     *MockTaskService
	handler             *DashboardHandler
}

func (suite *DashboardHandlerTestSuite) SetupSuite() {
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)
	gin.SetMode(gin.TestMode)
}

func (suite *DashboardHandlerTestSuite) SetupTest() {
	suite.mockLeadService = new(servicemocks.LeadService)
	suite.mockCustomerService = new(mocks.CustomerService)
	suite.mockTicketService = new(mocks.TicketService)
	suite.mockTaskService = new(MockTaskService)
	suite.handler = NewDashboardHandler(
		suite.mockLeadService,
		suite.mockCustomerService,
		suite.mockTicketService,
		suite.mockTaskService,
	)
}

func (suite *DashboardHandlerTestSuite) TearDownTest() {
	suite.mockLeadService.AssertExpectations(suite.T())
	suite.mockCustomerService.AssertExpectations(suite.T())
	suite.mockTicketService.AssertExpectations(suite.T())
	suite.mockTaskService.AssertExpectations(suite.T())
}

// newRouterWithRole wires the real dashboard routes (including their
// middleware) behind a stub auth layer that injects the given role, so the
// tests exercise the exact route registration from routes.go.
func (suite *DashboardHandlerTestSuite) newRouterWithRole(role models.UserRole) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("request_id", "test-request-id")
		c.Set("user_id", uint(1))
		c.Set("user_role", string(role))
		c.Next()
	})
	SetupDashboardRoutes(router.Group(""), suite.handler)
	return router
}

func (suite *DashboardHandlerTestSuite) expectStatsCounts() {
	suite.mockLeadService.On("GetCount").Return(int64(10), nil)
	suite.mockCustomerService.On("GetCount").Return(int64(4), nil)
	suite.mockTicketService.On("GetOpenCount").Return(int64(3), nil)
	suite.mockTaskService.On("GetPendingCount").Return(int64(7), nil)
}

func (suite *DashboardHandlerTestSuite) TestGetStats_AdminAllowed() {
	router := suite.newRouterWithRole(models.RoleAdmin)
	suite.expectStatsCounts()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/stats", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Data)

	stats := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), float64(10), stats["total_leads"])
	assert.Equal(suite.T(), float64(4), stats["total_customers"])
	assert.Equal(suite.T(), float64(3), stats["open_tickets"])
	assert.Equal(suite.T(), float64(7), stats["pending_tasks"])
	assert.Equal(suite.T(), float64(40), stats["conversion_rate"])
}

func (suite *DashboardHandlerTestSuite) TestGetStats_SalesAllowed() {
	router := suite.newRouterWithRole(models.RoleSales)
	suite.expectStatsCounts()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/stats", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *DashboardHandlerTestSuite) TestGetStats_SupportAllowed() {
	router := suite.newRouterWithRole(models.RoleSupport)
	suite.expectStatsCounts()

	req := httptest.NewRequest(http.MethodGet, "/dashboard/stats", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *DashboardHandlerTestSuite) TestGetStats_CustomerForbidden() {
	router := suite.newRouterWithRole(models.RoleCustomer)
	// No service expectations: a forbidden request must never reach the
	// aggregate counts.

	req := httptest.NewRequest(http.MethodGet, "/dashboard/stats", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Nil(suite.T(), response.Data)
}

func TestDashboardHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(DashboardHandlerTestSuite))
}
