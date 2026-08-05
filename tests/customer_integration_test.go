package tests

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
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
	inactiveUser    *models.User
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
	// The static /customers/export segment coexists with the /customers/:id
	// wildcard; gin resolves the literal first. Registered here exactly as
	// SetupCustomerRoutes registers it, minus the RequireRole middleware, so the
	// handler's own guard is what these tests exercise.
	protected.GET("/customers/export", customerHandler.Export)
	protected.GET("/customers/:id", customerHandler.Get)
	protected.PUT("/customers/:id", customerHandler.Update)
	protected.POST("/customers/:id/assign", customerHandler.Assign)
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
	
	// A deactivated sales account: it holds a role that could own customers, so
	// it isolates the "inactive" rejection from the "wrong role" one.
	inactiveUser := &models.User{
		Email:     "inactive-sales@test.com",
		FirstName: "Inactive",
		LastName:  "Sales",
		Role:      models.RoleSales,
		IsActive:  true,
	}
	err = suite.userService.Register(inactiveUser, "password123")
	suite.NoError(err)
	suite.NoError(suite.db.Model(&models.User{}).Where("id = ?", inactiveUser.ID).Update("is_active", false).Error)
	inactiveUser.IsActive = false
	suite.inactiveUser = inactiveUser

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

	// A duplicate email conflicts with existing state, so it is 409, not 400.
	assert.Equal(suite.T(), http.StatusConflict, rec.Code)
	
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

	// A duplicate email conflicts with existing state, so it is 409, not 400.
	assert.Equal(suite.T(), http.StatusConflict, rec.Code)
	
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
// --- CSV export --------------------------------------------------------------

// seedCustomer inserts a customer directly, bypassing the API, so an export test
// does not depend on the create endpoint's own rules.
func (suite *CustomerIntegrationTestSuite) seedCustomer(customer *models.Customer) *models.Customer {
	suite.NoError(suite.db.Create(customer).Error)
	return customer
}

func (suite *CustomerIntegrationTestSuite) TestExportCustomers_AdminGetsCSVFile() {
	assignee := suite.salesUser.ID
	suite.seedCustomer(&models.Customer{
		FirstName: "John", LastName: "Doe", Email: "john.export@test.com",
		Phone: "+1234567890", Company: "Acme Corp", Address: "123 Main St",
		Notes: "Prefers email", AssignedToID: &assignee,
	})
	suite.seedCustomer(&models.Customer{
		FirstName: "Jane", LastName: "Smith", Email: "jane.export@test.com",
	})

	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers/export", nil, suite.adminToken)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	assert.Equal(suite.T(), "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(suite.T(), "attachment; filename=customers-export.csv", rec.Header().Get("Content-Disposition"))

	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	suite.NoError(err)
	suite.Require().Len(records, 3, "header row plus one row per customer")
	assert.Equal(suite.T(), []string{
		"id", "first_name", "last_name", "email", "phone", "company",
		"address", "notes", "assigned_to_id", "created_at", "updated_at",
	}, records[0])

	byEmail := map[string][]string{}
	for _, record := range records[1:] {
		byEmail[record[3]] = record
	}
	suite.Require().Contains(byEmail, "john.export@test.com")
	assert.Equal(suite.T(), "Acme Corp", byEmail["john.export@test.com"][5])
	assert.Equal(suite.T(), fmt.Sprintf("%d", assignee), byEmail["john.export@test.com"][8])
	assert.Equal(suite.T(), "", byEmail["jane.export@test.com"][8], "an unassigned customer exports an empty cell")
}

func (suite *CustomerIntegrationTestSuite) TestExportCustomers_HonoursSearch() {
	suite.seedCustomer(&models.Customer{FirstName: "John", LastName: "Doe", Email: "john.search@test.com", Company: "Acme Corp"})
	suite.seedCustomer(&models.Customer{FirstName: "Jane", LastName: "Smith", Email: "jane.search@test.com", Company: "Globex"})

	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers/export?search=Globex", nil, suite.adminToken)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	suite.NoError(err)
	suite.Require().Len(records, 2)
	assert.Equal(suite.T(), "jane.search@test.com", records[1][3])
}

