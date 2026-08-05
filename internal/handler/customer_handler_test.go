package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

type CustomerHandlerTestSuite struct {
	suite.Suite
	mockService *mocks.CustomerService
	handler     *CustomerHandler
	router      *gin.Engine
}

func (suite *CustomerHandlerTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)
}

func (suite *CustomerHandlerTestSuite) SetupTest() {
	gin.SetMode(gin.TestMode)
	suite.mockService = new(mocks.CustomerService)
	suite.handler = NewCustomerHandler(suite.mockService)
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", "admin")
		c.Set("request_id", "test-request-id")
		c.Next()
	})
}

func (suite *CustomerHandlerTestSuite) TearDownTest() {
	suite.mockService.AssertExpectations(suite.T())
}

func (suite *CustomerHandlerTestSuite) TestCreate_Success() {
	suite.router.POST("/customers", suite.handler.Create)
	
	payload := CreateCustomerRequest{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Phone:     "+1234567890",
		Company:   "Acme Corp",
		Position:  "CEO",
		Address:   "123 Main St",
		City:      "New York",
		State:     "NY",
		Country:   "USA",
		PostalCode:"10001",
		Notes:     "Important customer",
	}
	
	suite.mockService.On("Create", mock.MatchedBy(func(c *models.Customer) bool {
		return c.FirstName == "John" &&
			c.LastName == "Doe" &&
			c.Email == "john@example.com" &&
			c.Phone == "+1234567890" &&
			c.Company == "Acme Corp" &&
			c.Position == "CEO" &&
			c.Address == "123 Main St" &&
			c.City == "New York" &&
			c.State == "NY" &&
			c.Country == "USA" &&
			c.PostalCode == "10001" &&
			c.Notes == "Important customer"
	})).Return(nil).Run(func(args mock.Arguments) {
		customer := args.Get(0).(*models.Customer)
		customer.ID = 1
	})
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/customers", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusCreated, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Data)
}

func (suite *CustomerHandlerTestSuite) TestCreate_DuplicateEmail() {
	suite.router.POST("/customers", suite.handler.Create)
	
	payload := CreateCustomerRequest{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}
	
	suite.mockService.On("Create", mock.Anything).Return(fmt.Errorf("customer with this email already exists: %w", apperrors.ErrDuplicateEmail))
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/customers", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)

	// A duplicate email conflicts with existing state, so it is 409, not 400.
	assert.Equal(suite.T(), http.StatusConflict, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "customer with this email already exists", response.Error.Message)
	assertNoDriverInternalsInBody(suite.T(), rec.Body.String())
}

