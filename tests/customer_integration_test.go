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

type CustomerIntegrationTestSuite struct {
	suite.Suite
	db              *gorm.DB
	router          *gin.Engine
	authService     service.AuthService
	userService     service.UserService
	customerService service.CustomerService
	adminUser       *models.User
	salesUser       *models.User
	supportUser     *models.User
	customerUser    *models.User
	adminToken      string
	salesToken      string
	supportToken    string
	customerToken   string
}

func (suite *CustomerIntegrationTestSuite) SetupSuite() {
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
	err = db.AutoMigrate(&models.User{}, &models.APIKey{}, &models.Customer{})
	suite.NoError(err)
	
	suite.db = db
	
	// Initialize repositories
	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	
	// Initialize services
	jwtConfig := config.JWTConfig{
		Secret:      "test-secret",
		ExpiryHours: 24,
	}
	suite.authService = service.NewAuthService(userRepo, apiKeyRepo, jwtConfig)
	suite.userService = service.NewUserService(userRepo)
	suite.customerService = service.NewCustomerService(customerRepo, userRepo)
	
	// Initialize handlers
	authHandler := handler.NewAuthHandler(suite.authService, suite.userService)
	userHandler := handler.NewUserHandler(suite.userService)
	customerHandler := handler.NewCustomerHandler(suite.customerService)
	
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
	
	// Customer routes
	protected.POST("/customers", customerHandler.Create)
	protected.GET("/customers", customerHandler.List)
	protected.GET("/customers/:id", customerHandler.Get)
	protected.PUT("/customers/:id", customerHandler.Update)
	protected.DELETE("/customers/:id", customerHandler.Delete)
	
	suite.router = router
	
	// Create test users
	suite.createTestUsers()
}

