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

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// Compile-time proof that the shared double still satisfies the interface it
// stands in for; internal/mocks cannot import internal/service itself.
var _ service.CompanyService = (*mocks.CompanyService)(nil)

type CompanyHandlerTestSuite struct {
	suite.Suite
	mockService *mocks.CompanyService
	handler     *CompanyHandler
	role        models.UserRole
	userID      uint
	router      *gin.Engine
}

func (suite *CompanyHandlerTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
	gin.SetMode(gin.TestMode)
}

func (suite *CompanyHandlerTestSuite) SetupTest() {
	suite.mockService = new(mocks.CompanyService)
	suite.handler = NewCompanyHandler(suite.mockService)
	suite.role = models.RoleAdmin
	suite.userID = 1

	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", suite.userID)
		c.Set("user_role", string(suite.role))
		c.Set("request_id", "test-request-id")
		c.Next()
	})
	// The same minimal bind-error handler the other handler suites install, so
	// a validation failure comes back as 400 rather than an empty 200.
	suite.router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 && c.Errors[0].Type == gin.ErrorTypeBind {
			utils.RespondValidationError(c, c.Errors[0].Error())
		}
	})
	// Mounted through the real route setup so the role guards are part of what
	// is tested, not only the handler bodies.
	SetupCompanyRoutes(suite.router.Group(""), suite.handler)
}

func (suite *CompanyHandlerTestSuite) TearDownTest() {
	suite.mockService.AssertExpectations(suite.T())
}

