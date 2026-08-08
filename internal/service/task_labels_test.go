package service

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

// TaskLabelServiceTestSuite covers the label half of the task service. It is a
// separate suite from TaskServiceTestSuite so the existing task expectations
// stay readable.
type TaskLabelServiceTestSuite struct {
	suite.Suite
	mockTaskRepo     *mocks.TaskRepository
	mockUserRepo     *mocks.UserRepository
	mockLeadRepo     *mocks.LeadRepository
	mockCustomerRepo *mocks.CustomerRepository
	mockLabelRepo    *mocks.LabelRepository
	service          TaskService
}

func (suite *TaskLabelServiceTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
}

func (suite *TaskLabelServiceTestSuite) SetupTest() {
	suite.mockTaskRepo = new(mocks.TaskRepository)
	suite.mockUserRepo = new(mocks.UserRepository)
	suite.mockLeadRepo = new(mocks.LeadRepository)
	suite.mockCustomerRepo = new(mocks.CustomerRepository)
	suite.mockLabelRepo = new(mocks.LabelRepository)
	suite.service = NewTaskService(suite.mockTaskRepo, suite.mockUserRepo, suite.mockLeadRepo, suite.mockCustomerRepo, suite.mockLabelRepo)
}

func (suite *TaskLabelServiceTestSuite) TearDownTest() {
	suite.mockTaskRepo.AssertExpectations(suite.T())
	suite.mockUserRepo.AssertExpectations(suite.T())
	suite.mockLabelRepo.AssertExpectations(suite.T())
}

func (suite *TaskLabelServiceTestSuite) activeAssignee() *models.User {
	assignee := &models.User{BaseModel: models.BaseModel{ID: 1}, Email: "a@example.com", IsActive: true}
	suite.mockUserRepo.On("GetByID", uint(1)).Return(assignee, nil)
	return assignee
}

func (suite *TaskLabelServiceTestSuite) TestCreateWithLabels_AttachesResolvedLabels() {
	suite.activeAssignee()
	labels := []models.Label{{BaseModel: models.BaseModel{ID: 2}, Name: "Urgent", Color: "#FF0000"}}

	suite.mockLabelRepo.On("FindByIDs", []uint{2}).Return(labels, nil)
	suite.mockTaskRepo.On("CreateWithLabels", mock.AnythingOfType("*models.Task"), labels).Return(nil)

	task := &models.Task{Title: "Labelled", AssignedToID: 1}
	assert.NoError(suite.T(), suite.service.CreateWithLabels(task, []uint{2}))
}

// Without any label ids the labelled path must be indistinguishable from the
// plain one, right down to the repository method it calls — otherwise every
// existing caller would silently start writing through a different code path.
func (suite *TaskLabelServiceTestSuite) TestCreateWithLabels_NoIDsUsesThePlainCreate() {
	suite.activeAssignee()
	suite.mockTaskRepo.On("Create", mock.AnythingOfType("*models.Task")).Return(nil)

	task := &models.Task{Title: "Bare", AssignedToID: 1}
	assert.NoError(suite.T(), suite.service.CreateWithLabels(task, nil))
	suite.mockTaskRepo.AssertNotCalled(suite.T(), "CreateWithLabels", mock.Anything, mock.Anything)
	suite.mockLabelRepo.AssertNotCalled(suite.T(), "FindByIDs", mock.Anything)
}

// A single unknown id rejects the whole request: attaching the labels that do
// exist and quietly dropping the rest would leave the caller believing a label
// was applied when it was not.
func (suite *TaskLabelServiceTestSuite) TestCreateWithLabels_UnknownIDRejectsTheWholeRequest() {
	suite.activeAssignee()

	suite.mockLabelRepo.On("FindByIDs", []uint{2, 99}).
		Return([]models.Label{{BaseModel: models.BaseModel{ID: 2}}}, nil)

	err := suite.service.CreateWithLabels(&models.Task{Title: "Bad ref", AssignedToID: 1}, []uint{2, 99})
	assert.ErrorIs(suite.T(), err, apperrors.ErrLabelNotFound)
	assert.Contains(suite.T(), err.Error(), "99", "the offending id belongs in the message")
	suite.mockTaskRepo.AssertNotCalled(suite.T(), "Create", mock.Anything)
	suite.mockTaskRepo.AssertNotCalled(suite.T(), "CreateWithLabels", mock.Anything, mock.Anything)
}

func (suite *TaskLabelServiceTestSuite) TestCreateWithLabels_CollapsesRepeatedIDs() {
	suite.activeAssignee()
	labels := []models.Label{{BaseModel: models.BaseModel{ID: 2}}}

	// The repository is asked for the ids exactly once each, so the
	// found-versus-requested comparison cannot be tripped by a repeat.
	suite.mockLabelRepo.On("FindByIDs", []uint{2}).Return(labels, nil)
	suite.mockTaskRepo.On("CreateWithLabels", mock.AnythingOfType("*models.Task"), labels).Return(nil)

	assert.NoError(suite.T(), suite.service.CreateWithLabels(&models.Task{Title: "Dup", AssignedToID: 1}, []uint{2, 2, 2}))
}

