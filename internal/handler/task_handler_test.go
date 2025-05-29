package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
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

	suite.mockService.On("Create", mock.MatchedBy(func(t *models.Task) bool {
		return t.Title == task.Title && t.AssignedToID == task.AssignedToID
	})).Return(nil)

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

	suite.mockService.On("Create", mock.AnythingOfType("*models.Task")).Return(errors.New("service error"))

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
	suite.mockService.On("GetByID", taskID).Return(nil, errors.New("task not found"))

	req, _ := http.NewRequest("GET", "/tasks/999", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
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

func (suite *TaskHandlerTestSuite) TestUpdateTask_Success() {
	taskID := uint(1)
	existingTask := &models.Task{
		Title:        "Original Task",
		AssignedToID: 1,
		Status:       models.TaskStatusPending,
	}
	existingTask.ID = taskID

	suite.mockService.On("GetByID", taskID).Return(existingTask, nil)
	suite.mockService.On("Update", mock.AnythingOfType("*models.Task")).Return(nil)

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

	req, _ := http.NewRequest("GET", "/tasks/my?per_page=10", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func TestTaskHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(TaskHandlerTestSuite))
}