func (suite *CustomerIntegrationTestSuite) createTestUsers() {
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
	
	// Create support user
	supportUser := &models.User{
		Email:     "support@test.com",
		FirstName: "Support",
		LastName:  "User",
		Role:      models.RoleSupport,
		IsActive:  true,
	}
	err = suite.userService.Register(supportUser, "password123")
	suite.NoError(err)
	suite.supportUser = supportUser
	
	// Create customer user (not allowed to access customer endpoints)
	customerUser := &models.User{
		Email:     "customer@test.com",
		FirstName: "Customer",
		LastName:  "User",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	err = suite.userService.Register(customerUser, "password123")
	suite.NoError(err)
	suite.customerUser = customerUser
	
	// Get tokens
	adminToken, err := suite.authService.Login("admin@test.com", "password123")
	suite.NoError(err)
	suite.adminToken = adminToken
	
	salesToken, err := suite.authService.Login("sales@test.com", "password123")
	suite.NoError(err)
	suite.salesToken = salesToken
	
	supportToken, err := suite.authService.Login("support@test.com", "password123")
	suite.NoError(err)
	suite.supportToken = supportToken
	
	customerToken, err := suite.authService.Login("customer@test.com", "password123")
	suite.NoError(err)
	suite.customerToken = customerToken
}

func (suite *CustomerIntegrationTestSuite) TearDownTest() {
	// Clean up customers between tests
	suite.db.Unscoped().Delete(&models.Customer{}, "1=1")
}

func (suite *CustomerIntegrationTestSuite) makeRequestWithAuth(method, url string, body interface{}, token string) *httptest.ResponseRecorder {
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

func (suite *CustomerIntegrationTestSuite) TestCreateCustomer_AdminSuccess() {
	payload := handler.CreateCustomerRequest{
		FirstName:  "John",
		LastName:   "Doe",
		Email:      "john@example.com",
		Phone:      "+1234567890",
		Company:    "Acme Corp",
		Position:   "CEO",
		Address:    "123 Main St",
		City:       "New York",
		State:      "NY",
		Country:    "USA",
		PostalCode: "10001",
		Notes:      "Important customer",
	}
	
	rec := suite.makeRequestWithAuth("POST", "/api/v1/customers", payload, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusCreated, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	customerData := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), "John", customerData["first_name"])
	assert.Equal(suite.T(), "Doe", customerData["last_name"])
	assert.Equal(suite.T(), "john@example.com", customerData["email"])
	assert.Equal(suite.T(), "Acme Corp", customerData["company"])
}

func (suite *CustomerIntegrationTestSuite) TestCreateCustomer_SalesSuccess() {
	payload := handler.CreateCustomerRequest{
		FirstName: "Jane",
		LastName:  "Smith",
		Email:     "jane@example.com",
		Phone:     "+1234567890",
		Company:   "Tech Corp",
	}
	
	rec := suite.makeRequestWithAuth("POST", "/api/v1/customers", payload, suite.salesToken)
	
	assert.Equal(suite.T(), http.StatusCreated, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *CustomerIntegrationTestSuite) TestCreateCustomer_DuplicateEmail() {
	// Create first customer
	customer := &models.Customer{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "duplicate@example.com",
		Company:   "Acme Corp",
	}
	err := suite.customerService.Create(customer)
	suite.NoError(err)
	
	// Try to create another with same email
	payload := handler.CreateCustomerRequest{
		FirstName: "Jane",
		LastName:  "Smith",
		Email:     "duplicate@example.com",
		Company:   "Tech Corp",
	}
	
	rec := suite.makeRequestWithAuth("POST", "/api/v1/customers", payload, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	
	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Contains(suite.T(), response.Error.Message, "customer with this email already exists")
}

func (suite *CustomerIntegrationTestSuite) TestCreateCustomer_SupportUserForbidden() {
	payload := handler.CreateCustomerRequest{
		FirstName: "Bob",
		LastName:  "Wilson",
		Email:     "bob@example.com",
	}
	
	rec := suite.makeRequestWithAuth("POST", "/api/v1/customers", payload, suite.supportToken)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *CustomerIntegrationTestSuite) TestListCustomers_AdminSeesAll() {
	// Create some customers
	customer1 := &models.Customer{
		FirstName: "Customer1",
		LastName:  "Test",
		Email:     "customer1@example.com",
		Company:   "Company1",
	}
	customer2 := &models.Customer{
		FirstName: "Customer2",
		LastName:  "Test",
		Email:     "customer2@example.com",
		Company:   "Company2",
	}
	
	err := suite.customerService.Create(customer1)
	suite.NoError(err)
	err = suite.customerService.Create(customer2)
	suite.NoError(err)
	
	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers", nil, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	data := response.Data.(map[string]interface{})
	customers := data["customers"].([]interface{})
	assert.Len(suite.T(), customers, 2)
	assert.Equal(suite.T(), float64(2), data["total"])
}

func (suite *CustomerIntegrationTestSuite) TestListCustomers_SalesCanSee() {
	// Create a customer
	customer := &models.Customer{
		FirstName: "Sales",
		LastName:  "Customer",
		Email:     "sales.customer@example.com",
		Company:   "Sales Company",
	}
	
	err := suite.customerService.Create(customer)
	suite.NoError(err)
	
	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers", nil, suite.salesToken)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *CustomerIntegrationTestSuite) TestListCustomers_SupportCanSee() {
	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers", nil, suite.supportToken)
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *CustomerIntegrationTestSuite) TestListCustomers_CustomerRoleForbidden() {
	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers", nil, suite.customerToken)
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *CustomerIntegrationTestSuite) TestGetCustomer_Success() {
	customer := &models.Customer{
		FirstName: "Test",
		LastName:  "Customer",
		Email:     "test@example.com",
		Company:   "Test Company",
	}
	
	err := suite.customerService.Create(customer)
	suite.NoError(err)
	
	rec := suite.makeRequestWithAuth("GET", fmt.Sprintf("/api/v1/customers/%d", customer.ID), nil, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	customerData := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), "Test", customerData["first_name"])
	assert.Equal(suite.T(), "Customer", customerData["last_name"])
}

func (suite *CustomerIntegrationTestSuite) TestGetCustomer_NotFound() {
	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers/999", nil, suite.adminToken)
	assert.Equal(suite.T(), http.StatusNotFound, rec.Code)
}

func (suite *CustomerIntegrationTestSuite) TestUpdateCustomer_AdminSuccess() {
	customer := &models.Customer{
		FirstName: "Original",
		LastName:  "Name",
		Email:     "original@example.com",
		Company:   "Original Company",
	}
	
	err := suite.customerService.Create(customer)
	suite.NoError(err)
	
	payload := handler.UpdateCustomerRequest{
		FirstName: "Updated",
		LastName:  "Customer",
		Company:   "Updated Company",
	}
	
	rec := suite.makeRequestWithAuth("PUT", fmt.Sprintf("/api/v1/customers/%d", customer.ID), payload, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	customerData := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), "Updated", customerData["first_name"])
	assert.Equal(suite.T(), "Customer", customerData["last_name"])
	assert.Equal(suite.T(), "Updated Company", customerData["company"])
	assert.Equal(suite.T(), "original@example.com", customerData["email"]) // Email unchanged
}

func (suite *CustomerIntegrationTestSuite) TestUpdateCustomer_DuplicateEmail() {
	// Create two customers
	customer1 := &models.Customer{
		FirstName: "Customer1",
		LastName:  "Test",
		Email:     "customer1@example.com",
	}
	customer2 := &models.Customer{
		FirstName: "Customer2",
		LastName:  "Test",
		Email:     "customer2@example.com",
	}
	
	err := suite.customerService.Create(customer1)
	suite.NoError(err)
	err = suite.customerService.Create(customer2)
	suite.NoError(err)
	
	// Try to update customer2 with customer1's email
	payload := handler.UpdateCustomerRequest{
		Email: "customer1@example.com",
	}
	
	rec := suite.makeRequestWithAuth("PUT", fmt.Sprintf("/api/v1/customers/%d", customer2.ID), payload, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
	
	var response utils.APIResponse
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Contains(suite.T(), response.Error.Message, "customer with this email already exists")
}

func (suite *CustomerIntegrationTestSuite) TestUpdateCustomer_SupportUserForbidden() {
	customer := &models.Customer{
		FirstName: "Test",
		LastName:  "Customer",
		Email:     "test@example.com",
	}
	
	err := suite.customerService.Create(customer)
	suite.NoError(err)
	
	payload := handler.UpdateCustomerRequest{
		FirstName: "Updated",
	}
	
	rec := suite.makeRequestWithAuth("PUT", fmt.Sprintf("/api/v1/customers/%d", customer.ID), payload, suite.supportToken)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *CustomerIntegrationTestSuite) TestDeleteCustomer_AdminSuccess() {
	customer := &models.Customer{
		FirstName: "ToDelete",
		LastName:  "Customer",
		Email:     "delete@example.com",
	}
	
	err := suite.customerService.Create(customer)
	suite.NoError(err)
	
	rec := suite.makeRequestWithAuth("DELETE", fmt.Sprintf("/api/v1/customers/%d", customer.ID), nil, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusNoContent, rec.Code)
	
	// Verify customer is deleted
	_, err = suite.customerService.GetByID(customer.ID)
	assert.Error(suite.T(), err)
}

func (suite *CustomerIntegrationTestSuite) TestDeleteCustomer_SalesUserForbidden() {
	customer := &models.Customer{
		FirstName: "Test",
		LastName:  "Customer",
		Email:     "test@example.com",
	}
	
	err := suite.customerService.Create(customer)
	suite.NoError(err)
	
	rec := suite.makeRequestWithAuth("DELETE", fmt.Sprintf("/api/v1/customers/%d", customer.ID), nil, suite.salesToken)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *CustomerIntegrationTestSuite) TestUnauthorizedAccess() {
	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers", nil, "")
	assert.Equal(suite.T(), http.StatusUnauthorized, rec.Code)
	
	rec = suite.makeRequestWithAuth("GET", "/api/v1/customers", nil, "invalid-token")
	assert.Equal(suite.T(), http.StatusUnauthorized, rec.Code)
}

func (suite *CustomerIntegrationTestSuite) TestPagination() {
	// Create multiple customers
	for i := 0; i < 5; i++ {
		customer := &models.Customer{
			FirstName: fmt.Sprintf("Customer%d", i+1),
			LastName:  "Test",
			Email:     fmt.Sprintf("customer%d@example.com", i+1),
			Company:   fmt.Sprintf("Company%d", i+1),
		}
		err := suite.customerService.Create(customer)
		suite.NoError(err)
	}
	
	// Get first page
	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers?limit=2", nil, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	
	data := response.Data.(map[string]interface{})
	customers := data["customers"].([]interface{})
	assert.Len(suite.T(), customers, 2)
	assert.Equal(suite.T(), float64(5), data["total"])
	
	// Check metadata
	assert.Equal(suite.T(), 1, int(response.Meta.Page))
	assert.Equal(suite.T(), 2, response.Meta.PerPage)
	assert.Equal(suite.T(), int64(5), response.Meta.Total)
	assert.Equal(suite.T(), int64(3), response.Meta.TotalPages)
	
	// Get second page
	rec = suite.makeRequestWithAuth("GET", "/api/v1/customers?offset=2&limit=2", nil, suite.adminToken)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	
	data = response.Data.(map[string]interface{})
	customers = data["customers"].([]interface{})
	assert.Len(suite.T(), customers, 2)
	assert.Equal(suite.T(), 2, int(response.Meta.Page))
}

func TestCustomerIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(CustomerIntegrationTestSuite))
}