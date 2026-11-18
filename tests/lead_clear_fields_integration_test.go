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

// Regression roots of #398: PUT /leads/{id} on SQLite, through the routes
// SetupLeadRoutes registers, behind the real RequestID, Logger, ErrorHandler
// and Auth middleware. A sent "" clears an optional text field; an omitted
// key or a JSON null keeps it; an empty first_name, last_name or email still
// leaves the stored value alone. Every value is read back from the database.

// clearableLeadFields are the six optional text fields of #398, keyed by
// their JSON name.
var clearableLeadFields = []string{"phone", "company", "position", "source", "external_id", "notes"}

type leadClearFieldsFixture struct {
	suite  *LeadIntegrationTestSuite
	router *gin.Engine
	seq    int
}

func newLeadClearFieldsFixture(t *testing.T) *leadClearFieldsFixture {
	t.Helper()
	s := new(LeadIntegrationTestSuite)
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
	handler.SetupLeadRoutes(protected, handler.NewLeadHandler(s.leadService))

	return &leadClearFieldsFixture{suite: s, router: router}
}

// seed stores a lead whose every text field is non-empty.
func (f *leadClearFieldsFixture) seed(t *testing.T) *models.Lead {
	t.Helper()
	f.seq++
	lead := &models.Lead{
		FirstName:  fmt.Sprintf("Ada%d", f.seq),
		LastName:   fmt.Sprintf("King%d", f.seq),
		Email:      fmt.Sprintf("ada.clear%d@example.com", f.seq),
		Phone:      fmt.Sprintf("+44 20 7946 %04d", f.seq),
		Company:    fmt.Sprintf("Engines %d Ltd", f.seq),
		Position:   fmt.Sprintf("CEO %d", f.seq),
		Source:     fmt.Sprintf("referral-%d", f.seq),
		ExternalID: fmt.Sprintf("crm-legacy-%d", f.seq),
		Notes:      fmt.Sprintf("Demo booked, week %d.", f.seq),
		Status:     models.LeadStatusNew,
		OwnerID:    f.suite.adminUser.ID,
	}
	require.NoError(t, f.suite.db.Create(lead).Error)
	return lead
}

func (f *leadClearFieldsFixture) put(t *testing.T, id uint, body string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/leads/%d", id), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.suite.adminToken)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	data, _ := response.Data.(map[string]interface{})
	return rec, data
}

func (f *leadClearFieldsFixture) reload(t *testing.T, id uint) *models.Lead {
	t.Helper()
	var stored models.Lead
	require.NoError(t, f.suite.db.First(&stored, id).Error)
	return &stored
}

// leadTextValues maps the JSON names of the text fields to their values.
func leadTextValues(l *models.Lead) map[string]string {
	return map[string]string{
		"first_name":  l.FirstName,
		"last_name":   l.LastName,
		"email":       l.Email,
		"phone":       l.Phone,
		"company":     l.Company,
		"position":    l.Position,
		"source":      l.Source,
		"external_id": l.ExternalID,
		"notes":       l.Notes,
	}
}

func TestLeadUpdateClearsOptionalTextFields(t *testing.T) {
	f := newLeadClearFieldsFixture(t)

	for _, field := range clearableLeadFields {
		t.Run(field, func(t *testing.T) {
			seeded := f.seed(t)
			before := leadTextValues(seeded)

			rec, data := f.put(t, seeded.ID, fmt.Sprintf(`{%q:""}`, field))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.NotNil(t, data)

			after := leadTextValues(f.reload(t, seeded.ID))
			for name, want := range before {
				if name == field {
					assert.Equal(t, "", after[name], "%s is cleared in the database", name)
				} else {
					assert.Equal(t, want, after[name], "%s is unchanged in the database", name)
				}
			}
		})
	}

	t.Run("all six at once", func(t *testing.T) {
		seeded := f.seed(t)
		before := leadTextValues(seeded)

		parts := make([]string, 0, len(clearableLeadFields))
		for _, field := range clearableLeadFields {
			parts = append(parts, fmt.Sprintf(`%q:""`, field))
		}
		rec, _ := f.put(t, seeded.ID, "{"+strings.Join(parts, ",")+"}")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		after := leadTextValues(f.reload(t, seeded.ID))
		for _, field := range clearableLeadFields {
			assert.Equal(t, "", after[field], "database %s", field)
		}
		for _, name := range []string{"first_name", "last_name", "email"} {
			assert.Equal(t, before[name], after[name], "%s is unchanged", name)
		}
	})

	// The email-or-phone rule of create is not re-checked on update, so the
	// only contact of a lead can be cleared. Observed behaviour, pinned so a
	// change to it is deliberate.
	t.Run("phone of a lead without an email", func(t *testing.T) {
		f.seq++
		lead := &models.Lead{
			FirstName: fmt.Sprintf("Phone%d", f.seq),
			LastName:  "Only",
			Phone:     "+44 20 7946 9999",
			Status:    models.LeadStatusNew,
			OwnerID:   f.suite.adminUser.ID,
		}
		require.NoError(t, f.suite.db.Create(lead).Error)
		require.Empty(t, f.reload(t, lead.ID).Email)

		rec, _ := f.put(t, lead.ID, `{"phone":""}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		stored := f.reload(t, lead.ID)
		assert.Empty(t, stored.Phone)
		assert.Empty(t, stored.Email)
		assert.Equal(t, lead.FirstName, stored.FirstName)
	})
}

func TestLeadUpdateOmittedOrNullFieldsUnchanged(t *testing.T) {
	f := newLeadClearFieldsFixture(t)

	nulls := make([]string, 0, len(clearableLeadFields))
	for _, field := range clearableLeadFields {
		nulls = append(nulls, fmt.Sprintf(`%q:null`, field))
	}

	for _, tc := range []struct {
		name   string
		body   string
		status models.LeadStatus
	}{
		{name: "status only", body: `{"status":"contacted"}`, status: models.LeadStatusContacted},
		{name: "all null", body: "{" + strings.Join(nulls, ",") + "}", status: models.LeadStatusNew},
		// The nulls ride along with a real change, which proves the request
		// was applied and not dropped.
		{name: "all null with a status", body: `{"status":"qualified",` + strings.Join(nulls, ",") + "}", status: models.LeadStatusQualified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seeded := f.seed(t)
			want := leadTextValues(seeded)

			rec, _ := f.put(t, seeded.ID, tc.body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			stored := f.reload(t, seeded.ID)
			assert.Equal(t, want, leadTextValues(stored))
			assert.Equal(t, tc.status, stored.Status)
		})
	}
}

func TestLeadUpdateEmptyRequiredFieldsUnchanged(t *testing.T) {
	f := newLeadClearFieldsFixture(t)
	seeded := f.seed(t)
	want := leadTextValues(seeded)

	rec, data := f.put(t, seeded.ID, `{"first_name":"","last_name":"","email":"","status":"contacted"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, seeded.FirstName, data["first_name"])
	assert.Equal(t, seeded.LastName, data["last_name"])
	assert.Equal(t, seeded.Email, data["email"])

	stored := f.reload(t, seeded.ID)
	assert.Equal(t, want, leadTextValues(stored))
	assert.Equal(t, models.LeadStatusContacted, stored.Status, "the request was applied")
}
