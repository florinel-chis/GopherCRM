package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gocrm/internal/config"
	"github.com/florinel-chis/gocrm/internal/handler"
	"github.com/florinel-chis/gocrm/internal/middleware"
	"github.com/florinel-chis/gocrm/internal/models"
	"github.com/florinel-chis/gocrm/internal/repository"
	"github.com/florinel-chis/gocrm/internal/service"
	"github.com/florinel-chis/gocrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type LeadIntegrationTestSuite struct {
	suite.Suite
	db          *gorm.DB
	router      *gin.Engine
	authService service.AuthService
	userService service.UserService
	leadService service.LeadService
	adminUser   *models.User
	salesUser   *models.User
	adminToken  string
	salesToken  string
}

func (suite *LeadIntegrationTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := &config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	err := utils.InitLogger(logConfig)
	suite.NoError(err)
	
	// Setup test database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	suite.NoError(err)
	
	// Migrate the schema
	err = db.AutoMigrate(&models.User{}, &models.APIKey{}, &models.Lead{}, &models.Customer{})
	suite.NoError(err)
	
	suite.db = db
	
	// Initialize repositories
	userRepo := repository.NewUserRepository(db)
	leadRepo := repository.NewLeadRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	
	// Initialize repositories
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	
	// Initialize services
	jwtConfig := config.JWTConfig{
		Secret:      "test-secret",
		ExpiryHours: 24,
	}
	suite.authService = service.NewAuthService(userRepo, apiKeyRepo, jwtConfig)
	suite.userService = service.NewUserService(userRepo)
	suite.leadService = service.NewLeadService(leadRepo, customerRepo)
	
	// Initialize handlers
	authHandler := handler.NewAuthHandler(suite.authService, suite.userService)
	userHandler := handler.NewUserHandler(suite.userService)
	leadHandler := handler.NewLeadHandler(suite.leadService)
	
	// Setup Gin router
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.Logger())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	
	// Setup routes
	api := router.Group("/api/v1")
	
	// Auth routes
	api.POST("/auth/login", authHandler.Login)
	
	// Protected routes
	protected := api.Group("")
	protected.Use(middleware.Auth(suite.authService))
	
	// User routes
	protected.POST("/users", userHandler.Create)
	protected.GET("/users", userHandler.List)
	protected.GET("/users/:id", userHandler.Get)
	protected.PUT("/users/:id", userHandler.Update)
	protected.DELETE("/users/:id", userHandler.Delete)
	
	// Lead routes
	protected.POST("/leads", leadHandler.Create)
	protected.GET("/leads", leadHandler.List)
	protected.GET("/leads/:id", leadHandler.Get)
	protected.PUT("/leads/:id", leadHandler.Update)
	protected.DELETE("/leads/:id", leadHandler.Delete)
	protected.POST("/leads/:id/convert", leadHandler.ConvertToCustomer)
	
	suite.router = router
	
	// Create test users
	suite.createTestUsers()
}

func (suite *LeadIntegrationTestSuite) createTestUsers() {
	// Create admin user
	adminUser := &models.User{
		Email:     "admin@test.com",
		FirstName: "Admin",
		LastName:  "User",
		Role:      models.RoleAdmin,
		IsActive:  true,
	}
	err := suite.userService.Register(adminUser, "password123")
	suite.NoError(err)
	suite.adminUser = adminUser
	
	// Create sales user
	salesUser := &models.User{
		Email:     "sales@test.com",
		FirstName: "Sales",
		LastName:  "User",
		Role:      models.RoleSales,
		IsActive:  true,
	}
	err = suite.userService.Register(salesUser, "password123")
	suite.NoError(err)
	suite.salesUser = salesUser
	
	// Get tokens
	adminToken, err := suite.authService.Login("admin@test.com", "password123")
	suite.NoError(err)
	suite.adminToken = adminToken
	
	salesToken, err := suite.authService.Login("sales@test.com", "password123")
	suite.NoError(err)
	suite.salesToken = salesToken
}

func (suite *LeadIntegrationTestSuite) TearDownTest() {
	// Clean up leads and customers between tests
	suite.db.Unscoped().Delete(&models.Customer{}, "1=1")
	suite.db.Unscoped().Delete(&models.Lead{}, "1=1")
}

func (suite *LeadIntegrationTestSuite) makeRequestWithAuth(method, url string, body interface{}, token string) *httptest.ResponseRecorder {
	var reqBody *bytes.Buffer
	if body != nil {
		jsonBody, _ := json.Marshal(body)
		reqBody = bytes.NewBuffer(jsonBody)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}
	
	req := httptest.NewRequest(method, url, reqBody)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	
	rec := httptest.NewRecorder()
	suite.router.ServeHTTP(rec, req)
	
	return rec
}