func (suite *CompanyHandlerTestSuite) do(method, path string, body interface{}) *httptest.ResponseRecorder {
	reader := bytes.NewBuffer(nil)
	if body != nil {
		encoded, err := json.Marshal(body)
		suite.Require().NoError(err)
		reader = bytes.NewBuffer(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func validCompanyBody() gin.H {
	return gin.H{"name": "Acme GmbH", "domain": "acme.example", "industry": "Software"}
}

// --- Role matrix ------------------------------------------------------------

// Every route against every role. The mock is primed only where the guard is
// expected to let the request through, so a guard that opened up would fail
// on the unexpected call as well as on the status.
func (suite *CompanyHandlerTestSuite) TestRoleMatrix() {
	company := &models.Company{BaseModel: models.BaseModel{ID: 5}, Name: "Acme"}
	routes := []struct {
		name    string
		method  string
		path    string
		body    interface{}
		allowed map[models.UserRole]bool
		prime   func()
	}{
		{"list", http.MethodGet, "/companies", nil,
			map[models.UserRole]bool{models.RoleAdmin: true, models.RoleSales: true, models.RoleSupport: true},
			func() {
				suite.mockService.On("List", 0, 20, "", "", "").Return([]models.Company{}, int64(0), nil).Once()
			}},
		{"get", http.MethodGet, "/companies/5", nil,
			map[models.UserRole]bool{models.RoleAdmin: true, models.RoleSales: true, models.RoleSupport: true},
			func() { suite.mockService.On("GetByID", uint(5)).Return(company, nil).Once() }},
		{"create", http.MethodPost, "/companies", validCompanyBody(),
			map[models.UserRole]bool{models.RoleAdmin: true, models.RoleSales: true},
			func() {
				suite.mockService.On("Create", mock.Anything).Return(nil).Once()
				suite.mockService.On("GetByID", uint(0)).Return(company, nil).Once()
			}},
		{"update", http.MethodPut, "/companies/5", validCompanyBody(),
			map[models.UserRole]bool{models.RoleAdmin: true, models.RoleSales: true},
			func() {
				suite.mockService.On("GetByID", uint(5)).Return(company, nil).Twice()
				suite.mockService.On("Update", mock.Anything).Return(nil).Once()
			}},
		{"delete", http.MethodDelete, "/companies/5", nil,
			map[models.UserRole]bool{models.RoleAdmin: true},
			func() { suite.mockService.On("Delete", uint(5)).Return(nil).Once() }},
		{"customers", http.MethodGet, "/companies/5/customers", nil,
			map[models.UserRole]bool{models.RoleAdmin: true, models.RoleSales: true, models.RoleSupport: true},
			func() {
				suite.mockService.On("ListCustomers", uint(5), 0, 20).Return([]models.Customer{}, int64(0), nil).Once()
			}},
		{"leads", http.MethodGet, "/companies/5/leads", nil,
			map[models.UserRole]bool{models.RoleAdmin: true, models.RoleSales: true},
			func() {
				suite.mockService.On("ListLeads", uint(5), mock.Anything, 0, 20).Return([]models.Lead{}, int64(0), nil).Once()
			}},
	}

	for _, route := range routes {
		for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer} {
			suite.SetupTest()
			suite.role = role
			if route.allowed[role] {
				route.prime()
			}

			w := suite.do(route.method, route.path, route.body)
			if route.allowed[role] {
				assert.NotEqualf(suite.T(), http.StatusForbidden, w.Code, "%s as %s must not be forbidden", route.name, role)
				assert.Lessf(suite.T(), w.Code, 400, "%s as %s: %s", route.name, role, w.Body.String())
			} else {
				assert.Equalf(suite.T(), http.StatusForbidden, w.Code, "%s as %s must be forbidden", route.name, role)
			}
			suite.mockService.AssertExpectations(suite.T())
		}
	}
}

// --- Create -----------------------------------------------------------------

func (suite *CompanyHandlerTestSuite) TestCreate_AdminMayLeaveTheCompanyUnowned() {
	suite.mockService.On("Create", mock.MatchedBy(func(c *models.Company) bool {
		return c.Name == "Acme GmbH" && c.Domain == "acme.example" && c.Industry == "Software" && c.OwnerID == nil
	})).Return(nil).Run(func(args mock.Arguments) {
		args.Get(0).(*models.Company).ID = 9
	})
	suite.mockService.On("GetByID", uint(9)).Return(&models.Company{BaseModel: models.BaseModel{ID: 9}, Name: "Acme GmbH"}, nil)

	w := suite.do(http.MethodPost, "/companies", validCompanyBody())
	assert.Equal(suite.T(), http.StatusCreated, w.Code)
	response := decodeResponse(suite.T(), w)
	assert.True(suite.T(), response.Success)
	assert.Contains(suite.T(), w.Body.String(), `"customer_count":0`)
}

func (suite *CompanyHandlerTestSuite) TestCreate_SalesGetsItselfAsOwnerWhenNoneIsSent() {
	suite.role = models.RoleSales
	suite.userID = 42
	suite.mockService.On("Create", mock.MatchedBy(func(c *models.Company) bool {
		return c.OwnerID != nil && *c.OwnerID == 42
	})).Return(nil)
	suite.mockService.On("GetByID", uint(0)).Return(&models.Company{}, nil)

	w := suite.do(http.MethodPost, "/companies", validCompanyBody())
	assert.Equal(suite.T(), http.StatusCreated, w.Code)
}

func (suite *CompanyHandlerTestSuite) TestCreate_SalesMayNotAssignSomebodyElse() {
	suite.role = models.RoleSales
	suite.userID = 42

	body := validCompanyBody()
	body["owner_id"] = 7
	w := suite.do(http.MethodPost, "/companies", body)
	assert.Equal(suite.T(), http.StatusForbidden, w.Code)

	// ...but may name itself explicitly.
	suite.mockService.On("Create", mock.MatchedBy(func(c *models.Company) bool {
		return c.OwnerID != nil && *c.OwnerID == 42
	})).Return(nil)
	suite.mockService.On("GetByID", uint(0)).Return(&models.Company{}, nil)
	body["owner_id"] = 42
	w = suite.do(http.MethodPost, "/companies", body)
	assert.Equal(suite.T(), http.StatusCreated, w.Code)
}

func (suite *CompanyHandlerTestSuite) TestCreate_AdminMayAssignAnybody() {
	suite.mockService.On("Create", mock.MatchedBy(func(c *models.Company) bool {
		return c.OwnerID != nil && *c.OwnerID == 7
	})).Return(nil)
	suite.mockService.On("GetByID", uint(0)).Return(&models.Company{}, nil)

	body := validCompanyBody()
	body["owner_id"] = 7
	w := suite.do(http.MethodPost, "/companies", body)
	assert.Equal(suite.T(), http.StatusCreated, w.Code)
}

func (suite *CompanyHandlerTestSuite) TestCreate_UnknownOwnerIsInvalidReference() {
	suite.mockService.On("Create", mock.Anything).
		Return(fmt.Errorf("unknown owner_id 7: %w", apperrors.ErrAssigneeNotFound))

	body := validCompanyBody()
	body["owner_id"] = 7
	w := suite.do(http.MethodPost, "/companies", body)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, decodeResponse(suite.T(), w).Error.Code)
}

func (suite *CompanyHandlerTestSuite) TestCreate_DuplicateDomainIs409() {
	suite.mockService.On("Create", mock.Anything).
		Return(fmt.Errorf("domain %q is already used by another company: %w", "acme.example", apperrors.ErrDuplicateCompanyDomain))

	w := suite.do(http.MethodPost, "/companies", validCompanyBody())
	assert.Equal(suite.T(), http.StatusConflict, w.Code)
	assert.Equal(suite.T(), utils.ErrCodeConflict, decodeResponse(suite.T(), w).Error.Code)
	assertNoDriverInternalsInBody(suite.T(), w.Body.String())
}

func (suite *CompanyHandlerTestSuite) TestCreate_ValidationSentinelIs400() {
	suite.mockService.On("Create", mock.Anything).
		Return(fmt.Errorf("website must be an http or https URL: %w", apperrors.ErrValidation))

	w := suite.do(http.MethodPost, "/companies", validCompanyBody())
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Contains(suite.T(), w.Body.String(), "website must be an http or https URL")
}

// Malformed bodies never reach the service: no expectation is primed.
func (suite *CompanyHandlerTestSuite) TestCreate_BindingRejectsBadBodies() {
	for name, body := range map[string]gin.H{
		"missing name":        {"domain": "acme.example"},
		"bad employee range":  {"name": "Acme", "employee_range": "lots"},
		"employee range case": {"name": "Acme", "employee_range": "1-10 "},
	} {
		w := suite.do(http.MethodPost, "/companies", body)
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "%s: %s", name, w.Body.String())
	}
}

