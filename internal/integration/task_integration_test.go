package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type TaskIntegrationTestSuite struct {
	suite.Suite
	db          *gorm.DB
	router      *gin.Engine
	adminToken  string
	salesToken  string
	adminUser   *models.User
	salesUser   *models.User
	testLead    *models.Lead
	testCustomer *models.Customer
}

func (suite *TaskIntegrationTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)
	
	gin.SetMode(gin.TestMode)

	// Setup in-memory SQLite database
	var err error
	suite.db, err = gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	suite.Require().NoError(err)

	// Migrate database
	err = suite.db.AutoMigrate(
		&models.User{},
		&models.Lead{},
		&models.Customer{},
		&models.Task{},
		&models.APIKey{},
	)
	suite.Require().NoError(err)

	// Create test users
	suite.adminUser = &models.User{
		Email:     "admin@test.com",
		FirstName: "Admin",
		LastName:  "User",
		Role:      models.RoleAdmin,
		IsActive:  true,
	}
	suite.Require().NoError(suite.db.Create(suite.adminUser).Error)

	suite.salesUser = &models.User{
		Email:     "sales@test.com",
		FirstName: "Sales",
		LastName:  "User",
		Role:      models.RoleSales,
		IsActive:  true,
	}
	suite.Require().NoError(suite.db.Create(suite.salesUser).Error)

	// Create test lead
	suite.testLead = &models.Lead{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john.doe@test.com",
		Phone:     "123-456-7890",
		Company:   "Test Company",
		Status:    models.LeadStatusNew,
		Source:    "website",
		OwnerID:   suite.salesUser.ID,
	}
	suite.Require().NoError(suite.db.Create(suite.testLead).Error)

	// Create test customer
	suite.testCustomer = &models.Customer{
		FirstName: "Jane",
		LastName:  "Smith",
		Email:     "jane.smith@test.com",
		Phone:     "098-765-4321",
		Company:   "Customer Company",
	}
	suite.Require().NoError(suite.db.Create(suite.testCustomer).Error)

	// Setup repositories
	userRepo := repository.NewUserRepository(suite.db)
	leadRepo := repository.NewLeadRepository(suite.db)
	customerRepo := repository.NewCustomerRepository(suite.db)
	taskRepo := repository.NewTaskRepository(suite.db)
	apiKeyRepo := repository.NewAPIKeyRepository(suite.db)

	// Setup services
	jwtConfig := config.JWTConfig{
		Secret:      "test-secret",
		ExpiryHours: 24,
	}
	authService := service.NewAuthService(userRepo, apiKeyRepo, jwtConfig)
	taskService := service.NewTaskService(taskRepo, userRepo, leadRepo, customerRepo)

	// Generate test tokens
	suite.adminToken, err = authService.GenerateJWT(suite.adminUser)
	suite.Require().NoError(err)

	suite.salesToken, err = authService.GenerateJWT(suite.salesUser)
	suite.Require().NoError(err)

	// Setup handlers
	taskHandler := handler.NewTaskHandler(taskService)

	// Setup router
	suite.router = gin.New()
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
	
	// Setup routes with auth middleware
	api := suite.router.Group("/api/v1")
	protected := api.Group("")
	protected.Use(func(c *gin.Context) {
		// Simple auth middleware for testing
		token := c.GetHeader("Authorization")
		if token == "" {
			utils.RespondUnauthorized(c, "")
			c.Abort()
			return
		}

		// Remove "Bearer " prefix
		if len(token) > 7 && token[:7] == "Bearer " {
			token = token[7:]
		}

		// Validate token and set user context
		if token == suite.adminToken {
			c.Set("user_id", suite.adminUser.ID)
			c.Set("user_role", string(suite.adminUser.Role))
		} else if token == suite.salesToken {
			c.Set("user_id", suite.salesUser.ID)
			c.Set("user_role", string(suite.salesUser.Role))
		} else {
			utils.RespondUnauthorized(c, "")
			c.Abort()
			return
		}
		c.Next()
	})

	handler.SetupTaskRoutes(protected, taskHandler)
}

func (suite *TaskIntegrationTestSuite) TearDownTest() {
	// Clean up tasks created during tests
	suite.db.Where("1 = 1").Delete(&models.Task{})
}

