package service

import (
	"testing"

	"github.com/florinel-chis/gocrm/internal/config"
	"github.com/florinel-chis/gocrm/internal/mocks"
	"github.com/florinel-chis/gocrm/internal/models"
	"github.com/florinel-chis/gocrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
)

type TaskServiceTestSuite struct {
	suite.Suite
	mockTaskRepo     *mocks.TaskRepository
	mockUserRepo     *mocks.UserRepository
	mockLeadRepo     *mocks.LeadRepository
	mockCustomerRepo *mocks.CustomerRepository
	service          TaskService
}

func (suite *TaskServiceTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)
}

func (suite *TaskServiceTestSuite) SetupTest() {
	suite.mockTaskRepo = new(mocks.TaskRepository)
	suite.mockUserRepo = new(mocks.UserRepository)
	suite.mockLeadRepo = new(mocks.LeadRepository)
	suite.mockCustomerRepo = new(mocks.CustomerRepository)
	suite.service = NewTaskService(suite.mockTaskRepo, suite.mockUserRepo, suite.mockLeadRepo, suite.mockCustomerRepo)
}

func (suite *TaskServiceTestSuite) TearDownTest() {
	suite.mockTaskRepo.AssertExpectations(suite.T())
	suite.mockUserRepo.AssertExpectations(suite.T())
	suite.mockLeadRepo.AssertExpectations(suite.T())
	suite.mockCustomerRepo.AssertExpectations(suite.T())
}

func (suite *TaskServiceTestSuite) TestCreate_Success() {
	task := &models.Task{
		Title:        "Test Task",
		Description:  "Test Description",
		AssignedToID: 1,
	}

	assignee := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "user@example.com",
		IsActive:  true,
	}

	suite.mockUserRepo.On("GetByID", uint(1)).Return(assignee, nil)
	suite.mockTaskRepo.On("Create", mock.MatchedBy(func(t *models.Task) bool {
		return t.Title == "Test Task" &&
			t.Status == models.TaskStatusPending &&
			t.Priority == models.TaskPriorityMedium
	})).Return(nil).Run(func(args mock.Arguments) {
		t := args.Get(0).(*models.Task)
		t.ID = 1
	})

	err := suite.service.Create(task)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(1), task.ID)
	assert.Equal(suite.T(), models.TaskStatusPending, task.Status)
	assert.Equal(suite.T(), models.TaskPriorityMedium, task.Priority)
}

func (suite *TaskServiceTestSuite) TestCreate_AssigneeNotFound() {
	task := &models.Task{
		Title:        "Test Task",
		AssignedToID: 999,
	}

	suite.mockUserRepo.On("GetByID", uint(999)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Create(task)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "assignee not found", err.Error())
}

func (suite *TaskServiceTestSuite) TestCreate_InactiveAssignee() {
	task := &models.Task{
		Title:        "Test Task",
		AssignedToID: 1,
	}

	assignee := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "user@example.com",
		IsActive:  false, // Inactive user
	}

	suite.mockUserRepo.On("GetByID", uint(1)).Return(assignee, nil)

	err := suite.service.Create(task)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "cannot assign task to inactive user", err.Error())
}

func (suite *TaskServiceTestSuite) TestCreate_WithLead_Success() {
	leadID := uint(1)
	task := &models.Task{
		Title:        "Test Task",
		AssignedToID: 1,
		LeadID:       &leadID,
	}

	assignee := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		IsActive:  true,
	}

	lead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "lead@example.com",
	}

	suite.mockUserRepo.On("GetByID", uint(1)).Return(assignee, nil)
	suite.mockLeadRepo.On("GetByID", uint(1)).Return(lead, nil)
	suite.mockTaskRepo.On("Create", mock.AnythingOfType("*models.Task")).Return(nil)

	err := suite.service.Create(task)
	assert.NoError(suite.T(), err)
}

func (suite *TaskServiceTestSuite) TestCreate_LeadNotFound() {
	leadID := uint(999)
	task := &models.Task{
		Title:        "Test Task",
		AssignedToID: 1,
		LeadID:       &leadID,
	}

	assignee := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		IsActive:  true,
	}

	suite.mockUserRepo.On("GetByID", uint(1)).Return(assignee, nil)
	suite.mockLeadRepo.On("GetByID", uint(999)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Create(task)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "lead not found", err.Error())
}