// One value per column, each one character over its width, and notes one byte
// over. The limits come from the model constants that a test in package
// models holds to the declared column widths.
func (suite *CompanyHandlerTestSuite) TestCreate_ValuesLongerThanTheirColumnAre400() {
	over := func(n int) string { return strings.Repeat("x", n+1) }
	for field, value := range map[string]string{
		"name":        over(models.CompanyNameMaxLength),
		"domain":      over(models.CompanyDomainMaxLength),
		"website":     over(models.CompanyWebsiteMaxLength),
		"industry":    over(models.CompanyIndustryMaxLength),
		"phone":       over(models.CompanyPhoneMaxLength),
		"address":     over(models.CompanyAddressMaxLength),
		"city":        over(models.CompanyCityMaxLength),
		"state":       over(models.CompanyStateMaxLength),
		"country":     over(models.CompanyCountryMaxLength),
		"postal_code": over(models.CompanyPostalCodeMaxLength),
		"notes":       over(models.CompanyNotesMaxBytes),
	} {
		body := validCompanyBody()
		body[field] = value
		w := suite.do(http.MethodPost, "/companies", body)
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "%s over its limit must be 400", field)
		assert.Containsf(suite.T(), strings.ToLower(w.Body.String()), field, "the message names the field")
	}

	// Multibyte text is measured in characters, not bytes, for the varchar
	// columns: 200 two-byte characters fit varchar(200).
	suite.mockService.On("Create", mock.Anything).Return(nil)
	suite.mockService.On("GetByID", uint(0)).Return(&models.Company{}, nil)
	body := validCompanyBody()
	body["name"] = strings.Repeat("é", models.CompanyNameMaxLength)
	w := suite.do(http.MethodPost, "/companies", body)
	assert.Equal(suite.T(), http.StatusCreated, w.Code)
}

// --- List -------------------------------------------------------------------