func (suite *TaskIntegrationTestSuite) makeAuthenticatedRequest(method, url string, body interface{}, token string) (*httptest.ResponseRecorder, error) {
	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}

	req := httptest.NewRequest(method, url, bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	return w, nil
}

func (suite *TaskIntegrationTestSuite) TestTaskLifecycle() {
	// Test creating a task
	createReq := handler.CreateTaskRequest{
		Title:        "Integration Test Task",
		Description:  "Test task for integration testing",
		Priority:     models.TaskPriorityHigh,
		AssignedToID: suite.salesUser.ID,
		LeadID:       &suite.testLead.ID,
	}

	w, err := suite.makeAuthenticatedRequest("POST", "/api/v1/tasks", createReq, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusCreated, w.Code)

	var createResponse utils.APIResponse
	err = json.Unmarshal(w.Body.Bytes(), &createResponse)
	suite.Require().NoError(err)
	suite.True(createResponse.Success)

	// Extract task data
	taskData, ok := createResponse.Data.(map[string]interface{})
	suite.Require().True(ok)
	taskID := uint(taskData["id"].(float64))

	// Test getting the task
	w, err = suite.makeAuthenticatedRequest("GET", fmt.Sprintf("/api/v1/tasks/%d", taskID), nil, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, w.Code)

	var getResponse utils.APIResponse
	err = json.Unmarshal(w.Body.Bytes(), &getResponse)
	suite.Require().NoError(err)
	suite.True(getResponse.Success)

	retrievedTask, ok := getResponse.Data.(map[string]interface{})
	suite.Require().True(ok)
	suite.Equal(createReq.Title, retrievedTask["title"])
	suite.Equal(createReq.Description, retrievedTask["description"])
	suite.Equal(string(createReq.Priority), retrievedTask["priority"])

	// Test updating the task
	updateReq := handler.UpdateTaskRequest{
		Title:  "Updated Integration Test Task",
		Status: models.TaskStatusInProgress,
	}

	w, err = suite.makeAuthenticatedRequest("PUT", fmt.Sprintf("/api/v1/tasks/%d", taskID), updateReq, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, w.Code)

	var updateResponse utils.APIResponse
	err = json.Unmarshal(w.Body.Bytes(), &updateResponse)
	suite.Require().NoError(err)
	suite.True(updateResponse.Success)

	updatedTask, ok := updateResponse.Data.(map[string]interface{})
	suite.Require().True(ok)
	suite.Equal(updateReq.Title, updatedTask["title"])
	suite.Equal(string(updateReq.Status), updatedTask["status"])

	// Test listing tasks
	w, err = suite.makeAuthenticatedRequest("GET", "/api/v1/tasks", nil, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, w.Code)

	var listResponse utils.APIResponse
	err = json.Unmarshal(w.Body.Bytes(), &listResponse)
	suite.Require().NoError(err)
	suite.True(listResponse.Success)

	// Verify task is in the list
	listData, ok := listResponse.Data.(map[string]interface{})
	suite.Require().True(ok)
	tasks, ok := listData["tasks"].([]interface{})
	suite.Require().True(ok)
	suite.GreaterOrEqual(len(tasks), 1)

	// Test deleting the task (only admin can delete)
	w, err = suite.makeAuthenticatedRequest("DELETE", fmt.Sprintf("/api/v1/tasks/%d", taskID), nil, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusNoContent, w.Code)

	// Verify task is deleted
	w, err = suite.makeAuthenticatedRequest("GET", fmt.Sprintf("/api/v1/tasks/%d", taskID), nil, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusNotFound, w.Code)
}