func (suite *TaskServiceTestSuite) TestCreate_WithCustomer_Success() {
	customerID := uint(1)
	task := &models.Task{
		Title:        "Test Task",
		AssignedToID: 1,
		CustomerID:   &customerID,
	}

	assignee := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		IsActive:  true,
	}

	customer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "customer@example.com",
	}

	suite.mockUserRepo.On("GetByID", uint(1)).Return(assignee, nil)
	suite.mockCustomerRepo.On("GetByID", uint(1)).Return(customer, nil)
	suite.mockTaskRepo.On("Create", mock.AnythingOfType("*models.Task")).Return(nil)

	err := suite.service.Create(task)
	assert.NoError(suite.T(), err)
}

func (suite *TaskServiceTestSuite) TestCreate_CustomerNotFound() {
	customerID := uint(999)
	task := &models.Task{
		Title:        "Test Task",
		AssignedToID: 1,
		CustomerID:   &customerID,
	}

	assignee := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		IsActive:  true,
	}

	suite.mockUserRepo.On("GetByID", uint(1)).Return(assignee, nil)
	suite.mockCustomerRepo.On("GetByID", uint(999)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Create(task)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "customer not found", err.Error())
}

func (suite *TaskServiceTestSuite) TestCreate_BothLeadAndCustomer() {
	leadID := uint(1)
	customerID := uint(1)
	task := &models.Task{
		Title:        "Test Task",
		AssignedToID: 1,
		LeadID:       &leadID,
		CustomerID:   &customerID,
	}

	assignee := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		IsActive:  true,
	}
	
	lead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
	}
	
	customer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
	}

	suite.mockUserRepo.On("GetByID", uint(1)).Return(assignee, nil)
	suite.mockLeadRepo.On("GetByID", uint(1)).Return(lead, nil)
	suite.mockCustomerRepo.On("GetByID", uint(1)).Return(customer, nil)

	err := suite.service.Create(task)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "task cannot be linked to both lead and customer", err.Error())
}

func (suite *TaskServiceTestSuite) TestGetByID_Success() {
	expectedTask := &models.Task{
		BaseModel:   models.BaseModel{ID: 1},
		Title:       "Test Task",
		Description: "Test Description",
		Status:      models.TaskStatusPending,
	}

	suite.mockTaskRepo.On("GetByID", uint(1)).Return(expectedTask, nil)

	task, err := suite.service.GetByID(1)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedTask, task)
}

func (suite *TaskServiceTestSuite) TestGetByID_NotFound() {
	suite.mockTaskRepo.On("GetByID", uint(999)).Return(nil, gorm.ErrRecordNotFound)

	task, err := suite.service.GetByID(999)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), task)
}

func (suite *TaskServiceTestSuite) TestGetByAssignee_Success() {
	expectedTasks := []models.Task{
		{BaseModel: models.BaseModel{ID: 1}, Title: "Task 1", AssignedToID: 1},
		{BaseModel: models.BaseModel{ID: 2}, Title: "Task 2", AssignedToID: 1},
	}

	suite.mockTaskRepo.On("GetByAssignedToID", uint(1), 0, 10).Return(expectedTasks, nil)
	suite.mockTaskRepo.On("CountByAssignedToID", uint(1)).Return(int64(2), nil)

	tasks, total, err := suite.service.GetByAssignee(1, 0, 10)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedTasks, tasks)
	assert.Equal(suite.T(), int64(2), total)
}

func (suite *TaskServiceTestSuite) TestUpdate_Success() {
	existingTask := &models.Task{
		BaseModel:    models.BaseModel{ID: 1},
		Title:        "Old Title",
		Status:       models.TaskStatusPending,
		AssignedToID: 1,
	}

	updatedTask := &models.Task{
		BaseModel:    models.BaseModel{ID: 1},
		Title:        "New Title",
		Status:       models.TaskStatusInProgress,
		AssignedToID: 1,
	}

	suite.mockTaskRepo.On("GetByID", uint(1)).Return(existingTask, nil)
	suite.mockTaskRepo.On("Update", updatedTask).Return(nil)

	err := suite.service.Update(updatedTask)
	assert.NoError(suite.T(), err)
}