// Labels are only resolved once the task itself is known to be valid; an
// invalid task must fail on its own terms and never touch the label store.
func (suite *TaskLabelServiceTestSuite) TestCreateWithLabels_InvalidAssigneeShortCircuits() {
	suite.mockUserRepo.On("GetByID", uint(1)).
		Return(&models.User{BaseModel: models.BaseModel{ID: 1}, IsActive: false}, nil)

	err := suite.service.CreateWithLabels(&models.Task{Title: "Inactive", AssignedToID: 1}, []uint{2})
	assert.ErrorIs(suite.T(), err, apperrors.ErrInactiveUser)
	suite.mockLabelRepo.AssertNotCalled(suite.T(), "FindByIDs", mock.Anything)
}

func (suite *TaskLabelServiceTestSuite) existingTask() *models.Task {
	task := &models.Task{
		BaseModel:    models.BaseModel{ID: 5},
		Title:        "Existing",
		Status:       models.TaskStatusPending,
		AssignedToID: 1,
	}
	suite.mockTaskRepo.On("GetByID", uint(5)).Return(task, nil)
	return task
}

// The pointer is what carries the difference between "leave the labels alone"
// and "clear them", and getting it backwards would silently wipe every task's
// labels on an unrelated edit.
func (suite *TaskLabelServiceTestSuite) TestUpdateWithLabels_NilPointerLeavesLabelsUntouched() {
	task := suite.existingTask()
	suite.mockTaskRepo.On("Update", task).Return(nil)

	assert.NoError(suite.T(), suite.service.UpdateWithLabels(task, nil))
	suite.mockTaskRepo.AssertNotCalled(suite.T(), "UpdateWithLabels", mock.Anything, mock.Anything)
	suite.mockLabelRepo.AssertNotCalled(suite.T(), "FindByIDs", mock.Anything)
}

func (suite *TaskLabelServiceTestSuite) TestUpdateWithLabels_EmptySliceClearsTheSet() {
	task := suite.existingTask()
	empty := []uint{}

	suite.mockTaskRepo.On("UpdateWithLabels", task, []models.Label(nil)).Return(nil)

	assert.NoError(suite.T(), suite.service.UpdateWithLabels(task, &empty))
	suite.mockTaskRepo.AssertNotCalled(suite.T(), "Update", mock.Anything)
	suite.mockLabelRepo.AssertNotCalled(suite.T(), "FindByIDs", mock.Anything)
}

func (suite *TaskLabelServiceTestSuite) TestUpdateWithLabels_ReplacesTheSet() {
	task := suite.existingTask()
	ids := []uint{2}
	labels := []models.Label{{BaseModel: models.BaseModel{ID: 2}, Name: "Urgent", Color: "#FF0000"}}

	suite.mockLabelRepo.On("FindByIDs", ids).Return(labels, nil)
	suite.mockTaskRepo.On("UpdateWithLabels", task, labels).Return(nil)

	assert.NoError(suite.T(), suite.service.UpdateWithLabels(task, &ids))
}

func (suite *TaskLabelServiceTestSuite) TestUpdateWithLabels_UnknownIDRejectsTheWholeRequest() {
	task := suite.existingTask()
	ids := []uint{99}

	suite.mockLabelRepo.On("FindByIDs", ids).Return([]models.Label{}, nil)

	err := suite.service.UpdateWithLabels(task, &ids)
	assert.ErrorIs(suite.T(), err, apperrors.ErrLabelNotFound)
	suite.mockTaskRepo.AssertNotCalled(suite.T(), "Update", mock.Anything)
	suite.mockTaskRepo.AssertNotCalled(suite.T(), "UpdateWithLabels", mock.Anything, mock.Anything)
}

func (suite *TaskLabelServiceTestSuite) TestListByLabel_ListsAndCountsAcrossEveryAssignee() {
	tasks := []models.Task{{BaseModel: models.BaseModel{ID: 1}, Title: "Tagged"}}

	suite.mockTaskRepo.On("ListByLabel", uint(3), (*uint)(nil), 0, 20, "title", "asc", []string{"AssignedTo", "Labels"}).
		Return(tasks, nil)
	suite.mockTaskRepo.On("CountByLabel", uint(3), (*uint)(nil)).Return(int64(1), nil)

	got, total, err := suite.service.ListByLabel(3, 0, 20, "title", "asc")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(1), total)
	assert.Len(suite.T(), got, 1)
}

// The assignee narrowing is pushed down to SQL rather than filtered afterwards,
// so the pointer has to arrive at the repository.
func (suite *TaskLabelServiceTestSuite) TestListByLabelForAssignee_PushesTheAssigneeDown() {
	assignee := uint(8)

	suite.mockTaskRepo.On("ListByLabel", uint(3), mock.MatchedBy(func(id *uint) bool {
		return id != nil && *id == assignee
	}), 0, 20, "", "", []string{"AssignedTo", "Labels"}).Return([]models.Task{}, nil)
	suite.mockTaskRepo.On("CountByLabel", uint(3), mock.MatchedBy(func(id *uint) bool {
		return id != nil && *id == assignee
	})).Return(int64(0), nil)

	_, total, err := suite.service.ListByLabelForAssignee(assignee, 3, 0, 20, "", "")
	assert.NoError(suite.T(), err)
	assert.Zero(suite.T(), total)
}

func TestTaskLabelServiceTestSuite(t *testing.T) {
	suite.Run(t, new(TaskLabelServiceTestSuite))
}