func (suite *LeadIntegrationTestSuite) TestCreateLead_AdminSuccess() {
	payload := handler.CreateLeadRequest{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Phone:     "+1234567890",
		Company:   "Acme Inc",
		Source:    "website",
		OwnerID:   &suite.salesUser.ID,
	}
	
	rec := suite.makeRequestWithAuth("POST", "/api/v1/leads", payload, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusCreated, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	leadData := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), "John", leadData["first_name"])
	assert.Equal(suite.T(), "Doe", leadData["last_name"])
	assert.Equal(suite.T(), "john@example.com", leadData["email"])
	assert.Equal(suite.T(), float64(suite.salesUser.ID), leadData["owner_id"])
}

func (suite *LeadIntegrationTestSuite) TestCreateLead_SalesSuccess() {
	payload := handler.CreateLeadRequest{
		FirstName: "Jane",
		LastName:  "Smith",
		Email:     "jane@example.com",
		Phone:     "+1234567890",
		Company:   "Tech Corp",
		Source:    "referral",
	}
	
	rec := suite.makeRequestWithAuth("POST", "/api/v1/leads", payload, suite.salesToken)
	
	assert.Equal(suite.T(), http.StatusCreated, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	leadData := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), "Jane", leadData["first_name"])
	assert.Equal(suite.T(), float64(suite.salesUser.ID), leadData["owner_id"])
}

func (suite *LeadIntegrationTestSuite) TestCreateLead_SalesCannotAssignToOthers() {
	payload := handler.CreateLeadRequest{
		FirstName: "Bob",
		LastName:  "Wilson",
		Email:     "bob@example.com",
		OwnerID:   &suite.adminUser.ID, // Sales user trying to assign to admin
	}
	
	rec := suite.makeRequestWithAuth("POST", "/api/v1/leads", payload, suite.salesToken)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Contains(suite.T(), response.Error.Message, "You can only assign leads to yourself")
}

func (suite *LeadIntegrationTestSuite) TestListLeads_AdminSeesAll() {
	// Create leads with different owners
	lead1 := &models.Lead{
		FirstName: "Lead1",
		Email:     "lead1@example.com",
		OwnerID:   suite.adminUser.ID,
		Status:    models.LeadStatusNew,
	}
	lead2 := &models.Lead{
		FirstName: "Lead2",
		Email:     "lead2@example.com",
		OwnerID:   suite.salesUser.ID,
		Status:    models.LeadStatusNew,
	}
	
	err := suite.leadService.Create(lead1)
	suite.NoError(err)
	err = suite.leadService.Create(lead2)
	suite.NoError(err)
	
	rec := suite.makeRequestWithAuth("GET", "/api/v1/leads", nil, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	data := response.Data.(map[string]interface{})
	leads := data["leads"].([]interface{})
	assert.Len(suite.T(), leads, 2)
	assert.Equal(suite.T(), float64(2), data["total"])
}

func (suite *LeadIntegrationTestSuite) TestListLeads_SalesSeesOnlyOwn() {
	// Create leads with different owners
	lead1 := &models.Lead{
		FirstName: "Lead1",
		Email:     "lead1@example.com",
		OwnerID:   suite.adminUser.ID,
		Status:    models.LeadStatusNew,
	}
	lead2 := &models.Lead{
		FirstName: "Lead2",
		Email:     "lead2@example.com",
		OwnerID:   suite.salesUser.ID,
		Status:    models.LeadStatusNew,
	}
	
	err := suite.leadService.Create(lead1)
	suite.NoError(err)
	err = suite.leadService.Create(lead2)
	suite.NoError(err)
	
	rec := suite.makeRequestWithAuth("GET", "/api/v1/leads", nil, suite.salesToken)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	data := response.Data.(map[string]interface{})
	leads := data["leads"].([]interface{})
	assert.Len(suite.T(), leads, 1)
	assert.Equal(suite.T(), float64(1), data["total"])
	
	lead := leads[0].(map[string]interface{})
	assert.Equal(suite.T(), "Lead2", lead["first_name"])
}

func (suite *LeadIntegrationTestSuite) TestGetLead_AdminSuccess() {
	lead := &models.Lead{
		FirstName: "Test",
		LastName:  "Lead",
		Email:     "test@example.com",
		OwnerID:   suite.salesUser.ID,
		Status:    models.LeadStatusNew,
	}
	
	err := suite.leadService.Create(lead)
	suite.NoError(err)
	
	rec := suite.makeRequestWithAuth("GET", fmt.Sprintf("/api/v1/leads/%d", lead.ID), nil, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	leadData := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), "Test", leadData["first_name"])
	assert.Equal(suite.T(), "Lead", leadData["last_name"])
}