// An empty result set is still a valid file: the header row is what tells the
// recipient the download worked and simply matched nothing.
func (suite *CustomerIntegrationTestSuite) TestExportCustomers_EmptyDatabaseYieldsHeaderOnly() {
	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers/export", nil, suite.adminToken)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	suite.NoError(err)
	assert.Len(suite.T(), records, 1)
}

// An erased customer must not come back through the export.
func (suite *CustomerIntegrationTestSuite) TestExportCustomers_ExcludesErasedCustomers() {
	erased := suite.seedCustomer(&models.Customer{FirstName: "Gone", LastName: "Customer", Email: "gone.export@test.com"})
	suite.seedCustomer(&models.Customer{FirstName: "Still", LastName: "Here", Email: "here.export@test.com"})

	deleteRec := suite.makeRequestWithAuth("DELETE", fmt.Sprintf("/api/v1/customers/%d", erased.ID), nil, suite.adminToken)
	suite.Require().Equal(http.StatusNoContent, deleteRec.Code)

	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers/export", nil, suite.adminToken)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	suite.NoError(err)
	suite.Require().Len(records, 2)
	assert.Equal(suite.T(), "here.export@test.com", records[1][3])
	assert.NotContains(suite.T(), rec.Body.String(), "gone.export@test.com")
}

// The export is a bulk PII egress and is narrower than the list endpoint that
// sales and support can both read.
func (suite *CustomerIntegrationTestSuite) TestExportCustomers_SalesForbidden() {
	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers/export", nil, suite.salesToken)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
	assert.NotContains(suite.T(), rec.Header().Get("Content-Type"), "text/csv")
}

func (suite *CustomerIntegrationTestSuite) TestExportCustomers_SupportForbidden() {
	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers/export", nil, suite.supportToken)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *CustomerIntegrationTestSuite) TestExportCustomers_Unauthenticated() {
	rec := suite.makeRequestWithAuth("GET", "/api/v1/customers/export", nil, "")

	assert.Equal(suite.T(), http.StatusUnauthorized, rec.Code)
}

// --- Assignment --------------------------------------------------------------

func (suite *CustomerIntegrationTestSuite) TestAssignCustomer_AdminSuccess() {
	customer := suite.seedCustomer(&models.Customer{FirstName: "John", LastName: "Doe", Email: "john.assign@test.com"})

	rec := suite.makeRequestWithAuth("POST",
		fmt.Sprintf("/api/v1/customers/%d/assign", customer.ID),
		handler.AssignCustomerRequest{UserID: suite.salesUser.ID},
		suite.adminToken)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	suite.NoError(json.Unmarshal(rec.Body.Bytes(), &response))
	assert.True(suite.T(), response.Success)
	data := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), float64(suite.salesUser.ID), data["assigned_to_id"])

	// ...and it is actually persisted, not just echoed back.
	var stored models.Customer
	suite.NoError(suite.db.First(&stored, customer.ID).Error)
	suite.Require().NotNil(stored.AssignedToID)
	assert.Equal(suite.T(), suite.salesUser.ID, *stored.AssignedToID)
}

func (suite *CustomerIntegrationTestSuite) TestAssignCustomer_SalesCallerAllowed() {
	customer := suite.seedCustomer(&models.Customer{FirstName: "John", LastName: "Doe", Email: "john.salesassign@test.com"})

	rec := suite.makeRequestWithAuth("POST",
		fmt.Sprintf("/api/v1/customers/%d/assign", customer.ID),
		handler.AssignCustomerRequest{UserID: suite.adminUser.ID},
		suite.salesToken)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *CustomerIntegrationTestSuite) TestAssignCustomer_SupportCallerForbidden() {
	customer := suite.seedCustomer(&models.Customer{FirstName: "John", LastName: "Doe", Email: "john.supportassign@test.com"})

	rec := suite.makeRequestWithAuth("POST",
		fmt.Sprintf("/api/v1/customers/%d/assign", customer.ID),
		handler.AssignCustomerRequest{UserID: suite.salesUser.ID},
		suite.supportToken)

	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *CustomerIntegrationTestSuite) TestAssignCustomer_CustomerNotFound() {
	rec := suite.makeRequestWithAuth("POST", "/api/v1/customers/99999/assign",
		handler.AssignCustomerRequest{UserID: suite.salesUser.ID}, suite.adminToken)

	assert.Equal(suite.T(), http.StatusNotFound, rec.Code)
}

