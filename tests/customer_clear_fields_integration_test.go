package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression roots of #270: PUT /customers/{id} on SQLite, through the routes
// SetupCustomerRoutes registers, behind the real RequestID, Logger,
// ErrorHandler and Auth middleware. A sent "" clears an optional text field;
// an omitted key or a JSON null keeps it; an empty first_name, last_name or
// email still leaves the stored value alone.

// clearableCustomerFields are the nine optional text fields of #270, keyed by
// their JSON name.
var clearableCustomerFields = []string{
	"phone", "company", "position", "address", "city", "state", "country", "postal_code", "notes",
}

type clearFieldsFixture struct {
	suite  *CustomerIntegrationTestSuite
	router *gin.Engine
	seq    int
}

func newClearFieldsFixture(t *testing.T) *clearFieldsFixture {
	t.Helper()
	s := new(CustomerIntegrationTestSuite)
	s.SetT(t)
	s.SetupSuite()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.Logger())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(s.authService))
	handler.SetupCustomerRoutes(protected, handler.NewCustomerHandler(s.customerService))

	return &clearFieldsFixture{suite: s, router: router}
}

// seed stores a customer whose every text field is non-empty.
func (f *clearFieldsFixture) seed(t *testing.T) *models.Customer {
	t.Helper()
	f.seq++
	c := &models.Customer{
		FirstName:  fmt.Sprintf("Ana%d", f.seq),
		LastName:   fmt.Sprintf("Popescu%d", f.seq),
		Email:      fmt.Sprintf("ana.clear%d@example.com", f.seq),
		Website:    fmt.Sprintf("https://acme%d.example", f.seq),
		Phone:      fmt.Sprintf("+40 721 000 %03d", f.seq),
		Company:    fmt.Sprintf("Acme %d SRL", f.seq),
		Position:   fmt.Sprintf("Buyer %d", f.seq),
		Address:    fmt.Sprintf("Strada Exemplu %d", f.seq),
		City:       fmt.Sprintf("Cluj %d", f.seq),
		State:      fmt.Sprintf("Judet %d", f.seq),
		Country:    fmt.Sprintf("Romania %d", f.seq),
		PostalCode: fmt.Sprintf("400%03d", f.seq),
		Notes:      fmt.Sprintf("Met at fair %d.", f.seq),
	}
	require.NoError(t, f.suite.db.Create(c).Error)
	return c
}

func (f *clearFieldsFixture) put(t *testing.T, id uint, body, token string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/customers/%d", id), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	data, _ := response.Data.(map[string]interface{})
	return rec, data
}

func (f *clearFieldsFixture) reload(t *testing.T, id uint) *models.Customer {
	t.Helper()
	var stored models.Customer
	require.NoError(t, f.suite.db.First(&stored, id).Error)
	return &stored
}

// customerTextValues maps the JSON names of the text fields to their values.
func customerTextValues(c *models.Customer) map[string]string {
	return map[string]string{
		"first_name":  c.FirstName,
		"last_name":   c.LastName,
		"email":       c.Email,
		"website":     c.Website,
		"phone":       c.Phone,
		"company":     c.Company,
		"position":    c.Position,
		"address":     c.Address,
		"city":        c.City,
		"state":       c.State,
		"country":     c.Country,
		"postal_code": c.PostalCode,
		"notes":       c.Notes,
	}
}

func TestCustomerUpdateClearsOptionalTextFields(t *testing.T) {
	f := newClearFieldsFixture(t)

	for _, field := range clearableCustomerFields {
		t.Run(field, func(t *testing.T) {
			seeded := f.seed(t)
			before := customerTextValues(seeded)

			rec, data := f.put(t, seeded.ID, fmt.Sprintf(`{%q:""}`, field), f.suite.adminToken)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.NotNil(t, data)
			assert.Equal(t, "", data[field], "the response shows the cleared field")

			after := customerTextValues(f.reload(t, seeded.ID))
			for name, want := range before {
				if name == field {
					assert.Equal(t, "", after[name], "%s is cleared in the database", name)
				} else {
					assert.Equal(t, want, after[name], "%s is unchanged in the database", name)
				}
			}
		})
	}

	t.Run("all nine at once", func(t *testing.T) {
		seeded := f.seed(t)
		before := customerTextValues(seeded)

		parts := make([]string, 0, len(clearableCustomerFields))
		for _, field := range clearableCustomerFields {
			parts = append(parts, fmt.Sprintf(`%q:""`, field))
		}
		rec, data := f.put(t, seeded.ID, "{"+strings.Join(parts, ",")+"}", f.suite.adminToken)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		after := customerTextValues(f.reload(t, seeded.ID))
		for _, field := range clearableCustomerFields {
			assert.Equal(t, "", data[field], "response %s", field)
			assert.Equal(t, "", after[field], "database %s", field)
		}
		for _, name := range []string{"first_name", "last_name", "email", "website"} {
			assert.Equal(t, before[name], after[name], "%s is unchanged", name)
		}
	})
}

func TestCustomerUpdateOmittedOrNullFieldsUnchanged(t *testing.T) {
	f := newClearFieldsFixture(t)

	nulls := make([]string, 0, len(clearableCustomerFields))
	for _, field := range clearableCustomerFields {
		nulls = append(nulls, fmt.Sprintf(`%q:null`, field))
	}

	for _, tc := range []struct {
		name      string
		body      string
		firstName string
	}{
		{name: "empty body", body: `{}`},
		{name: "first name only", body: `{"first_name":"Ioana"}`, firstName: "Ioana"},
		{name: "all null", body: "{" + strings.Join(nulls, ",") + "}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seeded := f.seed(t)
			want := customerTextValues(seeded)
			if tc.firstName != "" {
				want["first_name"] = tc.firstName
			}

			rec, _ := f.put(t, seeded.ID, tc.body, f.suite.adminToken)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			assert.Equal(t, want, customerTextValues(f.reload(t, seeded.ID)))
		})
	}
}

func TestCustomerUpdateEmptyRequiredFieldsUnchanged(t *testing.T) {
	f := newClearFieldsFixture(t)
	seeded := f.seed(t)
	want := customerTextValues(seeded)

	rec, data := f.put(t, seeded.ID, `{"first_name":"","last_name":"","email":""}`, f.suite.adminToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, seeded.FirstName, data["first_name"])
	assert.Equal(t, seeded.LastName, data["last_name"])
	assert.Equal(t, seeded.Email, data["email"])

	assert.Equal(t, want, customerTextValues(f.reload(t, seeded.ID)))
}

func TestCustomerUpdateClearRoles(t *testing.T) {
	f := newClearFieldsFixture(t)

	t.Run("sales may clear notes", func(t *testing.T) {
		seeded := f.seed(t)
		rec, data := f.put(t, seeded.ID, `{"notes":""}`, f.suite.salesToken)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "", data["notes"])
		assert.Equal(t, "", f.reload(t, seeded.ID).Notes)
	})

	t.Run("support is refused", func(t *testing.T) {
		seeded := f.seed(t)
		rec, _ := f.put(t, seeded.ID, `{"notes":""}`, f.suite.supportToken)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Equal(t, seeded.Notes, f.reload(t, seeded.ID).Notes)
	})
}
