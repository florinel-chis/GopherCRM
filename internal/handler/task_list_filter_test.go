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
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// #427: GET /tasks takes optional status and priority filters. These cases run
// the real handler over a strict mock service, so every service call, and
// every argument it receives, is pinned; an unexpected call fails the test.

type taskListFilterResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Tasks []models.Task `json:"tasks"`
		Total int64         `json:"total"`
	} `json:"data"`
	Meta  utils.APIMeta `json:"meta"`
	Error struct {
		Code    string                 `json:"code"`
		Details map[string]interface{} `json:"details"`
	} `json:"error"`
}

func newTaskListFilterRouter(t *testing.T, svc *MockTaskService, role models.UserRole, userID uint) *gin.Engine {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)
	h := NewTaskHandler(svc)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("request_id", "task-list-filter")
		c.Set("user_id", userID)
		c.Set("user_role", string(role))
		c.Next()
	})
	router.GET("/tasks", h.List)
	return router
}

func getTaskListFilter(t *testing.T, router *gin.Engine, path string) (*httptest.ResponseRecorder, taskListFilterResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var body taskListFilterResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	return w, body
}

func newStrictTaskService(t *testing.T) *MockTaskService {
	svc := new(MockTaskService)
	svc.Test(t)
	t.Cleanup(func() { svc.AssertExpectations(t) })
	return svc
}

