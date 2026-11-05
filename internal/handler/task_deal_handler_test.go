package handler

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestTaskDealHandlerErrorMapping: the two write routes answer 404 for the deal
// sentinel and 500 for anything else, and hand deal_id to the service as
// specified.
func TestTaskDealHandlerErrorMapping(t *testing.T) {
	stored := func() *models.Task {
		return &models.Task{BaseModel: models.BaseModel{ID: 5}, Title: "Stored", AssignedToID: 2}
	}
	refused := fmt.Errorf("unknown deal_id 9: %w", apperrors.ErrDealNotFound)
	outage := errors.New("connection refused")
	createBody := gin.H{"title": "New", "assigned_to_id": 2, "deal_id": 9}

	errorCases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"the deal sentinel", refused, http.StatusNotFound, "NOT_FOUND"},
		{"another failure", outage, http.StatusInternalServerError, "INTERNAL_ERROR"},
	}
	for _, tc := range errorCases {
		t.Run("POST /tasks: "+tc.name, func(t *testing.T) {
			svc := new(MockTaskService)
			svc.On("CreateWithLabels", mock.AnythingOfType("*models.Task"), []uint(nil), uint(2)).Return(tc.err)
			w := serveJSON(newTaskLeadRouter(svc, models.RoleSales, 2), http.MethodPost, "/tasks", createBody)
			assert.Equal(t, tc.status, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), `"code":"`+tc.code+`"`)
			assert.NotContains(t, w.Body.String(), "connection refused")
			svc.AssertExpectations(t)
		})

		t.Run("PUT /tasks/{id}: "+tc.name, func(t *testing.T) {
			svc := new(MockTaskService)
			svc.On("GetByID", uint(5)).Return(stored(), nil)
			svc.On("UpdateWithLabels", mock.AnythingOfType("*models.Task"), (*[]uint)(nil), false, true, uint(2)).Return(tc.err)
			w := serveJSON(newTaskLeadRouter(svc, models.RoleSales, 2), http.MethodPut, "/tasks/5", gin.H{"deal_id": 9})
			assert.Equal(t, tc.status, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), `"code":"`+tc.code+`"`)
			assert.NotContains(t, w.Body.String(), "connection refused")
			svc.AssertExpectations(t)
		})
	}

	t.Run("POST passes deal_id through: absent and 0 are no link", func(t *testing.T) {
		for name, body := range map[string]gin.H{
			"absent": {"title": "New", "assigned_to_id": 2},
			"zero":   {"title": "New", "assigned_to_id": 2, "deal_id": 0},
			"null":   {"title": "New", "assigned_to_id": 2, "deal_id": nil},
		} {
			svc := new(MockTaskService)
			svc.On("CreateWithLabels", mock.MatchedBy(func(task *models.Task) bool {
				return task.DealID == nil
			}), []uint(nil), uint(2)).Return(nil)
			w := serveJSON(newTaskLeadRouter(svc, models.RoleSales, 2), http.MethodPost, "/tasks", body)
			require.Equal(t, http.StatusCreated, w.Code, "%s: %s", name, w.Body.String())
			assert.NotContains(t, w.Body.String(), `"deal_id"`, name)
			svc.AssertExpectations(t)
		}
	})

	t.Run("POST passes an id through", func(t *testing.T) {
		svc := new(MockTaskService)
		svc.On("CreateWithLabels", mock.MatchedBy(func(task *models.Task) bool {
			return task.DealID != nil && *task.DealID == 9
		}), []uint(nil), uint(1)).Return(nil)
		w := serveJSON(newTaskLeadRouter(svc, models.RoleAdmin, 1), http.MethodPost, "/tasks", createBody)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		svc.AssertExpectations(t)
	})

	t.Run("PUT with deal_id absent keeps the stored link and flags it absent", func(t *testing.T) {
		for name, body := range map[string]gin.H{
			"absent": {"title": "Renamed"},
			"null":   {"title": "Renamed", "deal_id": nil},
		} {
			link := uint(30)
			task := stored()
			task.DealID = &link
			svc := new(MockTaskService)
			svc.On("GetByID", uint(5)).Return(task, nil)
			svc.On("UpdateWithLabels", mock.MatchedBy(func(got *models.Task) bool {
				return got.DealID != nil && *got.DealID == 30
			}), (*[]uint)(nil), false, false, uint(2)).Return(nil)
			w := serveJSON(newTaskLeadRouter(svc, models.RoleSales, 2), http.MethodPut, "/tasks/5", body)
			require.Equal(t, http.StatusOK, w.Code, "%s: %s", name, w.Body.String())
			svc.AssertExpectations(t)
		}
	})

	t.Run("PUT with deal_id 0 is a clear", func(t *testing.T) {
		link := uint(30)
		task := stored()
		task.DealID = &link
		svc := new(MockTaskService)
		svc.On("GetByID", uint(5)).Return(task, nil)
		svc.On("UpdateWithLabels", mock.MatchedBy(func(got *models.Task) bool {
			return got.DealID == nil
		}), (*[]uint)(nil), false, true, uint(2)).Return(nil)
		w := serveJSON(newTaskLeadRouter(svc, models.RoleSales, 2), http.MethodPut, "/tasks/5", gin.H{"deal_id": 0})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		svc.AssertExpectations(t)
	})

	t.Run("PUT with an id passes it through, flagged as sent", func(t *testing.T) {
		svc := new(MockTaskService)
		svc.On("GetByID", uint(5)).Return(stored(), nil)
		svc.On("UpdateWithLabels", mock.MatchedBy(func(got *models.Task) bool {
			return got.DealID != nil && *got.DealID == 9
		}), (*[]uint)(nil), false, true, uint(2)).Return(nil)
		w := serveJSON(newTaskLeadRouter(svc, models.RoleSales, 2), http.MethodPut, "/tasks/5", gin.H{"deal_id": 9})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		svc.AssertExpectations(t)
	})
}