func (suite *CustomerHandlerTestSuite) TestCreate_ForbiddenForSupportUser() {
	suite.router.POST("/customers", suite.handler.Create)
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_role", "support")
	})
	
	payload := CreateCustomerRequest{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/customers", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	// Create a new context with support role
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Set("user_id", uint(1))
	ctx.Set("user_role", "support")
	ctx.Set("request_id", "test-request-id")
	ctx.Request = req
	
	suite.handler.Create(ctx)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *CustomerHandlerTestSuite) TestList_Success() {
	suite.router.GET("/customers", suite.handler.List)
	
	expectedCustomers := []models.Customer{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", LastName: "Doe", Email: "john@example.com"},
		{BaseModel: models.BaseModel{ID: 2}, FirstName: "Jane", LastName: "Smith", Email: "jane@example.com"},
	}
	
	suite.mockService.On("List", 0, 20).Return(expectedCustomers, int64(2), nil)
	
	req := httptest.NewRequest(http.MethodGet, "/customers", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	data := response.Data.(map[string]interface{})
	assert.Len(suite.T(), data["customers"], 2)
	assert.Equal(suite.T(), float64(2), data["total"])
	assert.NotNil(suite.T(), response.Meta)
}

func (suite *CustomerHandlerTestSuite) TestList_WithPagination() {
	suite.router.GET("/customers", suite.handler.List)
	
	expectedCustomers := []models.Customer{
		{BaseModel: models.BaseModel{ID: 3}, FirstName: "Bob", LastName: "Wilson", Email: "bob@example.com"},
	}
	
	suite.mockService.On("List", 2, 1).Return(expectedCustomers, int64(3), nil)
	
	req := httptest.NewRequest(http.MethodGet, "/customers?offset=2&limit=1", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	assert.Equal(suite.T(), 3, int(response.Meta.Page))
	assert.Equal(suite.T(), 1, response.Meta.PerPage)
	assert.Equal(suite.T(), int64(3), response.Meta.Total)
	assert.Equal(suite.T(), int64(3), response.Meta.TotalPages)
}

func (suite *CustomerHandlerTestSuite) TestGet_Success() {
	suite.router.GET("/customers/:id", suite.handler.Get)
	
	expectedCustomer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Company:   "Acme Corp",
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(expectedCustomer, nil)
	
	req := httptest.NewRequest(http.MethodGet, "/customers/1", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	
	customerData := response.Data.(map[string]interface{})
	assert.Equal(suite.T(), "John", customerData["first_name"])
	assert.Equal(suite.T(), "Doe", customerData["last_name"])
	assert.Equal(suite.T(), "john@example.com", customerData["email"])
}

func (suite *CustomerHandlerTestSuite) TestGet_NotFound() {
	suite.router.GET("/customers/:id", suite.handler.Get)
	
	suite.mockService.On("GetByID", uint(999)).Return(nil, errors.New("not found"))
	
	req := httptest.NewRequest(http.MethodGet, "/customers/999", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusNotFound, rec.Code)
}

func (suite *CustomerHandlerTestSuite) TestUpdate_Success() {
	suite.router.PUT("/customers/:id", suite.handler.Update)
	
	existingCustomer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Company:   "Acme Corp",
	}
	
	payload := UpdateCustomerRequest{
		FirstName: "Jane",
		LastName:  "Smith",
		Company:   "Tech Corp",
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(existingCustomer, nil)
	suite.mockService.On("Update", mock.MatchedBy(func(c *models.Customer) bool {
		return c.ID == 1 &&
			c.FirstName == "Jane" &&
			c.LastName == "Smith" &&
			c.Email == "john@example.com" && // Email unchanged
			c.Company == "Tech Corp"
	})).Return(nil)
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/customers/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *CustomerHandlerTestSuite) TestUpdate_DuplicateEmail() {
	suite.router.PUT("/customers/:id", suite.handler.Update)
	
	existingCustomer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}
	
	payload := UpdateCustomerRequest{
		Email: "existing@example.com",
	}
	
	suite.mockService.On("GetByID", uint(1)).Return(existingCustomer, nil)
	suite.mockService.On("Update", mock.Anything).Return(fmt.Errorf("customer with this email already exists: %w", apperrors.ErrDuplicateEmail))
	
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/customers/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)

	// A duplicate email conflicts with existing state, so it is 409, not 400.
	assert.Equal(suite.T(), http.StatusConflict, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "customer with this email already exists", response.Error.Message)
	assertNoDriverInternalsInBody(suite.T(), rec.Body.String())
}

func (suite *CustomerHandlerTestSuite) TestDelete_Success() {
	suite.router.DELETE("/customers/:id", suite.handler.Delete)
	
	suite.mockService.On("Delete", uint(1)).Return(nil)
	
	req := httptest.NewRequest(http.MethodDelete, "/customers/1", nil)
	rec := httptest.NewRecorder()
	
	suite.router.ServeHTTP(rec, req)
	
	assert.Equal(suite.T(), http.StatusNoContent, rec.Code)
}

func (suite *CustomerHandlerTestSuite) TestDelete_NotFound() {
	suite.router.DELETE("/customers/:id", suite.handler.Delete)

	suite.mockService.On("Delete", uint(1)).
		Return(fmt.Errorf("customer %d not found: %w", 1, apperrors.ErrNotFound))

	req := httptest.NewRequest(http.MethodDelete, "/customers/1", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusNotFound, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "Customer not found", response.Error.Message)
	// The 404 says nothing beyond "no such customer" - no id, no internal detail.
	assert.Nil(suite.T(), response.Error.Details)
}

func (suite *CustomerHandlerTestSuite) TestDelete_GenuineFailureIsInternalError() {
	suite.router.DELETE("/customers/:id", suite.handler.Delete)

	// A failure part-way through an erasure must NOT look like an id that
	// never existed - the operator would record a completed erasure request
	// for personal data that is still in the database.
	dbErr := errors.New("Error 1213: Deadlock found when trying to get lock; try restarting transaction")
	suite.mockService.On("Delete", uint(1)).Return(dbErr)

	req := httptest.NewRequest(http.MethodDelete, "/customers/1", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusInternalServerError, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "An unexpected error occurred", response.Error.Message)
	assert.Nil(suite.T(), response.Error.Details)

	// The response body must leak neither the driver text nor the sentinel.
	body := rec.Body.String()
	assert.NotContains(suite.T(), body, "Deadlock")
	assert.NotContains(suite.T(), body, "1213")
	assert.NotContains(suite.T(), body, "not found")
}

func (suite *CustomerHandlerTestSuite) TestDelete_ForbiddenForNonAdmin() {
	suite.router.DELETE("/customers/:id", suite.handler.Delete)
	
	req := httptest.NewRequest(http.MethodDelete, "/customers/1", nil)
	rec := httptest.NewRecorder()
	
	// Create a new context with sales role
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Set("user_id", uint(2))
	ctx.Set("user_role", "sales")
	ctx.Set("request_id", "test-request-id")
	ctx.Request = req
	ctx.Params = []gin.Param{{Key: "id", Value: "1"}}
	
	suite.handler.Delete(ctx)
	
	assert.Equal(suite.T(), http.StatusForbidden, rec.Code)
}

func (suite *CustomerHandlerTestSuite) TestList_SortByCreatedAtDesc() {
	suite.router.GET("/customers", suite.handler.List)

	expectedCustomers := []models.Customer{
		{BaseModel: models.BaseModel{ID: 2}, FirstName: "Jane", Email: "jane@example.com"},
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", Email: "john@example.com"},
	}

	suite.mockService.On("ListSorted", 0, 20, "created_at", "desc").Return(expectedCustomers, int64(2), nil)

	req := httptest.NewRequest(http.MethodGet, "/customers?sort_by=created_at&sort_order=desc", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.Equal(suite.T(), int64(2), response.Meta.Total)
}

func (suite *CustomerHandlerTestSuite) TestList_SortByInvalidColumn() {
	suite.router.GET("/customers", suite.handler.List)

	expectedCustomers := []models.Customer{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", Email: "john@example.com"},
	}

	// Invalid sort_by should fall through to unsorted List
	suite.mockService.On("List", 0, 20).Return(expectedCustomers, int64(1), nil)

	req := httptest.NewRequest(http.MethodGet, "/customers?sort_by=invalid_column&sort_order=desc", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

func (suite *CustomerHandlerTestSuite) TestList_SearchByEmail() {
	suite.router.GET("/customers", suite.handler.List)

	expectedCustomers := []models.Customer{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", Email: "john@acme.com"},
	}

	suite.mockService.On("Search", "acme.com", 0, 20, "", "asc").Return(expectedCustomers, int64(1), nil)

	req := httptest.NewRequest(http.MethodGet, "/customers?search=acme.com", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.Equal(suite.T(), int64(1), response.Meta.Total)
}

func (suite *CustomerHandlerTestSuite) TestList_SearchWithSort() {
	suite.router.GET("/customers", suite.handler.List)

	expectedCustomers := []models.Customer{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", Email: "john@acme.com"},
	}

	suite.mockService.On("Search", "acme", 0, 20, "created_at", "desc").Return(expectedCustomers, int64(1), nil)

	req := httptest.NewRequest(http.MethodGet, "/customers?search=acme&sort_by=created_at&sort_order=desc", nil)
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
}

// TestCreate_DuplicateEmailDoesNotLeakDriverInternals ensures that even when the
// service wraps a raw driver/unique-index error, the 409 body only carries the
// clean, client-facing message.
func (suite *CustomerHandlerTestSuite) TestCreate_DuplicateEmailDoesNotLeakDriverInternals() {
	suite.router.POST("/customers", suite.handler.Create)

	payload := CreateCustomerRequest{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}

	driverErr := fmt.Errorf("Error 1062 (23000): Duplicate entry 'john@example.com' for key 'idx_customers_email': %w", apperrors.ErrDuplicateEmail)
	suite.mockService.On("Create", mock.Anything).Return(driverErr)

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/customers", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusConflict, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "customer with this email already exists", response.Error.Message)
	assertNoDriverInternalsInBody(suite.T(), rec.Body.String())
}

// TestUpdate_DuplicateEmailDoesNotLeakDriverInternals is the update-path
// counterpart of the create-path leak check above.
func (suite *CustomerHandlerTestSuite) TestUpdate_DuplicateEmailDoesNotLeakDriverInternals() {
	suite.router.PUT("/customers/:id", suite.handler.Update)

	existingCustomer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}

	payload := UpdateCustomerRequest{
		Email: "existing@example.com",
	}

	driverErr := fmt.Errorf("UNIQUE constraint failed: customers.email (idx_customers_email): %w", apperrors.ErrDuplicateEmail)
	suite.mockService.On("GetByID", uint(1)).Return(existingCustomer, nil)
	suite.mockService.On("Update", mock.Anything).Return(driverErr)

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/customers/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	suite.router.ServeHTTP(rec, req)

	assert.Equal(suite.T(), http.StatusConflict, rec.Code)

	var response utils.APIResponse
	err := json.Unmarshal(rec.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.Equal(suite.T(), "customer with this email already exists", response.Error.Message)
	assertNoDriverInternalsInBody(suite.T(), rec.Body.String())
}

// assertNoDriverInternalsInBody fails if a response body leaks database driver
// text, constraint wording or index names to the client.
func assertNoDriverInternalsInBody(t *testing.T, body string) {
	t.Helper()
	lowered := strings.ToLower(body)
	for _, needle := range []string{
		"duplicate entry",
		"unique constraint",
		"constraint failed",
		"idx_customers_email",
		"sqlstate",
		"error 1062",
		"23000",
		"for key",
	} {
		assert.NotContains(t, lowered, needle,
			"response body leaked driver internals (%q): %s", needle, body)
	}
}

func TestCustomerHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(CustomerHandlerTestSuite))
}