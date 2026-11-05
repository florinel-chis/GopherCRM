package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Regression roots of #270: PUT /customers/{id} lets a sent empty string clear
// the nine optional text fields, while an omitted key or a JSON null keeps the
// value read by GetByID.

func clearFieldsStoredCustomer() *models.Customer {
	return &models.Customer{
		BaseModel:  models.BaseModel{ID: 7},
		FirstName:  "Ana",
		LastName:   "Popescu",
		Email:      "ana@example.com",
		Website:    "https://acme.example",
		Phone:      "+40 721 000 000",
		Company:    "Acme SRL",
		Position:   "Buyer",
		Address:    "Strada Exemplu 1",
		City:       "Cluj-Napoca",
		State:      "Cluj",
		Country:    "Romania",
		PostalCode: "400000",
		Notes:      "Met at the fair.",
	}
}

// runClearFieldsUpdate sends body to the real Update handler with a mocked
// service and returns the customer the handler passed to CustomerService.Update.
func runClearFieldsUpdate(t *testing.T, body string) *models.Customer {
	t.Helper()
	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previous })
	gin.SetMode(gin.TestMode)

	svc := new(mocks.CustomerService)
	svc.On("GetByID", uint(7)).Return(clearFieldsStoredCustomer(), nil).Once()
	var passed *models.Customer
	svc.On("Update", mock.AnythingOfType("*models.Customer")).Run(func(args mock.Arguments) {
		passed = args.Get(0).(*models.Customer)
	}).Return(nil).Once()

	h := NewCustomerHandler(svc)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleAdmin))
		c.Next()
	})
	router.PUT("/customers/:id", h.Update)

	req := httptest.NewRequest(http.MethodPut, "/customers/7", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	svc.AssertExpectations(t)
	require.NotNil(t, passed)
	return passed
}

func TestCustomerHandlerUpdateClearsOptionalFields(t *testing.T) {
	passed := runClearFieldsUpdate(t, `{"phone":"","company":"","position":"","address":"","city":"","state":"","country":"","postal_code":"","notes":""}`)

	assert.Empty(t, passed.Phone)
	assert.Empty(t, passed.Company)
	assert.Empty(t, passed.Position)
	assert.Empty(t, passed.Address)
	assert.Empty(t, passed.City)
	assert.Empty(t, passed.State)
	assert.Empty(t, passed.Country)
	assert.Empty(t, passed.PostalCode)
	assert.Empty(t, passed.Notes)

	// Fields that were not sent keep their stored values.
	stored := clearFieldsStoredCustomer()
	assert.Equal(t, stored.FirstName, passed.FirstName)
	assert.Equal(t, stored.LastName, passed.LastName)
	assert.Equal(t, stored.Email, passed.Email)
	assert.Equal(t, stored.Website, passed.Website)
}

func TestCustomerHandlerUpdateKeepsOmittedOrNull(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty body", body: `{}`},
		{name: "first name only", body: `{"first_name":"Ioana"}`},
		{name: "all null", body: `{"phone":null,"company":null,"position":null,"address":null,"city":null,"state":null,"country":null,"postal_code":null,"notes":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passed := runClearFieldsUpdate(t, tc.body)
			stored := clearFieldsStoredCustomer()

			assert.Equal(t, stored.Phone, passed.Phone)
			assert.Equal(t, stored.Company, passed.Company)
			assert.Equal(t, stored.Position, passed.Position)
			assert.Equal(t, stored.Address, passed.Address)
			assert.Equal(t, stored.City, passed.City)
			assert.Equal(t, stored.State, passed.State)
			assert.Equal(t, stored.Country, passed.Country)
			assert.Equal(t, stored.PostalCode, passed.PostalCode)
			assert.Equal(t, stored.Notes, passed.Notes)
			assert.Equal(t, stored.LastName, passed.LastName)
			assert.Equal(t, stored.Email, passed.Email)
			assert.Equal(t, stored.Website, passed.Website)
			if tc.name == "first name only" {
				assert.Equal(t, "Ioana", passed.FirstName)
			} else {
				assert.Equal(t, stored.FirstName, passed.FirstName)
			}
		})
	}
}