func (suite *TaskIntegrationTestSuite) TestTaskPermissions() {
	// Admin creates a task assigned to sales user
	createReq := handler.CreateTaskRequest{
		Title:        "Permission Test Task",
		Description:  "Test task for permission testing",
		Priority:     models.TaskPriorityMedium,
		AssignedToID: suite.salesUser.ID,
		CustomerID:   &suite.testCustomer.ID,
	}

	w, err := suite.makeAuthenticatedRequest("POST", "/api/v1/tasks", createReq, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusCreated, w.Code)

	var createResponse utils.APIResponse
	err = json.Unmarshal(w.Body.Bytes(), &createResponse)
	suite.Require().NoError(err)

	taskData, ok := createResponse.Data.(map[string]interface{})
	suite.Require().True(ok)
	taskID := uint(taskData["id"].(float64))

	// Sales user should be able to view their assigned task
	w, err = suite.makeAuthenticatedRequest("GET", fmt.Sprintf("/api/v1/tasks/%d", taskID), nil, suite.salesToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, w.Code)

	// Sales user should be able to update their assigned task
	updateReq := handler.UpdateTaskRequest{
		Status: models.TaskStatusInProgress,
	}

	w, err = suite.makeAuthenticatedRequest("PUT", fmt.Sprintf("/api/v1/tasks/%d", taskID), updateReq, suite.salesToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, w.Code)

	// Sales user should NOT be able to delete tasks
	w, err = suite.makeAuthenticatedRequest("DELETE", fmt.Sprintf("/api/v1/tasks/%d", taskID), nil, suite.salesToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusForbidden, w.Code)

	// Sales user should NOT be able to reassign tasks
	reassignReq := handler.UpdateTaskRequest{
		AssignedToID: suite.adminUser.ID,
	}

	w, err = suite.makeAuthenticatedRequest("PUT", fmt.Sprintf("/api/v1/tasks/%d", taskID), reassignReq, suite.salesToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusForbidden, w.Code)

	// Clean up
	w, err = suite.makeAuthenticatedRequest("DELETE", fmt.Sprintf("/api/v1/tasks/%d", taskID), nil, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusNoContent, w.Code)
}

func (suite *TaskIntegrationTestSuite) TestTaskCreationPermissions() {
	// Sales user can only assign tasks to themselves
	createReq := handler.CreateTaskRequest{
		Title:        "Self-Assigned Task",
		Description:  "Task assigned to self",
		Priority:     models.TaskPriorityLow,
		AssignedToID: suite.salesUser.ID,
	}

	w, err := suite.makeAuthenticatedRequest("POST", "/api/v1/tasks", createReq, suite.salesToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusCreated, w.Code)

	var response utils.APIResponse
	err = json.Unmarshal(w.Body.Bytes(), &response)
	suite.Require().NoError(err)

	taskData, ok := response.Data.(map[string]interface{})
	suite.Require().True(ok)
	taskID := uint(taskData["id"].(float64))

	// Sales user should NOT be able to assign tasks to others
	createReq.AssignedToID = suite.adminUser.ID
	createReq.Title = "Task for Admin"

	w, err = suite.makeAuthenticatedRequest("POST", "/api/v1/tasks", createReq, suite.salesToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusForbidden, w.Code)

	// Clean up
	w, err = suite.makeAuthenticatedRequest("DELETE", fmt.Sprintf("/api/v1/tasks/%d", taskID), nil, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusNoContent, w.Code)
}

func (suite *TaskIntegrationTestSuite) TestTaskValidation() {
	// Test creating task with missing required fields
	createReq := handler.CreateTaskRequest{
		Description:  "Task without title",
		AssignedToID: suite.salesUser.ID,
	}

	w, err := suite.makeAuthenticatedRequest("POST", "/api/v1/tasks", createReq, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusBadRequest, w.Code)

	// Test creating task with both lead and customer (should fail)
	createReq = handler.CreateTaskRequest{
		Title:        "Invalid Task",
		AssignedToID: suite.salesUser.ID,
		LeadID:       &suite.testLead.ID,
		CustomerID:   &suite.testCustomer.ID,
	}

	w, err = suite.makeAuthenticatedRequest("POST", "/api/v1/tasks", createReq, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusBadRequest, w.Code) // Service validation error

	// Test creating task with non-existent assignee
	createReq = handler.CreateTaskRequest{
		Title:        "Task for Non-existent User",
		AssignedToID: 99999,
	}

	w, err = suite.makeAuthenticatedRequest("POST", "/api/v1/tasks", createReq, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusNotFound, w.Code)
}

