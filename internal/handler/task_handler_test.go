package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type MockTaskService struct {
	mock.Mock
}

func (m *MockTaskService) Create(task *models.Task) error {
	args := m.Called(task)
	return args.Error(0)
}

func (m *MockTaskService) GetByID(id uint) (*models.Task, error) {
	args := m.Called(id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.Task), args.Error(1)
}

func (m *MockTaskService) GetByAssignee(assignedToID uint, offset, limit int) ([]models.Task, int64, error) {
	args := m.Called(assignedToID, offset, limit)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).([]models.Task), args.Get(1).(int64), args.Error(2)
}

func (m *MockTaskService) Update(task *models.Task) error {
	args := m.Called(task)
	return args.Error(0)
}

func (m *MockTaskService) Delete(id uint) error {
	args := m.Called(id)
	return args.Error(0)
}

func (m *MockTaskService) List(offset, limit int) ([]models.Task, int64, error) {
	args := m.Called(offset, limit)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).([]models.Task), args.Get(1).(int64), args.Error(2)
}

func (m *MockTaskService) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error) {
	args := m.Called(offset, limit, sortBy, sortOrder)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).([]models.Task), args.Get(1).(int64), args.Error(2)
}

func (m *MockTaskService) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error) {
	args := m.Called(query, offset, limit, sortBy, sortOrder)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).([]models.Task), args.Get(1).(int64), args.Error(2)
}

func (m *MockTaskService) GetPendingCount() (int64, error) {
	args := m.Called()
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockTaskService) GetStatusCounts() (map[string]int64, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]int64), args.Error(1)
}

func (m *MockTaskService) GetUpcoming(limit int) ([]models.Task, error) {
	args := m.Called(limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]models.Task), args.Error(1)
}

func (m *MockTaskService) GetUpcomingByAssignee(assigneeID uint, limit int) ([]models.Task, error) {
	args := m.Called(assigneeID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]models.Task), args.Error(1)
}

func (m *MockTaskService) GetDueWithin(from, to time.Time, limit int) ([]models.Task, error) {
	args := m.Called(from, to, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]models.Task), args.Error(1)
}

func (m *MockTaskService) GetDueWithinByAssignee(assigneeID uint, from, to time.Time, limit int) ([]models.Task, error) {
	args := m.Called(assigneeID, from, to, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]models.Task), args.Error(1)
}

func (m *MockTaskService) GetRecentlyCompleted(limit int) ([]models.Task, error) {
	args := m.Called(limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]models.Task), args.Error(1)
}

func (m *MockTaskService) CreateWithLabels(task *models.Task, labelIDs []uint) error {
	args := m.Called(task, labelIDs)
	return args.Error(0)
}

func (m *MockTaskService) UpdateWithLabels(task *models.Task, labelIDs *[]uint) error {
	args := m.Called(task, labelIDs)
	return args.Error(0)
}

func (m *MockTaskService) ListByLabel(labelID uint, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error) {
	args := m.Called(labelID, offset, limit, sortBy, sortOrder)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).([]models.Task), args.Get(1).(int64), args.Error(2)
}

func (m *MockTaskService) ListByLabelForAssignee(assigneeID, labelID uint, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error) {
	args := m.Called(assigneeID, labelID, offset, limit, sortBy, sortOrder)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).([]models.Task), args.Get(1).(int64), args.Error(2)
}

// Compile-time proof that the local double still satisfies the service
// interface it stands in for.
var _ service.TaskService = (*MockTaskService)(nil)

type TaskHandlerTestSuite struct {
	suite.Suite
	handler     *TaskHandler
	mockService *MockTaskService
	router      *gin.Engine
}

func (suite *TaskHandlerTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)
	gin.SetMode(gin.TestMode)
}

func (suite *TaskHandlerTestSuite) SetupTest() {
	suite.mockService = new(MockTaskService)
	suite.handler = NewTaskHandler(suite.mockService)
	suite.router = gin.New()
	
	// Add middleware to set user context
	suite.router.Use(func(c *gin.Context) {
		// Default test user
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleAdmin))
		c.Next()
	})
	
	// Add error handler middleware to handle validation errors
	suite.router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 {
			err := c.Errors[0]
			if err.Type == gin.ErrorTypeBind {
				utils.RespondValidationError(c, err.Error())
				return
			}
		}
	})
	
	SetupTaskRoutes(suite.router.Group(""), suite.handler)
}

