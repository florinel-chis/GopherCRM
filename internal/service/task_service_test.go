package service

import (
	"errors"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
)

type TaskServiceTestSuite struct {
	suite.Suite
	mockTaskRepo     *mocks.TaskRepository
	mockUserRepo     *mocks.UserRepository
	mockLeadRepo     *mocks.LeadRepository
	mockCustomerRepo *mocks.CustomerRepository
	mockLabelRepo    *mocks.LabelRepository
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
	suite.mockLabelRepo = new(mocks.LabelRepository)
	suite.service = NewTaskService(suite.mockTaskRepo, suite.mockUserRepo, suite.mockLeadRepo, suite.mockCustomerRepo, suite.mockLabelRepo)
}

func (suite *TaskServiceTestSuite) TearDownTest() {
	suite.mockTaskRepo.AssertExpectations(suite.T())
	suite.mockUserRepo.AssertExpectations(suite.T())
	suite.mockLeadRepo.AssertExpectations(suite.T())
	suite.mockCustomerRepo.AssertExpectations(suite.T())
	suite.mockLabelRepo.AssertExpectations(suite.T())
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
	assert.True(suite.T(), errors.Is(err, apperrors.ErrAssigneeNotFound))
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
	assert.True(suite.T(), errors.Is(err, apperrors.ErrInactiveUser))
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
		OwnerID:   5,
	}

	suite.mockUserRepo.On("GetByID", uint(1)).Return(assignee, nil)
	suite.mockUserRepo.On("GetByID", uint(7)).Return(&models.User{BaseModel: models.BaseModel{ID: 7}, Role: models.RoleAdmin}, nil)
	suite.mockLeadRepo.On("GetByID", uint(1)).Return(lead, nil)
	suite.mockTaskRepo.On("Create", mock.AnythingOfType("*models.Task")).Return(nil)

	err := suite.service.CreateWithLabels(task, nil, 7)
	assert.NoError(suite.T(), err)
	assert.Same(suite.T(), lead, task.Lead, "the checked lead is attached for the response")
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
	assert.True(suite.T(), errors.Is(err, apperrors.ErrLeadNotFound))
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
	assert.True(suite.T(), errors.Is(err, apperrors.ErrCustomerNotFound))
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
	suite.mockUserRepo.On("GetByID", uint(7)).Return(&models.User{BaseModel: models.BaseModel{ID: 7}, Role: models.RoleAdmin}, nil)
	suite.mockLeadRepo.On("GetByID", uint(1)).Return(lead, nil)
	suite.mockCustomerRepo.On("GetByID", uint(1)).Return(customer, nil)

	err := suite.service.CreateWithLabels(task, nil, 7)
	assert.Error(suite.T(), err)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrTaskLeadCustomerConflict))
}

// --- lead visibility ---------------------------------------------------------

// leadVisibilityFixture primes an active assignee (id 1), a lead owned by user
// 20 (id 30) and the acting user (id 10) with the given role.
func (suite *TaskServiceTestSuite) leadVisibilityFixture(actorRole models.UserRole) *models.Lead {
	suite.mockUserRepo.On("GetByID", uint(1)).Return(&models.User{BaseModel: models.BaseModel{ID: 1}, IsActive: true}, nil).Maybe()
	suite.mockUserRepo.On("GetByID", uint(10)).Return(&models.User{BaseModel: models.BaseModel{ID: 10}, Role: actorRole, IsActive: true}, nil).Maybe()
	lead := &models.Lead{BaseModel: models.BaseModel{ID: 30}, Email: "lead@example.com", OwnerID: 20}
	suite.mockLeadRepo.On("GetByID", uint(30)).Return(lead, nil).Maybe()
	return lead
}

