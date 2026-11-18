package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Regression roots of #398: PUT /leads/{id} passes a sent optional text field
// (phone, company, position, source, external_id, notes) to LeadService.Update
// as a map key, the empty string included, and no key for an omitted or null
// field. first_name, last_name and email still apply only when non-empty.

// clearableLeadFields are the six optional text fields of #398, keyed by
// their JSON name, which is also their key in the update map.
var clearableLeadFields = []string{"phone", "company", "position", "source", "external_id", "notes"}

func clearFieldsStoredLead() *models.Lead {
	return &models.Lead{
		BaseModel:  models.BaseModel{ID: 7},
		FirstName:  "Ada",
		LastName:   "King",
		Email:      "ada.king@example.com",
		Phone:      "+44 20 7946 0001",
		Company:    "Analytical Engines Ltd",
		Position:   "CEO",
		Source:     "referral",
		ExternalID: "crm-legacy-1042",
		Notes:      "Demo booked for Tuesday.",
		Status:     models.LeadStatusNew,
		OwnerID:    1,
	}
}

// runLeadClearFieldsUpdate sends body to the real Update handler with a
// mocked service and returns the update map the handler passed to
// LeadService.Update. The mock expects exactly one Update call, so a handler
// that never reached the service fails here and not with an empty map.
func runLeadClearFieldsUpdate(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previous })
	gin.SetMode(gin.TestMode)

	svc := new(mocks.LeadService)
	svc.On("GetByID", uint(7)).Return(clearFieldsStoredLead(), nil).Once()
	var passed map[string]interface{}
	svc.On("Update", uint(7), mock.AnythingOfType("map[string]interface {}")).Run(func(args mock.Arguments) {
		passed = args.Get(1).(map[string]interface{})
	}).Return(clearFieldsStoredLead(), nil).Once()

	h := NewLeadHandler(svc)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleAdmin))
		c.Next()
	})
	router.PUT("/leads/:id", h.Update)

	req := httptest.NewRequest(http.MethodPut, "/leads/7", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	svc.AssertExpectations(t)
	svc.AssertNumberOfCalls(t, "Update", 1)
	require.NotNil(t, passed, "LeadService.Update received no map")
	return passed
}

func TestLeadHandlerUpdateClearsOptionalFields(t *testing.T) {
	passed := runLeadClearFieldsUpdate(t, `{"phone":"","company":"","position":"","source":"","external_id":"","notes":""}`)

	for _, field := range clearableLeadFields {
		value, sent := passed[field]
		assert.True(t, sent, "%s is passed to the service", field)
		assert.Equal(t, "", value, "%s is passed as the empty string", field)
	}
	assert.Len(t, passed, len(clearableLeadFields), "nothing else is passed: %v", passed)
}

func TestLeadHandlerUpdateKeepsOmittedOrNull(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want map[string]interface{}
	}{
		{name: "empty body", body: `{}`, want: map[string]interface{}{}},
		{name: "first name only", body: `{"first_name":"Grace"}`, want: map[string]interface{}{"first_name": "Grace"}},
		{
			name: "all null",
			body: `{"phone":null,"company":null,"position":null,"source":null,"external_id":null,"notes":null}`,
			want: map[string]interface{}{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passed := runLeadClearFieldsUpdate(t, tc.body)

			for _, field := range clearableLeadFields {
				assert.NotContains(t, passed, field)
			}
			assert.Equal(t, tc.want, passed)
		})
	}
}

func TestLeadHandlerUpdateIgnoresEmptyRequiredFields(t *testing.T) {
	passed := runLeadClearFieldsUpdate(t, `{"first_name":"","last_name":"","email":"","phone":""}`)

	assert.NotContains(t, passed, "first_name")
	assert.NotContains(t, passed, "last_name")
	assert.NotContains(t, passed, "email")
	assert.Equal(t, map[string]interface{}{"phone": ""}, passed)
}