func (suite *TaskHandlerTestSuite) TearDownTest() {
	suite.mockService.AssertExpectations(suite.T())
}

func (suite *TaskHandlerTestSuite) TestCreateTask_Success() {
	task := &models.Task{
		Title:        "Test Task",
		Description:  "Test Description",
		AssignedToID: 1,
		Status:       models.TaskStatusPending,
		Priority:     models.TaskPriorityMedium,
	}

	// No label_ids in the body, so the handler passes a nil id list through.
	suite.mockService.On("CreateWithLabels", mock.MatchedBy(func(t *models.Task) bool {
		return t.Title == task.Title && t.AssignedToID == task.AssignedToID
	}), []uint(nil)).Return(nil)

	body, _ := json.Marshal(task)
	req, _ := http.NewRequest("POST", "/tasks", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusCreated, w.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Data)
}

func (suite *TaskHandlerTestSuite) TestCreateTask_NonAdminAssignToOther_Forbidden() {
	// Set user as non-admin
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleSales))
		c.Next()
	})
	SetupTaskRoutes(suite.router.Group(""), suite.handler)

	task := &models.Task{
		Title:        "Test Task",
		AssignedToID: 2, // Different user
		Status:       models.TaskStatusPending,
	}

	body, _ := json.Marshal(task)
	req, _ := http.NewRequest("POST", "/tasks", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), response.Success)
	assert.NotNil(suite.T(), response.Error)
}