func (suite *CompanyHandlerTestSuite) TestList_ReturnsTheArrayWithPaginationMeta() {
	suite.role = models.RoleSupport
	suite.mockService.On("List", 20, 10, "acme", "name", "asc").Return([]models.Company{
		{BaseModel: models.BaseModel{ID: 1}, Name: "Acme", CustomerCount: 3, LeadCount: 1},
	}, int64(31), nil)

	w := suite.do(http.MethodGet, "/companies?page=3&limit=10&search=acme&sort_by=name&sort_order=asc", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response struct {
		Success bool             `json:"success"`
		Data    []models.Company `json:"data"`
		Meta    utils.APIMeta    `json:"meta"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &response))
	assert.True(suite.T(), response.Success)
	suite.Require().Len(response.Data, 1)
	assert.Equal(suite.T(), int64(3), response.Data[0].CustomerCount)
	assert.Equal(suite.T(), 3, response.Meta.Page)
	assert.Equal(suite.T(), 10, response.Meta.PerPage)
	assert.Equal(suite.T(), int64(31), response.Meta.Total)
	assert.Equal(suite.T(), int64(4), response.Meta.TotalPages)
}

func (suite *CompanyHandlerTestSuite) TestList_UnknownSortColumnIs400() {
	w := suite.do(http.MethodGet, "/companies?sort_by=owner_id", nil)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)

	// A semicolon would make Go's query parser drop the whole query string, so
	// the injection attempt has to be one the parser passes through.
	w = suite.do(http.MethodGet, "/companies?sort_by=name+DESC%2C+(SELECT+1)", nil)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

func (suite *CompanyHandlerTestSuite) TestList_ServiceFailureIsAServerError() {
	suite.mockService.On("List", 0, 20, "", "", "").Return(nil, int64(0), errors.New("db down"))

	w := suite.do(http.MethodGet, "/companies", nil)
	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "db down")
}

// --- Get --------------------------------------------------------------------

func (suite *CompanyHandlerTestSuite) TestGet_NotFoundAndBadID() {
	suite.mockService.On("GetByID", uint(404)).Return(nil, fmt.Errorf("company 404 not found: %w", apperrors.ErrNotFound))

	w := suite.do(http.MethodGet, "/companies/404", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)

	w = suite.do(http.MethodGet, "/companies/abc", nil)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

// --- Update -----------------------------------------------------------------

func (suite *CompanyHandlerTestSuite) TestUpdate_ReplacesTheFieldsAndKeepsTheOwner() {
	owner := uint(7)
	existing := &models.Company{BaseModel: models.BaseModel{ID: 5}, Name: "Old", Domain: "old.example", Notes: "keep?", OwnerID: &owner}
	suite.mockService.On("GetByID", uint(5)).Return(existing, nil).Twice()
	suite.mockService.On("Update", mock.MatchedBy(func(c *models.Company) bool {
		// PUT is a full replacement: notes not sent means notes cleared. The
		// owner was not sent either, and it is the one field that survives.
		return c.ID == 5 && c.Name == "New" && c.Domain == "" && c.Notes == "" && c.OwnerID != nil && *c.OwnerID == 7
	})).Return(nil)

	w := suite.do(http.MethodPut, "/companies/5", gin.H{"name": "New"})
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *CompanyHandlerTestSuite) TestUpdate_OwnerRules() {
	owner := uint(7)
	fresh := func() *models.Company {
		o := owner
		return &models.Company{BaseModel: models.BaseModel{ID: 5}, Name: "Acme", OwnerID: &o}
	}

	// Sales may not reassign to somebody else, nor clear the owner.
	suite.role = models.RoleSales
	suite.userID = 42
	suite.mockService.On("GetByID", uint(5)).Return(fresh(), nil).Twice()
	w := suite.do(http.MethodPut, "/companies/5", gin.H{"name": "Acme", "owner_id": 8})
	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
	w = suite.do(http.MethodPut, "/companies/5", gin.H{"name": "Acme", "owner_id": 0})
	assert.Equal(suite.T(), http.StatusForbidden, w.Code)

	// Sales may take it over itself.
	suite.mockService.On("GetByID", uint(5)).Return(fresh(), nil).Twice()
	suite.mockService.On("Update", mock.MatchedBy(func(c *models.Company) bool {
		return c.OwnerID != nil && *c.OwnerID == 42
	})).Return(nil).Once()
	w = suite.do(http.MethodPut, "/companies/5", gin.H{"name": "Acme", "owner_id": 42})
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	// Admin clears with 0.
	suite.role = models.RoleAdmin
	suite.mockService.On("GetByID", uint(5)).Return(fresh(), nil).Twice()
	suite.mockService.On("Update", mock.MatchedBy(func(c *models.Company) bool {
		return c.OwnerID == nil
	})).Return(nil).Once()
	w = suite.do(http.MethodPut, "/companies/5", gin.H{"name": "Acme", "owner_id": 0})
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *CompanyHandlerTestSuite) TestUpdate_DuplicateDomainIs409() {
	suite.mockService.On("GetByID", uint(5)).Return(&models.Company{BaseModel: models.BaseModel{ID: 5}, Name: "Acme"}, nil)
	suite.mockService.On("Update", mock.Anything).
		Return(fmt.Errorf("domain %q is already used by another company: %w", "acme.example", apperrors.ErrDuplicateCompanyDomain))

	w := suite.do(http.MethodPut, "/companies/5", validCompanyBody())
	assert.Equal(suite.T(), http.StatusConflict, w.Code)
}

func (suite *CompanyHandlerTestSuite) TestUpdate_NotFoundAndTooLong() {
	suite.mockService.On("GetByID", uint(404)).Return(nil, fmt.Errorf("company 404 not found: %w", apperrors.ErrNotFound))
	w := suite.do(http.MethodPut, "/companies/404", validCompanyBody())
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)

	// Bounds are checked before the lookup, so nothing is primed for id 5.
	body := validCompanyBody()
	body["city"] = strings.Repeat("c", models.CompanyCityMaxLength+1)
	w = suite.do(http.MethodPut, "/companies/5", body)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

// --- Delete -----------------------------------------------------------------

func (suite *CompanyHandlerTestSuite) TestDelete_SuccessNotFoundAndFailure() {
	suite.mockService.On("Delete", uint(5)).Return(nil).Once()
	w := suite.do(http.MethodDelete, "/companies/5", nil)
	assert.Equal(suite.T(), http.StatusNoContent, w.Code)

	suite.mockService.On("Delete", uint(404)).Return(fmt.Errorf("company 404 not found: %w", apperrors.ErrNotFound)).Once()
	w = suite.do(http.MethodDelete, "/companies/404", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)

	// A failed unlink-and-delete must never look like a success or a miss.
	suite.mockService.On("Delete", uint(6)).Return(errors.New("tx failed")).Once()
	w = suite.do(http.MethodDelete, "/companies/6", nil)
	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "tx failed")
}

// --- Sub-lists --------------------------------------------------------------

func (suite *CompanyHandlerTestSuite) TestListLeads_SalesIsNarrowedToItsOwnLeadsAdminIsNot() {
	suite.role = models.RoleSales
	suite.userID = 42
	suite.mockService.On("ListLeads", uint(5), mock.MatchedBy(func(ownerID *uint) bool {
		return ownerID != nil && *ownerID == 42
	}), 0, 20).Return([]models.Lead{}, int64(0), nil).Once()
	w := suite.do(http.MethodGet, "/companies/5/leads", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	suite.role = models.RoleAdmin
	suite.mockService.On("ListLeads", uint(5), mock.MatchedBy(func(ownerID *uint) bool {
		return ownerID == nil
	}), 10, 10).Return([]models.Lead{{BaseModel: models.BaseModel{ID: 3}}}, int64(11), nil).Once()
	w = suite.do(http.MethodGet, "/companies/5/leads?page=2&limit=10", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"total":11`)
	assert.Contains(suite.T(), w.Body.String(), `"page":2`)
}

func (suite *CompanyHandlerTestSuite) TestListCustomers_UnknownCompanyIs404() {
	suite.mockService.On("ListCustomers", uint(404), 0, 20).
		Return(nil, int64(0), fmt.Errorf("company 404 not found: %w", apperrors.ErrNotFound))
	w := suite.do(http.MethodGet, "/companies/404/customers", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

func TestCompanyHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(CompanyHandlerTestSuite))
}