func TestTaskListFilterHandler(t *testing.T) {
	rows := []models.Task{{BaseModel: models.BaseModel{ID: 5}, Title: "Call", Status: models.TaskStatusCompleted, AssignedToID: 9}}
	const callerID = uint(9)

	t.Run("admin status filter goes through ListFiltered with no assignee", func(t *testing.T) {
		svc := newStrictTaskService(t)
		svc.On("ListFiltered", repository.TaskListFilter{Status: "completed"}, 0, 20, "", "asc").
			Return(rows, int64(41), nil).Once()

		router := newTaskListFilterRouter(t, svc, models.RoleAdmin, 1)
		w, body := getTaskListFilter(t, router, "/tasks?status=completed")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, int64(41), body.Data.Total)
		assert.Len(t, body.Data.Tasks, 1)
		assert.Equal(t, int64(41), body.Meta.Total)
		assert.Equal(t, int64(3), body.Meta.TotalPages)
		assert.Equal(t, 1, body.Meta.Page)
		assert.Equal(t, 20, body.Meta.PerPage)
	})

	t.Run("admin priority combines with search and the sort", func(t *testing.T) {
		svc := newStrictTaskService(t)
		svc.On("ListFiltered", repository.TaskListFilter{Priority: "high", Search: "tok"}, 0, 20, "title", "desc").
			Return(rows, int64(1), nil).Once()

		router := newTaskListFilterRouter(t, svc, models.RoleAdmin, 1)
		w, _ := getTaskListFilter(t, router, "/tasks?priority=high&search=tok&sort_by=title&sort_order=desc")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})

	t.Run("admin sort_order is passed normalised", func(t *testing.T) {
		svc := newStrictTaskService(t)
		svc.On("ListFiltered", repository.TaskListFilter{Priority: "low"}, 0, 20, "due_date", "asc").
			Return(rows, int64(1), nil).Once()

		router := newTaskListFilterRouter(t, svc, models.RoleAdmin, 1)
		w, _ := getTaskListFilter(t, router, "/tasks?priority=low&sort_by=due_date&sort_order=sideways")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})

	t.Run("admin label_id still wins over search", func(t *testing.T) {
		svc := newStrictTaskService(t)
		svc.On("ListFiltered", repository.TaskListFilter{Status: "pending", LabelID: 3}, 0, 20, "", "asc").
			Return(rows, int64(1), nil).Once()

		router := newTaskListFilterRouter(t, svc, models.RoleAdmin, 1)
		w, _ := getTaskListFilter(t, router, "/tasks?status=pending&label_id=3&search=tok")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})

	for _, role := range []models.UserRole{models.RoleSales, models.RoleSupport, models.RoleCustomer} {
		role := role
		t.Run(string(role)+" is narrowed to its own tasks without search or sort", func(t *testing.T) {
			svc := newStrictTaskService(t)
			assignee := callerID
			svc.On("ListFiltered", repository.TaskListFilter{AssigneeID: &assignee, Status: "completed"}, 0, 20, "", "").
				Return(rows, int64(1), nil).Once()

			router := newTaskListFilterRouter(t, svc, role, callerID)
			w, body := getTaskListFilter(t, router, "/tasks?status=completed&search=tok&sort_by=title")
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, int64(1), body.Data.Total)
		})
	}

	t.Run("non-admin with label_id keeps the sort", func(t *testing.T) {
		svc := newStrictTaskService(t)
		assignee := callerID
		svc.On("ListFiltered", repository.TaskListFilter{AssigneeID: &assignee, LabelID: 3, Priority: "low"}, 0, 20, "title", "asc").
			Return(rows, int64(1), nil).Once()

		router := newTaskListFilterRouter(t, svc, models.RoleSales, callerID)
		w, _ := getTaskListFilter(t, router, "/tasks?priority=low&label_id=3&sort_by=title")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})

	t.Run("empty status and priority take the existing branch", func(t *testing.T) {
		svc := newStrictTaskService(t)
		svc.On("List", 0, 20).Return(rows, int64(1), nil).Once()

		router := newTaskListFilterRouter(t, svc, models.RoleAdmin, 1)
		w, _ := getTaskListFilter(t, router, "/tasks?status=&priority=")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		svc.AssertNotCalled(t, "ListFiltered")
	})

	t.Run("empty filters keep the non-admin branch", func(t *testing.T) {
		svc := newStrictTaskService(t)
		svc.On("GetByAssignee", callerID, 0, 20).Return(rows, int64(1), nil).Once()

		router := newTaskListFilterRouter(t, svc, models.RoleSupport, callerID)
		w, _ := getTaskListFilter(t, router, "/tasks?status=&priority=")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})

	t.Run("a repeated parameter uses its first value", func(t *testing.T) {
		svc := newStrictTaskService(t)
		svc.On("ListFiltered", repository.TaskListFilter{Status: "completed"}, 0, 20, "", "asc").
			Return(rows, int64(1), nil).Once()

		router := newTaskListFilterRouter(t, svc, models.RoleAdmin, 1)
		w, _ := getTaskListFilter(t, router, "/tasks?status=completed&status=zzbogus_status")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})

	invalid := []struct {
		name     string
		query    string
		keys     []string
		absent   string
		withRole models.UserRole
	}{
		{"unknown status with a valid priority", "status=zzbogus_status&priority=high", []string{"status"}, "zzbogus_status", models.RoleAdmin},
		{"status in another case", "status=Completed", []string{"status"}, "Completed", models.RoleAdmin},
		{"ticket priority urgent", "priority=urgent", []string{"priority"}, "urgent", models.RoleAdmin},
		{"both invalid", "status=zzbogus_status&priority=zzbogus_priority", []string{"status", "priority"}, "zzbogus_", models.RoleAdmin},
		{"non-admin unknown priority", "priority=zzbogus_priority&label_id=3", []string{"priority"}, "zzbogus_priority", models.RoleSales},
	}
	for _, tc := range invalid {
		tc := tc
		t.Run("400 "+tc.name, func(t *testing.T) {
			svc := newStrictTaskService(t)
			router := newTaskListFilterRouter(t, svc, tc.withRole, callerID)
			w, body := getTaskListFilter(t, router, "/tasks?"+tc.query+"&search=tok&sort_by=title")
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.False(t, body.Success)
			assert.Equal(t, "VALIDATION_ERROR", body.Error.Code)
			keys := make([]string, 0, len(body.Error.Details))
			for k := range body.Error.Details {
				keys = append(keys, k)
			}
			assert.ElementsMatch(t, tc.keys, keys)
			assert.False(t, strings.Contains(w.Body.String(), tc.absent), "the raw value is not echoed: %s", w.Body.String())
			assert.Empty(t, svc.Calls, "no service method is called")
		})
	}
}