func (suite *LeadIntegrationTestSuite) TestGetLead_SalesCannotAccessOthers() {
	lead := &models.Lead{
		FirstName: "Test",
		LastName:  "Lead",
		Email:     "test@example.com",
		OwnerID:   suite.adminUser.ID,
		Status:    models.LeadStatusNew,
	}
	
	err := suite.leadService.Create(lead)
	suite.NoError(err)
	
	rec := suite.makeRequestWithAuth("GET", fmt.Sprintf("/api/v1/leads/%d", lead.ID), nil, suite.salesToken)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *LeadIntegrationTestSuite) TestUpdateLead_Success() {
	lead := &models.Lead{
		FirstName: "Test",
		LastName:  "Lead",
		Email:     "test@example.com",
		OwnerID:   suite.salesUser.ID,
		Status:    models.LeadStatusNew,
	}
	
	err := suite.leadService.Create(lead)
	suite.NoError(err)
	
	payload := handler.UpdateLeadRequest{
		FirstName: "Updated",
		LastName:  "Name",
		Status:    models.LeadStatusContacted,
	}
	
	rec := suite.makeRequestWithAuth("PUT", fmt.Sprintf("/api/v1/leads/%d", lead.ID), payload, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	leadData := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), "Updated", leadData["first_name"])
	assert.Equal(suite.T(), "Name", leadData["last_name"])
	assert.Equal(suite.T(), string(models.LeadStatusContacted), leadData["status"])
}

func (suite *LeadIntegrationTestSuite) TestDeleteLead_Success() {
	lead := &models.Lead{
		FirstName: "Test",
		LastName:  "Lead",
		Email:     "test@example.com",
		OwnerID:   suite.salesUser.ID,
		Status:    models.LeadStatusNew,
	}
	
	err := suite.leadService.Create(lead)
	suite.NoError(err)
	
	rec := suite.makeRequestWithAuth("DELETE", fmt.Sprintf("/api/v1/leads/%d", lead.ID), nil, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusNoContent, rec.Code)
	
	// Verify lead is deleted
	_, err = suite.leadService.GetByID(lead.ID)
	assert.Error(suite.T(), err)
}

func (suite *LeadIntegrationTestSuite) TestConvertToCustomer_Success() {
	lead := &models.Lead{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Phone:     "+1234567890",
		Company:   "Acme Inc",
		OwnerID:   suite.salesUser.ID,
		Status:    models.LeadStatusQualified,
	}
	
	err := suite.leadService.Create(lead)
	suite.NoError(err)
	
	payload := handler.ConvertLeadRequest{
		CompanyName: "Acme Corporation",
		Address:     "123 Main St",
		Notes:       "Converted from qualified lead",
	}
	
	rec := suite.makeRequestWithAuth("POST", fmt.Sprintf("/api/v1/leads/%d/convert", lead.ID), payload, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	customerData := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), "John", customerData["first_name"])
	assert.Equal(suite.T(), "Doe", customerData["last_name"])
	assert.Equal(suite.T(), "john@example.com", customerData["email"])
	assert.Equal(suite.T(), "Acme Corporation", customerData["company"])
	
	// Verify lead status is updated
	updatedLead, err := suite.leadService.GetByID(lead.ID)
	suite.NoError(err)
	assert.Equal(suite.T(), models.LeadStatusConverted, updatedLead.Status)
}

func (suite *LeadIntegrationTestSuite) TestConvertToCustomer_AlreadyConverted() {
	lead := &models.Lead{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		OwnerID:   suite.salesUser.ID,
		Status:    models.LeadStatusConverted,
	}
	
	err := suite.leadService.Create(lead)
	suite.NoError(err)
	
	payload := handler.ConvertLeadRequest{
		CompanyName: "Acme Corporation",
	}
	
	rec := suite.makeRequestWithAuth("POST", fmt.Sprintf("/api/v1/leads/%d/convert", lead.ID), payload, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	
	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Contains(suite.T(), response.Error.Message, "lead already converted")
}

func (suite *LeadIntegrationTestSuite) TestConvertToCustomer_SalesUserForbidden() {
	lead := &models.Lead{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		OwnerID:   suite.adminUser.ID, // Sales user trying to convert admin's lead
		Status:    models.LeadStatusQualified,
	}
	
	err := suite.leadService.Create(lead)
	suite.NoError(err)
	
	payload := handler.ConvertLeadRequest{
		CompanyName: "Acme Corporation",
	}
	
	rec := suite.makeRequestWithAuth("POST", fmt.Sprintf("/api/v1/leads/%d/convert", lead.ID), payload, suite.salesToken)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *LeadIntegrationTestSuite) TestUnauthorizedAccess() {
	rec := suite.makeRequestWithAuth("GET", "/api/v1/leads", nil, "")
	assert.Equal(suite.T(), http.StatusUnauthorized, rec.Code)
	
	rec = suite.makeRequestWithAuth("GET", "/api/v1/leads", nil, "invalid-token")
	assert.Equal(suite.T(), http.StatusUnauthorized, rec.Code)
}

// Helper function
func stringPtr(s string) *string {
	return &s
}

func TestLeadIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(LeadIntegrationTestSuite))
}