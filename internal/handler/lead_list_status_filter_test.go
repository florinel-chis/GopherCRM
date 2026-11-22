package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// #426: GET /leads takes an optional status filter. These cases run the real
// handler over a mock service, so every service call, and every argument it
// receives, is pinned.

type leadStatusFilterResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Leads []models.Lead `json:"leads"`
		Total int64         `json:"total"`
	} `json:"data"`
	Meta  utils.APIMeta `json:"meta"`
	Error struct {
		Code    string                 `json:"code"`
		Details map[string]interface{} `json:"details"`
	} `json:"error"`
}

func newLeadStatusFilterRouter(t *testing.T, svc *mocks.LeadService, role models.UserRole, userID uint) *gin.Engine {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)
	h := NewLeadHandler(svc)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("request_id", "lead-status-filter")
		c.Set("user_id", userID)
		c.Set("user_role", string(role))
		c.Next()
	})
	router.GET("/leads", h.List)
	return router
}

func getLeadStatusFilter(t *testing.T, router *gin.Engine, path string) (*httptest.ResponseRecorder, leadStatusFilterResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var body leadStatusFilterResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	return w, body
}

func newStrictLeadService(t *testing.T) *mocks.LeadService {
	svc := new(mocks.LeadService)
	svc.Test(t)
	return svc
}

func TestLeadListStatusFilterHandler(t *testing.T) {
	rows := []models.Lead{{BaseModel: models.BaseModel{ID: 5}, FirstName: "Ada", Status: models.LeadStatusQualified}}

	t.Run("admin combines status with search, classification and the sort", func(t *testing.T) {
		svc := newStrictLeadService(t)
		svc.On("ListFiltered", repository.LeadListFilter{
			Search:         "tok",
			Classification: models.LeadClassificationLead,
			Status:         models.LeadStatusQualified,
		}, 0, 20, "email", "desc").Return(rows, int64(41), nil).Once()

		router := newLeadStatusFilterRouter(t, svc, models.RoleAdmin, 1)
		w, body := getLeadStatusFilter(t, router, "/leads?status=qualified&search=tok&classification=lead&sort_by=email&sort_order=desc")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, int64(41), body.Data.Total)
		assert.Len(t, body.Data.Leads, 1)
		assert.Equal(t, int64(41), body.Meta.Total)
		assert.Equal(t, int64(3), body.Meta.TotalPages)
		assert.Equal(t, 1, body.Meta.Page)
		assert.Equal(t, 20, body.Meta.PerPage)
		svc.AssertExpectations(t)
	})

	t.Run("admin status alone", func(t *testing.T) {
		svc := newStrictLeadService(t)
		svc.On("ListFiltered", repository.LeadListFilter{Status: models.LeadStatusQualified}, 20, 10, "", "asc").
			Return(rows, int64(11), nil).Once()

		router := newLeadStatusFilterRouter(t, svc, models.RoleAdmin, 1)
		w, body := getLeadStatusFilter(t, router, "/leads?status=qualified&page=3&limit=10")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, int64(11), body.Data.Total)
		assert.Equal(t, 3, body.Meta.Page)
		svc.AssertExpectations(t)
	})

	t.Run("sales narrows only its own leads by status", func(t *testing.T) {
		svc := newStrictLeadService(t)
		owner := uint(7)
		svc.On("ListFiltered", repository.LeadListFilter{OwnerID: &owner, Status: models.LeadStatusQualified}, 0, 20, "", "").
			Return(rows, int64(1), nil).Once()

		router := newLeadStatusFilterRouter(t, svc, models.RoleSales, 7)
		w, body := getLeadStatusFilter(t, router, "/leads?status=qualified&search=tok&classification=lead&sort_by=email")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, int64(1), body.Data.Total)
		svc.AssertExpectations(t)
	})

	for _, value := range []string{"lost", "Qualified", "QUALIFIED"} {
		t.Run("invalid status "+value+" is 400 without a service call", func(t *testing.T) {
			for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales} {
				svc := newStrictLeadService(t)
				router := newLeadStatusFilterRouter(t, svc, role, 3)
				w, body := getLeadStatusFilter(t, router, "/leads?status="+value)
				require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
				assert.False(t, body.Success)
				assert.Equal(t, utils.ErrCodeValidation, body.Error.Code)
				assert.Contains(t, body.Error.Details, "status")
				assert.NotContains(t, w.Body.String(), value)
				svc.AssertExpectations(t)
			}
		})
	}

	t.Run("an empty status is no status filter", func(t *testing.T) {
		svc := newStrictLeadService(t)
		svc.On("ListFiltered", repository.LeadListFilter{}, 0, 20, "", "asc").Return(rows, int64(1), nil).Once()
		router := newLeadStatusFilterRouter(t, svc, models.RoleAdmin, 1)
		w, _ := getLeadStatusFilter(t, router, "/leads?status=")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		svc.AssertExpectations(t)
	})

	// #618: without a status the parameters a caller may use still combine in
	// the one list call.
	combined := []struct {
		name    string
		role    models.UserRole
		userID  uint
		url     string
		filter  repository.LeadListFilter
		sortBy  string
		sortOrd string
	}{
		{"admin search, classification and sort combine", models.RoleAdmin, 1,
			"/leads?search=tok&classification=spam&sort_by=email&sort_order=desc",
			repository.LeadListFilter{Search: "tok", Classification: models.LeadClassificationSpam}, "email", "desc"},
		{"admin classification keeps the sort", models.RoleAdmin, 1,
			"/leads?classification=spam&sort_by=email",
			repository.LeadListFilter{Classification: models.LeadClassificationSpam}, "email", "asc"},
		{"admin without parameters lists every lead", models.RoleAdmin, 1,
			"/leads", repository.LeadListFilter{}, "", "asc"},
		{"sales ignores search, classification and sort", models.RoleSales, 3,
			"/leads?search=tok&classification=spam&sort_by=email",
			repository.LeadListFilter{OwnerID: leadListOwner(3)}, "", ""},
	}
	for _, tc := range combined {
		t.Run(tc.name, func(t *testing.T) {
			svc := newStrictLeadService(t)
			svc.On("ListFiltered", tc.filter, 0, 20, tc.sortBy, tc.sortOrd).Return(rows, int64(1), nil).Once()
			router := newLeadStatusFilterRouter(t, svc, tc.role, tc.userID)
			w, body := getLeadStatusFilter(t, router, tc.url)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, int64(1), body.Data.Total)
			svc.AssertExpectations(t)
		})
	}

	t.Run("external_id overrides status, which is then not validated", func(t *testing.T) {
		svc := newStrictLeadService(t)
		svc.On("ListByExternalID", "X").Return(rows, nil).Once()
		router := newLeadStatusFilterRouter(t, svc, models.RoleAdmin, 1)
		w, _ := getLeadStatusFilter(t, router, "/leads?external_id=X&status=lost")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		svc.AssertExpectations(t)
	})

	t.Run("a repeated status uses its first value", func(t *testing.T) {
		svc := newStrictLeadService(t)
		svc.On("ListFiltered", repository.LeadListFilter{Status: models.LeadStatusQualified}, 0, 20, "", "asc").
			Return(rows, int64(1), nil).Once()
		router := newLeadStatusFilterRouter(t, svc, models.RoleAdmin, 1)
		w, _ := getLeadStatusFilter(t, router, "/leads?status=qualified&status=lost")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.False(t, strings.Contains(w.Body.String(), "VALIDATION_ERROR"))
		svc.AssertExpectations(t)
	})
}