func (suite *CustomerIntegrationTestSuite) TestAssignCustomer_UserNotFound() {
	customer := suite.seedCustomer(&models.Customer{FirstName: "John", LastName: "Doe", Email: "john.nouser@test.com"})

	rec := suite.makeRequestWithAuth("POST",
		fmt.Sprintf("/api/v1/customers/%d/assign", customer.ID),
		handler.AssignCustomerRequest{UserID: 99999}, suite.adminToken)

	assert.Equal(suite.T(), http.StatusNotFound, rec.Code)
}

// A deactivated account exists; the request is refused on its merits, so 400.
func (suite *CustomerIntegrationTestSuite) TestAssignCustomer_InactiveUserIsBadRequest() {
	customer := suite.seedCustomer(&models.Customer{FirstName: "John", LastName: "Doe", Email: "john.inactive@test.com"})

	rec := suite.makeRequestWithAuth("POST",
		fmt.Sprintf("/api/v1/customers/%d/assign", customer.ID),
		handler.AssignCustomerRequest{UserID: suite.inactiveUser.ID}, suite.adminToken)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)

	var stored models.Customer
	suite.NoError(suite.db.First(&stored, customer.ID).Error)
	assert.Nil(suite.T(), stored.AssignedToID, "a rejected assignment must not be persisted")
}

func (suite *CustomerIntegrationTestSuite) TestAssignCustomer_SupportTargetIsBadRequest() {
	customer := suite.seedCustomer(&models.Customer{FirstName: "John", LastName: "Doe", Email: "john.supporttarget@test.com"})

	rec := suite.makeRequestWithAuth("POST",
		fmt.Sprintf("/api/v1/customers/%d/assign", customer.ID),
		handler.AssignCustomerRequest{UserID: suite.supportUser.ID}, suite.adminToken)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
}

// Handing a customer-role account a book of other people's records would be a
// data-protection incident, not a typo.
func (suite *CustomerIntegrationTestSuite) TestAssignCustomer_CustomerTargetIsBadRequest() {
	customer := suite.seedCustomer(&models.Customer{FirstName: "John", LastName: "Doe", Email: "john.customertarget@test.com"})

	rec := suite.makeRequestWithAuth("POST",
		fmt.Sprintf("/api/v1/customers/%d/assign", customer.ID),
		handler.AssignCustomerRequest{UserID: suite.customerUser.ID}, suite.adminToken)

	assert.Equal(suite.T(), http.StatusBadRequest, rec.Code)
}

// Erasure must keep working with the new column present, and must NOT treat
// assigned_to_id as personal data: who handled the account is business history,
// and the staff member has their own erasure path.
func (suite *CustomerIntegrationTestSuite) TestAssignedCustomerErasureKeepsTheAssignmentAndClearsThePII() {
	customer := suite.seedCustomer(&models.Customer{
		FirstName: "Erasable", LastName: "Customer", Email: "erasable.assign@test.com",
		Phone: "+40 700 111 222", Notes: "Ring after six",
	})

	assignRec := suite.makeRequestWithAuth("POST",
		fmt.Sprintf("/api/v1/customers/%d/assign", customer.ID),
		handler.AssignCustomerRequest{UserID: suite.salesUser.ID}, suite.adminToken)
	suite.Require().Equal(http.StatusOK, assignRec.Code)

	deleteRec := suite.makeRequestWithAuth("DELETE", fmt.Sprintf("/api/v1/customers/%d", customer.ID), nil, suite.adminToken)
	suite.Require().Equal(http.StatusNoContent, deleteRec.Code)

	var erased models.Customer
	suite.NoError(suite.db.Unscoped().First(&erased, customer.ID).Error)
	assert.Empty(suite.T(), erased.FirstName)
	assert.Empty(suite.T(), erased.LastName)
	assert.Empty(suite.T(), erased.Phone)
	assert.Empty(suite.T(), erased.Notes)
	assert.NotEqual(suite.T(), "erasable.assign@test.com", erased.Email)
	suite.Require().NotNil(erased.AssignedToID, "assigned_to_id is a foreign key to a staff account, not personal data, and must survive erasure")
	assert.Equal(suite.T(), suite.salesUser.ID, *erased.AssignedToID)
}