// Support, customer and a sales user who does not own the lead are all refused,
// with the same sentinel a missing lead gets, and nothing is written.
func (suite *TaskServiceTestSuite) TestCreate_LeadHiddenFromTheActorIsRejected() {
	for _, role := range []models.UserRole{models.RoleSupport, models.RoleCustomer, models.RoleSales} {
		suite.SetupTest()
		suite.leadVisibilityFixture(role)
		leadID := uint(30)
		task := &models.Task{Title: "Probe", AssignedToID: 1, LeadID: &leadID}

		err := suite.service.CreateWithLabels(task, nil, 10)
		assert.Truef(suite.T(), errors.Is(err, apperrors.ErrLeadNotFound), "%s: got %v", role, err)
		assert.Nilf(suite.T(), task.Lead, "%s: nothing about the lead is handed back", role)
		suite.mockTaskRepo.AssertNotCalled(suite.T(), "Create", mock.Anything)
		suite.mockTaskRepo.AssertNotCalled(suite.T(), "CreateWithLabels", mock.Anything, mock.Anything)
	}
}

func (suite *TaskServiceTestSuite) TestCreate_SalesMayLinkItsOwnLead() {
	lead := suite.leadVisibilityFixture(models.RoleSales)
	lead.OwnerID = 10
	leadID := uint(30)
	task := &models.Task{Title: "Follow up", AssignedToID: 1, LeadID: &leadID}
	suite.mockTaskRepo.On("Create", task).Return(nil)

	require.NoError(suite.T(), suite.service.CreateWithLabels(task, nil, 10))
	assert.Same(suite.T(), lead, task.Lead)
}

// Create has no acting user, so it cannot link a lead at all, whoever owns it.
func (suite *TaskServiceTestSuite) TestCreate_WithoutAnActorCannotLinkALead() {
	suite.leadVisibilityFixture(models.RoleAdmin)
	leadID := uint(30)

	err := suite.service.Create(&models.Task{Title: "Anonymous", AssignedToID: 1, LeadID: &leadID})
	assert.True(suite.T(), errors.Is(err, apperrors.ErrLeadNotFound), "got %v", err)
	suite.mockUserRepo.AssertNotCalled(suite.T(), "GetByID", uint(10))
	suite.mockTaskRepo.AssertNotCalled(suite.T(), "Create", mock.Anything)
}

// A failing lookup of the actor is a server error, not a hidden lead.
func (suite *TaskServiceTestSuite) TestCreate_ActorLookupFailureIsNotALeadMiss() {
	suite.mockUserRepo.On("GetByID", uint(1)).Return(&models.User{BaseModel: models.BaseModel{ID: 1}, IsActive: true}, nil)
	suite.mockUserRepo.On("GetByID", uint(10)).Return(nil, errors.New("connection refused"))
	suite.mockLeadRepo.On("GetByID", uint(30)).Return(&models.Lead{BaseModel: models.BaseModel{ID: 30}, OwnerID: 10}, nil)
	leadID := uint(30)

	err := suite.service.CreateWithLabels(&models.Task{Title: "X", AssignedToID: 1, LeadID: &leadID}, nil, 10)
	assert.Error(suite.T(), err)
	assert.False(suite.T(), errors.Is(err, apperrors.ErrLeadNotFound))
	suite.mockTaskRepo.AssertNotCalled(suite.T(), "Create", mock.Anything)
}

// An update that moves the task onto a lead hidden from the actor is refused
// and writes nothing.
func (suite *TaskServiceTestSuite) TestUpdate_ChangingToAHiddenLeadIsRejected() {
	suite.leadVisibilityFixture(models.RoleSales)
	suite.mockTaskRepo.On("GetByID", uint(5)).Return(&models.Task{BaseModel: models.BaseModel{ID: 5}, AssignedToID: 1}, nil)
	leadID := uint(30)
	task := &models.Task{BaseModel: models.BaseModel{ID: 5}, AssignedToID: 1, LeadID: &leadID}

	err := suite.service.UpdateWithLabels(task, nil, 10)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrLeadNotFound), "got %v", err)
	suite.mockTaskRepo.AssertNotCalled(suite.T(), "Update", mock.Anything)
	suite.mockTaskRepo.AssertNotCalled(suite.T(), "UpdateWithLabels", mock.Anything, mock.Anything)
}