func (suite *TaskIntegrationTestSuite) TestMyTasks() {
	// Create tasks assigned to sales user
	for i := 0; i < 3; i++ {
		createReq := handler.CreateTaskRequest{
			Title:        fmt.Sprintf("My Task %d", i+1),
			Description:  fmt.Sprintf("Description for task %d", i+1),
			Priority:     models.TaskPriorityMedium,
			AssignedToID: suite.salesUser.ID,
		}

		w, err := suite.makeAuthenticatedRequest("POST", "/api/v1/tasks", createReq, suite.adminToken)
		suite.Require().NoError(err)
		suite.Equal(http.StatusCreated, w.Code)
	}

	// Sales user gets their own tasks
	w, err := suite.makeAuthenticatedRequest("GET", "/api/v1/tasks/my", nil, suite.salesToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, w.Code)

	var response utils.APIResponse
	err = json.Unmarshal(w.Body.Bytes(), &response)
	suite.Require().NoError(err)
	suite.True(response.Success)

	responseData, ok := response.Data.(map[string]interface{})
	suite.Require().True(ok)
	tasks, ok := responseData["tasks"].([]interface{})
	suite.Require().True(ok)
	suite.Equal(3, len(tasks))

	// Admin gets all tasks (should be more than 3)
	w, err = suite.makeAuthenticatedRequest("GET", "/api/v1/tasks", nil, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, w.Code)

	err = json.Unmarshal(w.Body.Bytes(), &response)
	suite.Require().NoError(err)
	suite.True(response.Success)

	responseData, ok = response.Data.(map[string]interface{})
	suite.Require().True(ok)
	allTasks, ok := responseData["tasks"].([]interface{})
	suite.Require().True(ok)
	suite.GreaterOrEqual(len(allTasks), 3)

	// Sales user lists tasks (should get their own tasks, not all)
	w, err = suite.makeAuthenticatedRequest("GET", "/api/v1/tasks", nil, suite.salesToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, w.Code)

	err = json.Unmarshal(w.Body.Bytes(), &response)
	suite.Require().NoError(err)
	suite.True(response.Success)

	responseData, ok = response.Data.(map[string]interface{})
	suite.Require().True(ok)
	salesTasks, ok := responseData["tasks"].([]interface{})
	suite.Require().True(ok)
	suite.Equal(3, len(salesTasks))
}

func (suite *TaskIntegrationTestSuite) TestTaskBusinessRules() {
	// Create a task
	createReq := handler.CreateTaskRequest{
		Title:        "Business Rules Test",
		AssignedToID: suite.salesUser.ID,
	}

	w, err := suite.makeAuthenticatedRequest("POST", "/api/v1/tasks", createReq, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusCreated, w.Code)

	var createResponse utils.APIResponse
	err = json.Unmarshal(w.Body.Bytes(), &createResponse)
	suite.Require().NoError(err)

	taskData, ok := createResponse.Data.(map[string]interface{})
	suite.Require().True(ok)
	taskID := uint(taskData["id"].(float64))

	// Complete the task
	updateReq := handler.UpdateTaskRequest{
		Status: models.TaskStatusCompleted,
	}

	w, err = suite.makeAuthenticatedRequest("PUT", fmt.Sprintf("/api/v1/tasks/%d", taskID), updateReq, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, w.Code)

	// Try to change status of completed task (should fail)
	updateReq.Status = models.TaskStatusInProgress

	w, err = suite.makeAuthenticatedRequest("PUT", fmt.Sprintf("/api/v1/tasks/%d", taskID), updateReq, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusBadRequest, w.Code)

	// Clean up
	w, err = suite.makeAuthenticatedRequest("DELETE", fmt.Sprintf("/api/v1/tasks/%d", taskID), nil, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusNoContent, w.Code)
}

func (suite *TaskIntegrationTestSuite) TestTaskWithDueDate() {
	dueDate := time.Now().Add(24 * time.Hour)
	
	createReq := handler.CreateTaskRequest{
		Title:        "Task with Due Date",
		Description:  "Task that has a due date",
		Priority:     models.TaskPriorityHigh,
		AssignedToID: suite.salesUser.ID,
		DueDate:      &dueDate,
	}

	w, err := suite.makeAuthenticatedRequest("POST", "/api/v1/tasks", createReq, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusCreated, w.Code)

	var response utils.APIResponse
	err = json.Unmarshal(w.Body.Bytes(), &response)
	suite.Require().NoError(err)
	suite.True(response.Success)

	taskData, ok := response.Data.(map[string]interface{})
	suite.Require().True(ok)
	suite.NotNil(taskData["due_date"])

	taskID := uint(taskData["id"].(float64))

	// Clean up
	w, err = suite.makeAuthenticatedRequest("DELETE", fmt.Sprintf("/api/v1/tasks/%d", taskID), nil, suite.adminToken)
	suite.Require().NoError(err)
	suite.Equal(http.StatusNoContent, w.Code)
}

func TestTaskIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(TaskIntegrationTestSuite))
}