func (suite *TaskHandlerTestSuite) TestCreateTask_ValidationError() {
	task := &models.Task{
		// Missing required fields
		AssignedToID: 1,
	}

	body, _ := json.Marshal(task)
	req, _ := http.NewRequest("POST", "/tasks", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

func (suite *TaskHandlerTestSuite) TestCreateTask_ServiceError() {
	task := &models.Task{
		Title:        "Test Task",
		AssignedToID: 1,
		Status:       models.TaskStatusPending,
	}

	suite.mockService.On("CreateWithLabels", mock.AnythingOfType("*models.Task"), []uint(nil)).Return(errors.New("service error"))

	body, _ := json.Marshal(task)
	req, _ := http.NewRequest("POST", "/tasks", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
}

func (suite *TaskHandlerTestSuite) TestGetTask_Success() {
	taskID := uint(1)
	expectedTask := &models.Task{
		Title:        "Test Task",
		AssignedToID: 1,
		Status:       models.TaskStatusPending,
	}
	expectedTask.ID = taskID

	suite.mockService.On("GetByID", taskID).Return(expectedTask, nil)

	req, _ := http.NewRequest("GET", "/tasks/1", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *TaskHandlerTestSuite) TestGetTask_NonAdminAccessOthersTask_Forbidden() {
	// Set user as non-admin with different ID
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(2))
		c.Set("user_role", string(models.RoleSales))
		c.Next()
	})
	SetupTaskRoutes(suite.router.Group(""), suite.handler)

	taskID := uint(1)
	expectedTask := &models.Task{
		Title:        "Test Task",
		AssignedToID: 1, // Different from current user
		Status:       models.TaskStatusPending,
	}
	expectedTask.ID = taskID

	suite.mockService.On("GetByID", taskID).Return(expectedTask, nil)

	req, _ := http.NewRequest("GET", "/tasks/1", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
}

func (suite *TaskHandlerTestSuite) TestGetTask_NotFound() {
	taskID := uint(999)
	suite.mockService.On("GetByID", taskID).Return(nil, fmt.Errorf("task 999 not found: %w", apperrors.ErrNotFound))

	req, _ := http.NewRequest("GET", "/tasks/999", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

// A retrieval failure that is not a missing task must surface as 500, not be
// disguised as a 404.
func (suite *TaskHandlerTestSuite) TestGetTask_ServiceError_InternalError() {
	taskID := uint(1)
	suite.mockService.On("GetByID", taskID).Return(nil, errors.New("connection refused"))

	req, _ := http.NewRequest("GET", "/tasks/1", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
}

func (suite *TaskHandlerTestSuite) TestGetTasksByAssignee_Success() {
	assigneeID := uint(1)
	task1 := models.Task{Title: "Task 1", AssignedToID: assigneeID}
	task1.ID = 1
	task2 := models.Task{Title: "Task 2", AssignedToID: assigneeID}
	task2.ID = 2
	expectedTasks := []models.Task{task1, task2}
	totalCount := int64(2)

	suite.mockService.On("GetByAssignee", uint(1), 0, 20).Return(expectedTasks, totalCount, nil)

	req, _ := http.NewRequest("GET", "/tasks/my", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.Equal(suite.T(), totalCount, response.Meta.Total)
	assert.Equal(suite.T(), int64(1), response.Meta.TotalPages)
}

func (suite *TaskHandlerTestSuite) TestGetMyTasks_NonAdminCanAccess() {
	// Set user as non-admin
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleSales))
		c.Next()
	})
	SetupTaskRoutes(suite.router.Group(""), suite.handler)

	expectedTasks := []models.Task{}
	totalCount := int64(0)
	suite.mockService.On("GetByAssignee", uint(1), 0, 20).Return(expectedTasks, totalCount, nil)

	req, _ := http.NewRequest("GET", "/tasks/my", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *TaskHandlerTestSuite) TestUpdateTask_LookupFailureIsInternalError() {
	taskID := uint(1)
	suite.mockService.On("GetByID", taskID).Return(nil, errors.New("connection refused"))

	updateData := map[string]interface{}{"title": "Updated Task"}
	body, _ := json.Marshal(updateData)
	req, _ := http.NewRequest("PUT", "/tasks/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "not found")
	suite.mockService.AssertNotCalled(suite.T(), "Update", mock.Anything)
}

func (suite *TaskHandlerTestSuite) TestUpdateTask_Success() {
	taskID := uint(1)
	existingTask := &models.Task{
		Title:        "Original Task",
		AssignedToID: 1,
		Status:       models.TaskStatusPending,
	}
	existingTask.ID = taskID

	suite.mockService.On("GetByID", taskID).Return(existingTask, nil)
	suite.mockService.On("UpdateWithLabels", mock.AnythingOfType("*models.Task"), (*[]uint)(nil)).Return(nil)

	updateData := map[string]interface{}{
		"title":  "Updated Task",
		"status": "in_progress",
	}
	body, _ := json.Marshal(updateData)
	req, _ := http.NewRequest("PUT", "/tasks/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *TaskHandlerTestSuite) TestUpdateTask_NonAdminReassign_Forbidden() {
	// Set user as non-admin
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleSales))
		c.Next()
	})
	SetupTaskRoutes(suite.router.Group(""), suite.handler)

	taskID := uint(1)
	existingTask := &models.Task{
		Title:        "Original Task",
		AssignedToID: 1,
		Status:       models.TaskStatusPending,
	}
	existingTask.ID = taskID

	suite.mockService.On("GetByID", taskID).Return(existingTask, nil)

	updateData := map[string]interface{}{
		"assigned_to_id": 2, // Trying to reassign to different user
	}
	body, _ := json.Marshal(updateData)
	req, _ := http.NewRequest("PUT", "/tasks/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
}

// Regression test: a non-admin updating their own task while echoing back the
// current assigned_to_id must not be treated as a reassignment attempt.
func (suite *TaskHandlerTestSuite) TestUpdateTask_NonAdminSameAssignee_Success() {
	// Set user as non-admin
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleSales))
		c.Next()
	})
	SetupTaskRoutes(suite.router.Group(""), suite.handler)

	taskID := uint(1)
	existingTask := &models.Task{
		Title:        "Original Task",
		AssignedToID: 1,
		Status:       models.TaskStatusPending,
	}
	existingTask.ID = taskID

	suite.mockService.On("GetByID", taskID).Return(existingTask, nil)
	suite.mockService.On("UpdateWithLabels", mock.MatchedBy(func(t *models.Task) bool {
		return t.ID == taskID && t.Title == "Updated Task" && t.AssignedToID == uint(1)
	}), (*[]uint)(nil)).Return(nil)

	updateData := map[string]interface{}{
		"title":          "Updated Task",
		"assigned_to_id": 1, // Same as current assignee - not a reassignment
	}
	body, _ := json.Marshal(updateData)
	req, _ := http.NewRequest("PUT", "/tasks/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *TaskHandlerTestSuite) TestUpdateTask_NonAdminDifferentAssignee_Forbidden() {
	// Set user as non-admin
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleSales))
		c.Next()
	})
	SetupTaskRoutes(suite.router.Group(""), suite.handler)

	taskID := uint(1)
	existingTask := &models.Task{
		Title:        "Original Task",
		AssignedToID: 1,
		Status:       models.TaskStatusPending,
	}
	existingTask.ID = taskID

	suite.mockService.On("GetByID", taskID).Return(existingTask, nil)

	updateData := map[string]interface{}{
		"title":          "Updated Task",
		"assigned_to_id": 3, // Actual reassignment attempt
	}
	body, _ := json.Marshal(updateData)
	req, _ := http.NewRequest("PUT", "/tasks/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
	suite.mockService.AssertNotCalled(suite.T(), "Update", mock.Anything)
}