// A link the update leaves alone is not re-checked — compared by id, not by
// pointer — so the assignee can keep editing a task whose lead has changed
// hands. Hiding that link is the read side's job (RedactTaskLead).
func (suite *TaskServiceTestSuite) TestUpdate_UnchangedLinkIsNotRechecked() {
	stored, same := uint(30), uint(30)
	suite.mockTaskRepo.On("GetByID", uint(5)).Return(&models.Task{BaseModel: models.BaseModel{ID: 5}, AssignedToID: 1, LeadID: &stored}, nil)
	task := &models.Task{BaseModel: models.BaseModel{ID: 5}, Title: "Renamed", AssignedToID: 1, LeadID: &same,
		Lead: &models.Lead{BaseModel: models.BaseModel{ID: 30}, OwnerID: 20}}
	suite.mockTaskRepo.On("Update", task).Return(nil)

	require.NoError(suite.T(), suite.service.UpdateWithLabels(task, nil, 10))
	suite.mockLeadRepo.AssertNotCalled(suite.T(), "GetByID", mock.Anything)
	suite.mockUserRepo.AssertNotCalled(suite.T(), "GetByID", mock.Anything)
}

// Moving the link drops the lead preloaded for the old one before the save,
// so GORM cannot write the old id back, and attaches the new one afterwards.
func (suite *TaskServiceTestSuite) TestUpdate_ChangingToAVisibleLeadSwapsThePreloadedLead() {
	lead := suite.leadVisibilityFixture(models.RoleAdmin)
	old, next := uint(29), uint(30)
	suite.mockTaskRepo.On("GetByID", uint(5)).Return(&models.Task{BaseModel: models.BaseModel{ID: 5}, AssignedToID: 1, LeadID: &old}, nil)
	task := &models.Task{BaseModel: models.BaseModel{ID: 5}, AssignedToID: 1, LeadID: &next,
		Lead: &models.Lead{BaseModel: models.BaseModel{ID: 29}}}
	suite.mockTaskRepo.On("Update", mock.MatchedBy(func(t *models.Task) bool {
		return t.Lead == nil && t.LeadID != nil && *t.LeadID == 30
	})).Return(nil)

	require.NoError(suite.T(), suite.service.UpdateWithLabels(task, nil, 10))
	assert.Same(suite.T(), lead, task.Lead)
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
	// A missing task must be classifiable by the caller, not guessed at.
	assert.True(suite.T(), errors.Is(err, apperrors.ErrNotFound))
}

// A repository failure that is not a missing row must stay unclassified, so the
// handler can tell it apart from a genuine 404.
func (suite *TaskServiceTestSuite) TestGetByID_RepositoryError_NotClassifiedAsNotFound() {
	dbErr := errors.New("connection refused")
	suite.mockTaskRepo.On("GetByID", uint(1)).Return(nil, dbErr)

	task, err := suite.service.GetByID(1)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), task)
	assert.False(suite.T(), errors.Is(err, apperrors.ErrNotFound))
	assert.True(suite.T(), errors.Is(err, dbErr))
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
	assert.True(suite.T(), errors.Is(err, apperrors.ErrCompletedTaskModify))
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
	assert.True(suite.T(), errors.Is(err, apperrors.ErrInactiveUser))
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
	assert.True(suite.T(), errors.Is(err, apperrors.ErrNotFound))
}

// A lookup failure that is not a missing row must not be reported as not-found.
func (suite *TaskServiceTestSuite) TestDelete_LookupError_NotClassifiedAsNotFound() {
	dbErr := errors.New("connection refused")
	suite.mockTaskRepo.On("GetByID", uint(1)).Return(nil, dbErr)

	err := suite.service.Delete(1)
	assert.Error(suite.T(), err)
	assert.False(suite.T(), errors.Is(err, apperrors.ErrNotFound))
	assert.True(suite.T(), errors.Is(err, dbErr))
}