func (suite *TaskServiceTestSuite) TestUpdate_TaskNotFound() {
	task := &models.Task{
		BaseModel: models.BaseModel{ID: 999},
		Title:     "New Title",
	}

	suite.mockTaskRepo.On("GetByID", uint(999)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Update(task)
	assert.Error(suite.T(), err)
}

func (suite *TaskServiceTestSuite) TestUpdate_CannotChangeCompletedTask() {
	existingTask := &models.Task{
		BaseModel:    models.BaseModel{ID: 1},
		Status:       models.TaskStatusCompleted,
		AssignedToID: 1,
	}

	updatedTask := &models.Task{
		BaseModel:    models.BaseModel{ID: 1},
		Status:       models.TaskStatusPending, // Trying to change from completed
		AssignedToID: 1,
	}

	suite.mockTaskRepo.On("GetByID", uint(1)).Return(existingTask, nil)

	err := suite.service.Update(updatedTask)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "cannot change status of completed task", err.Error())
}

func (suite *TaskServiceTestSuite) TestUpdate_NewAssignee_Success() {
	existingTask := &models.Task{
		BaseModel:    models.BaseModel{ID: 1},
		Title:        "Test Task",
		AssignedToID: 1,
	}

	updatedTask := &models.Task{
		BaseModel:    models.BaseModel{ID: 1},
		Title:        "Test Task",
		AssignedToID: 2, // New assignee
	}

	newAssignee := &models.User{
		BaseModel: models.BaseModel{ID: 2},
		IsActive:  true,
	}

	suite.mockTaskRepo.On("GetByID", uint(1)).Return(existingTask, nil)
	suite.mockUserRepo.On("GetByID", uint(2)).Return(newAssignee, nil)
	suite.mockTaskRepo.On("Update", updatedTask).Return(nil)

	err := suite.service.Update(updatedTask)
	assert.NoError(suite.T(), err)
}

func (suite *TaskServiceTestSuite) TestUpdate_NewAssigneeInactive() {
	existingTask := &models.Task{
		BaseModel:    models.BaseModel{ID: 1},
		AssignedToID: 1,
	}

	updatedTask := &models.Task{
		BaseModel:    models.BaseModel{ID: 1},
		AssignedToID: 2,
	}

	newAssignee := &models.User{
		BaseModel: models.BaseModel{ID: 2},
		IsActive:  false, // Inactive user
	}

	suite.mockTaskRepo.On("GetByID", uint(1)).Return(existingTask, nil)
	suite.mockUserRepo.On("GetByID", uint(2)).Return(newAssignee, nil)

	err := suite.service.Update(updatedTask)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "cannot assign task to inactive user", err.Error())
}

func (suite *TaskServiceTestSuite) TestDelete_Success() {
	task := &models.Task{
		BaseModel: models.BaseModel{ID: 1},
		Title:     "Test Task",
	}

	suite.mockTaskRepo.On("GetByID", uint(1)).Return(task, nil)
	suite.mockTaskRepo.On("Delete", uint(1)).Return(nil)

	err := suite.service.Delete(1)
	assert.NoError(suite.T(), err)
}

func (suite *TaskServiceTestSuite) TestDelete_NotFound() {
	suite.mockTaskRepo.On("GetByID", uint(999)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Delete(999)
	assert.Error(suite.T(), err)
}

func (suite *TaskServiceTestSuite) TestList_Success() {
	expectedTasks := []models.Task{
		{BaseModel: models.BaseModel{ID: 1}, Title: "Task 1"},
		{BaseModel: models.BaseModel{ID: 2}, Title: "Task 2"},
	}

	suite.mockTaskRepo.On("List", 0, 10).Return(expectedTasks, nil)
	suite.mockTaskRepo.On("Count").Return(int64(2), nil)

	tasks, total, err := suite.service.List(0, 10)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedTasks, tasks)
	assert.Equal(suite.T(), int64(2), total)
}

func TestTaskServiceTestSuite(t *testing.T) {
	suite.Run(t, new(TaskServiceTestSuite))
}