func (suite *TaskHandlerTestSuite) TestUpdateTask_AdminReassign_Success() {
	taskID := uint(1)
	existingTask := &models.Task{
		Title:        "Original Task",
		AssignedToID: 1,
		Status:       models.TaskStatusPending,
	}
	existingTask.ID = taskID

	suite.mockService.On("GetByID", taskID).Return(existingTask, nil)
	suite.mockService.On("UpdateWithLabels", mock.MatchedBy(func(t *models.Task) bool {
		return t.ID == taskID && t.AssignedToID == uint(2)
	}), (*[]uint)(nil)).Return(nil)

	updateData := map[string]interface{}{
		"assigned_to_id": 2, // Admin reassigns to a different user
	}
	body, _ := json.Marshal(updateData)
	req, _ := http.NewRequest("PUT", "/tasks/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
}

func (suite *TaskHandlerTestSuite) TestUpdateTask_NonAdminAccessOthersTask_Forbidden() {
	// Set user as non-admin with different ID
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(2))
		c.Set("user_role", string(models.RoleSales))
		c.Next()
	})
	SetupTaskRoutes(suite.router.Group(""), suite.handler)

	taskID := uint(1)
	existingTask := &models.Task{
		Title:        "Original Task",
		AssignedToID: 1, // Different from current user
		Status:       models.TaskStatusPending,
	}
	existingTask.ID = taskID

	suite.mockService.On("GetByID", taskID).Return(existingTask, nil)

	updateData := map[string]interface{}{
		"title": "Updated Task",
	}
	body, _ := json.Marshal(updateData)
	req, _ := http.NewRequest("PUT", "/tasks/1", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
}

func (suite *TaskHandlerTestSuite) TestDeleteTask_Success() {
	taskID := uint(1)

	suite.mockService.On("Delete", taskID).Return(nil)

	req, _ := http.NewRequest("DELETE", "/tasks/1", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusNoContent, w.Code)
}

func (suite *TaskHandlerTestSuite) TestDeleteTask_NotFound() {
	taskID := uint(999)

	suite.mockService.On("Delete", taskID).Return(fmt.Errorf("task 999 not found: %w", apperrors.ErrNotFound))

	req, _ := http.NewRequest("DELETE", "/tasks/999", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

// A delete that fails for any reason other than a missing task must surface as
// 500; reporting it as 404 hides real database failures.
func (suite *TaskHandlerTestSuite) TestDeleteTask_ServiceError_InternalError() {
	taskID := uint(1)

	suite.mockService.On("Delete", taskID).Return(errors.New("deadlock detected"))

	req, _ := http.NewRequest("DELETE", "/tasks/1", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
}

func (suite *TaskHandlerTestSuite) TestDeleteTask_NonAdminForbidden() {
	// Set user as non-admin
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleSales))
		c.Next()
	})
	SetupTaskRoutes(suite.router.Group(""), suite.handler)

	req, _ := http.NewRequest("DELETE", "/tasks/1", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
}