// The delete itself failing is a server error, not a missing task.
func (suite *TaskServiceTestSuite) TestDelete_RepositoryError_NotClassifiedAsNotFound() {
	dbErr := errors.New("deadlock detected")
	task := &models.Task{
		BaseModel: models.BaseModel{ID: 1},
		Title:     "Test Task",
	}

	suite.mockTaskRepo.On("GetByID", uint(1)).Return(task, nil)
	suite.mockTaskRepo.On("Delete", uint(1)).Return(dbErr)

	err := suite.service.Delete(1)
	assert.Error(suite.T(), err)
	assert.False(suite.T(), errors.Is(err, apperrors.ErrNotFound))
	assert.True(suite.T(), errors.Is(err, dbErr))
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

// --- dashboard analytics -----------------------------------------------------

func (suite *TaskServiceTestSuite) TestGetStatusCounts_Success() {
	expected := map[string]int64{"pending": 4, "completed": 2}
	suite.mockTaskRepo.On("CountByStatus").Return(expected, nil)

	counts, err := suite.service.GetStatusCounts()

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expected, counts)
}

func (suite *TaskServiceTestSuite) TestGetStatusCounts_RepositoryError() {
	suite.mockTaskRepo.On("CountByStatus").Return(nil, errors.New("database is down"))

	counts, err := suite.service.GetStatusCounts()

	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), counts)
}

// A nil assignee filter is what makes the query span every assignee; passing a
// zero uint instead would silently match nobody.
func (suite *TaskServiceTestSuite) TestGetUpcoming_PassesNilAssigneeFilter() {
	suite.mockTaskRepo.On("ListUpcoming", (*uint)(nil), 5).Return([]models.Task{{Title: "Due soon"}}, nil)

	tasks, err := suite.service.GetUpcoming(5)

	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), tasks, 1)
}

func (suite *TaskServiceTestSuite) TestGetUpcomingByAssignee_PassesAssigneeFilter() {
	suite.mockTaskRepo.On("ListUpcoming", mock.MatchedBy(func(assigneeID *uint) bool {
		return assigneeID != nil && *assigneeID == 12
	}), 5).Return([]models.Task{}, nil)

	tasks, err := suite.service.GetUpcomingByAssignee(12, 5)

	assert.NoError(suite.T(), err)
	assert.Empty(suite.T(), tasks)
}

func (suite *TaskServiceTestSuite) TestGetDueWithin_PassesWindowAndNilAssignee() {
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 7)
	suite.mockTaskRepo.On("ListDueBetween", (*uint)(nil), from, to, 100).Return([]models.Task{{Title: "In window"}}, nil)

	tasks, err := suite.service.GetDueWithin(from, to, 100)

	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), tasks, 1)
}

func (suite *TaskServiceTestSuite) TestGetDueWithinByAssignee_PassesWindowAndAssignee() {
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 7)
	suite.mockTaskRepo.On("ListDueBetween", mock.MatchedBy(func(assigneeID *uint) bool {
		return assigneeID != nil && *assigneeID == 9
	}), from, to, 100).Return([]models.Task{}, nil)

	tasks, err := suite.service.GetDueWithinByAssignee(9, from, to, 100)

	assert.NoError(suite.T(), err)
	assert.Empty(suite.T(), tasks)
}

func (suite *TaskServiceTestSuite) TestGetRecentlyCompleted_Success() {
	suite.mockTaskRepo.On("ListRecentlyCompleted", 10).Return([]models.Task{{Title: "Done"}}, nil)

	tasks, err := suite.service.GetRecentlyCompleted(10)

	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), tasks, 1)
}

func TestTaskServiceTestSuite(t *testing.T) {
	suite.Run(t, new(TaskServiceTestSuite))
}