func (suite *TaskHandlerTestSuite) TestListTasks_Success() {
	task1 := models.Task{Title: "Task 1", AssignedToID: 1}
	task1.ID = 1
	task2 := models.Task{Title: "Task 2", AssignedToID: 2}
	task2.ID = 2
	expectedTasks := []models.Task{task1, task2}
	totalCount := int64(2)

	suite.mockService.On("List", 0, 20).Return(expectedTasks, totalCount, nil)

	req, _ := http.NewRequest("GET", "/tasks", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	
	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.Equal(suite.T(), totalCount, response.Meta.Total)
}

func (suite *TaskHandlerTestSuite) TestListTasks_NonAdminGetsOwnTasks() {
	// Set user as non-admin
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleSales))
		c.Next()
	})
	SetupTaskRoutes(suite.router.Group(""), suite.handler)

	expectedTasks := []models.Task{}
	totalCount := int64(0)
	suite.mockService.On("GetByAssignee", uint(1), 0, 20).Return(expectedTasks, totalCount, nil)

	req, _ := http.NewRequest("GET", "/tasks", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *TaskHandlerTestSuite) TestInvalidTaskID() {
	req, _ := http.NewRequest("GET", "/tasks/invalid", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

func (suite *TaskHandlerTestSuite) TestMyTasks_ParsePaginationSuccess() {
	expectedTasks := []models.Task{}
	totalCount := int64(0)
	suite.mockService.On("GetByAssignee", uint(1), 0, 10).Return(expectedTasks, totalCount, nil)

	req, _ := http.NewRequest("GET", "/tasks/my?limit=10", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

// Regression: the frontend sends `limit`, and it used to be ignored because the
// handler only read `per_page`.
func (suite *TaskHandlerTestSuite) TestListTasks_LimitIsHonoured() {
	suite.mockService.On("List", 0, 50).Return([]models.Task{}, int64(0), nil)

	req, _ := http.NewRequest("GET", "/tasks?limit=50", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *TaskHandlerTestSuite) TestListTasks_PageConvertsToOffset() {
	suite.mockService.On("List", 20, 20).Return([]models.Task{}, int64(0), nil)

	req, _ := http.NewRequest("GET", "/tasks?page=2&limit=20", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), 2, response.Meta.Page)
	assert.Equal(suite.T(), 20, response.Meta.PerPage)
}

// An oversized limit is clamped to the 100 cap, not discarded back to 20.
func (suite *TaskHandlerTestSuite) TestListTasks_LimitCappedAtHundred() {
	suite.mockService.On("List", 0, 100).Return([]models.Task{}, int64(0), nil)

	req, _ := http.NewRequest("GET", "/tasks?limit=500", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *TaskHandlerTestSuite) TestListTasks_OffsetIsHonoured() {
	suite.mockService.On("List", 40, 20).Return([]models.Task{}, int64(0), nil)

	req, _ := http.NewRequest("GET", "/tasks?offset=40", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *TaskHandlerTestSuite) TestListMyTasks_LimitIsHonoured() {
	suite.mockService.On("GetByAssignee", uint(1), 0, 50).Return([]models.Task{}, int64(0), nil)

	req, _ := http.NewRequest("GET", "/tasks/my?limit=50", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *TaskHandlerTestSuite) TestListMyTasks_PageConvertsToOffset() {
	suite.mockService.On("GetByAssignee", uint(1), 20, 20).Return([]models.Task{}, int64(0), nil)

	req, _ := http.NewRequest("GET", "/tasks/my?page=2&limit=20", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), 2, response.Meta.Page)
	assert.Equal(suite.T(), 20, response.Meta.PerPage)
}

func (suite *TaskHandlerTestSuite) TestListMyTasks_LimitCappedAtHundred() {
	suite.mockService.On("GetByAssignee", uint(1), 0, 100).Return([]models.Task{}, int64(0), nil)

	req, _ := http.NewRequest("GET", "/tasks/my?limit=500", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

// The non-admin branch of List is narrowed to the caller's own tasks; it must
// paginate identically.
func (suite *TaskHandlerTestSuite) TestListTasks_NonAdminLimitIsHonoured() {
	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleSales))
		c.Next()
	})
	SetupTaskRoutes(suite.router.Group(""), suite.handler)

	suite.mockService.On("GetByAssignee", uint(1), 0, 50).Return([]models.Task{}, int64(0), nil)

	req, _ := http.NewRequest("GET", "/tasks?limit=50", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *TaskHandlerTestSuite) TestListTasks_SortByCreatedAtDesc() {
	task1 := models.Task{Title: "Task 1", AssignedToID: 1}
	task1.ID = 2
	task2 := models.Task{Title: "Task 2", AssignedToID: 2}
	task2.ID = 1
	expectedTasks := []models.Task{task1, task2}

	suite.mockService.On("ListSorted", 0, 20, "created_at", "desc").Return(expectedTasks, int64(2), nil)

	req, _ := http.NewRequest("GET", "/tasks?sort_by=created_at&sort_order=desc", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.Equal(suite.T(), int64(2), response.Meta.Total)
}

func (suite *TaskHandlerTestSuite) TestListTasks_SortByInvalidColumn() {
	task1 := models.Task{Title: "Task 1", AssignedToID: 1}
	task1.ID = 1
	expectedTasks := []models.Task{task1}

	// Invalid sort_by should fall through to unsorted List
	suite.mockService.On("List", 0, 20).Return(expectedTasks, int64(1), nil)

	req, _ := http.NewRequest("GET", "/tasks?sort_by=invalid_column&sort_order=desc", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *TaskHandlerTestSuite) TestListTasks_SearchByTitle() {
	task1 := models.Task{Title: "Follow up with client", AssignedToID: 1}
	task1.ID = 1
	expectedTasks := []models.Task{task1}

	suite.mockService.On("Search", "follow up", 0, 20, "", "asc").Return(expectedTasks, int64(1), nil)

	req, _ := http.NewRequest("GET", "/tasks?search=follow+up", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response utils.APIResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), response.Success)
	assert.Equal(suite.T(), int64(1), response.Meta.Total)
}

func (suite *TaskHandlerTestSuite) TestListTasks_SearchWithSort() {
	task1 := models.Task{Title: "Follow up with client", AssignedToID: 1}
	task1.ID = 1
	expectedTasks := []models.Task{task1}

	suite.mockService.On("Search", "follow", 0, 20, "due_date", "asc").Return(expectedTasks, int64(1), nil)

	req, _ := http.NewRequest("GET", "/tasks?search=follow&sort_by=due_date&sort_order=asc", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func TestTaskHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(TaskHandlerTestSuite))
}

// --- GET /tasks/upcoming -----------------------------------------------------

// newUpcomingRouter registers only the upcoming route, on its own engine, so
// the test never collides with whatever SetupTaskRoutes registers in routes.go.
func (suite *TaskHandlerTestSuite) newUpcomingRouter(role models.UserRole, userID uint) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("request_id", "test-request-id")
		c.Set("user_id", userID)
		c.Set("user_role", string(role))
		c.Next()
	})
	router.GET("/tasks/upcoming", suite.handler.GetUpcoming)
	return router
}

func (suite *TaskHandlerTestSuite) getUpcoming(role models.UserRole, userID uint, query string) *httptest.ResponseRecorder {
	router := suite.newUpcomingRouter(role, userID)
	req := httptest.NewRequest(http.MethodGet, "/tasks/upcoming"+query, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// The window must be exactly seven days wide by default, and the payload a bare
// JSON array — the frontend reads response.data as the array itself.
func (suite *TaskHandlerTestSuite) TestGetUpcoming_DefaultsToSevenDaysForAdmin() {
	task := models.Task{Title: "Renew contract", AssignedToID: 4}
	task.ID = 3

	var capturedFrom, capturedTo time.Time
	suite.mockService.On("GetDueWithin", mock.AnythingOfType("time.Time"), mock.AnythingOfType("time.Time"), 100).
		Run(func(args mock.Arguments) {
			capturedFrom = args.Get(0).(time.Time)
			capturedTo = args.Get(1).(time.Time)
		}).
		Return([]models.Task{task}, nil)

	rec := suite.getUpcoming(models.RoleAdmin, 1, "")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	assert.Equal(suite.T(), capturedFrom.AddDate(0, 0, 7), capturedTo)

	var env struct {
		Success bool          `json:"success"`
		Data    []models.Task `json:"data"`
	}
	assert.NoError(suite.T(), json.Unmarshal(rec.Body.Bytes(), &env))
	assert.True(suite.T(), env.Success)
	assert.Len(suite.T(), env.Data, 1)
	assert.Equal(suite.T(), "Renew contract", env.Data[0].Title)
}

func (suite *TaskHandlerTestSuite) TestGetUpcoming_HonoursDaysParameter() {
	var capturedFrom, capturedTo time.Time
	suite.mockService.On("GetDueWithin", mock.AnythingOfType("time.Time"), mock.AnythingOfType("time.Time"), 100).
		Run(func(args mock.Arguments) {
			capturedFrom = args.Get(0).(time.Time)
			capturedTo = args.Get(1).(time.Time)
		}).
		Return([]models.Task{}, nil)

	rec := suite.getUpcoming(models.RoleAdmin, 1, "?days=30")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	assert.Equal(suite.T(), capturedFrom.AddDate(0, 0, 30), capturedTo)
}

// Non-admins only ever see their own assignments, exactly as TaskHandler.List
// narrows them.
func (suite *TaskHandlerTestSuite) TestGetUpcoming_NonAdminScopedToAssignee() {
	suite.mockService.On("GetDueWithinByAssignee", uint(12), mock.AnythingOfType("time.Time"), mock.AnythingOfType("time.Time"), 100).
		Return([]models.Task{}, nil)

	rec := suite.getUpcoming(models.RoleSupport, 12, "")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	assert.NoError(suite.T(), json.Unmarshal(rec.Body.Bytes(), &env))
	assert.True(suite.T(), env.Success)
	assert.Equal(suite.T(), "[]", string(env.Data), "an empty result must be a JSON array, never null")
}

func (suite *TaskHandlerTestSuite) TestGetUpcoming_DaysAboveRangeIsClampedToNinety() {
	var capturedFrom, capturedTo time.Time
	suite.mockService.On("GetDueWithin", mock.AnythingOfType("time.Time"), mock.AnythingOfType("time.Time"), 100).
		Run(func(args mock.Arguments) {
			capturedFrom = args.Get(0).(time.Time)
			capturedTo = args.Get(1).(time.Time)
		}).
		Return([]models.Task{}, nil)

	rec := suite.getUpcoming(models.RoleAdmin, 1, "?days=365")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	assert.Equal(suite.T(), capturedFrom.AddDate(0, 0, 90), capturedTo)
}

func (suite *TaskHandlerTestSuite) TestGetUpcoming_InvalidDaysFallsBackToDefault() {
	var capturedFrom, capturedTo time.Time
	suite.mockService.On("GetDueWithin", mock.AnythingOfType("time.Time"), mock.AnythingOfType("time.Time"), 100).
		Run(func(args mock.Arguments) {
			capturedFrom = args.Get(0).(time.Time)
			capturedTo = args.Get(1).(time.Time)
		}).
		Return([]models.Task{}, nil)

	rec := suite.getUpcoming(models.RoleAdmin, 1, "?days=not-a-number")

	assert.Equal(suite.T(), http.StatusOK, rec.Code)
	assert.Equal(suite.T(), capturedFrom.AddDate(0, 0, 7), capturedTo)
}

func (suite *TaskHandlerTestSuite) TestGetUpcoming_ServiceErrorIsInternalError() {
	suite.mockService.On("GetDueWithin", mock.AnythingOfType("time.Time"), mock.AnythingOfType("time.Time"), 100).
		Return(nil, errors.New("database is down"))

	rec := suite.getUpcoming(models.RoleAdmin, 1, "")

	assert.Equal(suite.T(), http.StatusInternalServerError, rec.Code)
}