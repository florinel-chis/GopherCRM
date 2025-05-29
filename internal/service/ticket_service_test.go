package service

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
)

type TicketServiceTestSuite struct {
	suite.Suite
	mockTicketRepo   *mocks.TicketRepository
	mockCustomerRepo *mocks.CustomerRepository
	mockUserRepo     *mocks.UserRepository
	service          TicketService
}

func (suite *TicketServiceTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)
}

func (suite *TicketServiceTestSuite) SetupTest() {
	suite.mockTicketRepo = new(mocks.TicketRepository)
	suite.mockCustomerRepo = new(mocks.CustomerRepository)
	suite.mockUserRepo = new(mocks.UserRepository)
	suite.service = NewTicketService(suite.mockTicketRepo, suite.mockCustomerRepo, suite.mockUserRepo)
}

func (suite *TicketServiceTestSuite) TearDownTest() {
	suite.mockTicketRepo.AssertExpectations(suite.T())
	suite.mockCustomerRepo.AssertExpectations(suite.T())
	suite.mockUserRepo.AssertExpectations(suite.T())
}

func (suite *TicketServiceTestSuite) TestCreate_Success() {
	ticket := &models.Ticket{
		Title:       "Test Ticket",
		Description: "Test Description",
		CustomerID:  1,
		AssignedToID: uintPtr(2),
	}

	customer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "customer@example.com",
	}

	assignee := &models.User{
		BaseModel: models.BaseModel{ID: 2},
		Email:     "support@example.com",
		Role:      models.RoleSupport,
	}

	suite.mockCustomerRepo.On("GetByID", uint(1)).Return(customer, nil)
	suite.mockUserRepo.On("GetByID", uint(2)).Return(assignee, nil)
	suite.mockTicketRepo.On("Create", mock.MatchedBy(func(t *models.Ticket) bool {
		return t.Title == "Test Ticket" &&
			t.Status == models.TicketStatusOpen &&
			t.Priority == models.TicketPriorityMedium
	})).Return(nil).Run(func(args mock.Arguments) {
		t := args.Get(0).(*models.Ticket)
		t.ID = 1
	})

	err := suite.service.Create(ticket)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(1), ticket.ID)
	assert.Equal(suite.T(), models.TicketStatusOpen, ticket.Status)
	assert.Equal(suite.T(), models.TicketPriorityMedium, ticket.Priority)
}

func (suite *TicketServiceTestSuite) TestCreate_CustomerNotFound() {
	ticket := &models.Ticket{
		Title:       "Test Ticket",
		Description: "Test Description",
		CustomerID:  999,
	}

	suite.mockCustomerRepo.On("GetByID", uint(999)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Create(ticket)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "customer not found", err.Error())
}

func (suite *TicketServiceTestSuite) TestCreate_AssigneeNotFound() {
	ticket := &models.Ticket{
		Title:        "Test Ticket",
		Description:  "Test Description",
		CustomerID:   1,
		AssignedToID: uintPtr(999),
	}

	customer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
	}

	suite.mockCustomerRepo.On("GetByID", uint(1)).Return(customer, nil)
	suite.mockUserRepo.On("GetByID", uint(999)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Create(ticket)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "assignee not found", err.Error())
}

func (suite *TicketServiceTestSuite) TestCreate_InvalidAssigneeRole() {
	ticket := &models.Ticket{
		Title:        "Test Ticket",
		Description:  "Test Description",
		CustomerID:   1,
		AssignedToID: uintPtr(2),
	}

	customer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
	}

	assignee := &models.User{
		BaseModel: models.BaseModel{ID: 2},
		Role:      models.RoleSales, // Invalid role for ticket assignment
	}

	suite.mockCustomerRepo.On("GetByID", uint(1)).Return(customer, nil)
	suite.mockUserRepo.On("GetByID", uint(2)).Return(assignee, nil)

	err := suite.service.Create(ticket)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "tickets can only be assigned to support or admin users", err.Error())
}

func (suite *TicketServiceTestSuite) TestGetByID_Success() {
	expectedTicket := &models.Ticket{
		BaseModel:   models.BaseModel{ID: 1},
		Title:       "Test Ticket",
		Description: "Test Description",
		CustomerID:  1,
		Status:      models.TicketStatusOpen,
	}

	suite.mockTicketRepo.On("GetByID", uint(1)).Return(expectedTicket, nil)

	ticket, err := suite.service.GetByID(1)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedTicket, ticket)
}

func (suite *TicketServiceTestSuite) TestGetByID_NotFound() {
	suite.mockTicketRepo.On("GetByID", uint(999)).Return(nil, gorm.ErrRecordNotFound)

	ticket, err := suite.service.GetByID(999)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), ticket)
}

func (suite *TicketServiceTestSuite) TestGetByCustomer_Success() {
	expectedTickets := []models.Ticket{
		{BaseModel: models.BaseModel{ID: 1}, Title: "Ticket 1", CustomerID: 1},
		{BaseModel: models.BaseModel{ID: 2}, Title: "Ticket 2", CustomerID: 1},
	}

	suite.mockTicketRepo.On("GetByCustomerID", uint(1), 0, 10).Return(expectedTickets, nil)
	suite.mockTicketRepo.On("CountByCustomerID", uint(1)).Return(int64(2), nil)

	tickets, total, err := suite.service.GetByCustomer(1, 0, 10)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedTickets, tickets)
	assert.Equal(suite.T(), int64(2), total)
}

func (suite *TicketServiceTestSuite) TestGetByAssignee_Success() {
	expectedTickets := []models.Ticket{
		{BaseModel: models.BaseModel{ID: 1}, Title: "Ticket 1", AssignedToID: uintPtr(2)},
		{BaseModel: models.BaseModel{ID: 2}, Title: "Ticket 2", AssignedToID: uintPtr(2)},
	}

	suite.mockTicketRepo.On("GetByAssignedToID", uint(2), 0, 10).Return(expectedTickets, nil)
	suite.mockTicketRepo.On("CountByAssignedToID", uint(2)).Return(int64(2), nil)

	tickets, total, err := suite.service.GetByAssignee(2, 0, 10)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedTickets, tickets)
	assert.Equal(suite.T(), int64(2), total)
}

func (suite *TicketServiceTestSuite) TestUpdate_Success() {
	existingTicket := &models.Ticket{
		BaseModel:    models.BaseModel{ID: 1},
		Title:        "Old Title",
		Status:       models.TicketStatusOpen,
		AssignedToID: uintPtr(2),
	}

	updatedTicket := &models.Ticket{
		BaseModel:    models.BaseModel{ID: 1},
		Title:        "New Title",
		Status:       models.TicketStatusInProgress,
		AssignedToID: uintPtr(3),
	}

	newAssignee := &models.User{
		BaseModel: models.BaseModel{ID: 3},
		Role:      models.RoleAdmin,
	}

	suite.mockTicketRepo.On("GetByID", uint(1)).Return(existingTicket, nil)
	suite.mockUserRepo.On("GetByID", uint(3)).Return(newAssignee, nil)
	suite.mockTicketRepo.On("Update", updatedTicket).Return(nil)

	err := suite.service.Update(updatedTicket)
	assert.NoError(suite.T(), err)
}

func (suite *TicketServiceTestSuite) TestUpdate_CannotReopenClosed() {
	existingTicket := &models.Ticket{
		BaseModel: models.BaseModel{ID: 1},
		Status:    models.TicketStatusClosed,
	}

	updatedTicket := &models.Ticket{
		BaseModel: models.BaseModel{ID: 1},
		Status:    models.TicketStatusOpen,
	}

	suite.mockTicketRepo.On("GetByID", uint(1)).Return(existingTicket, nil)

	err := suite.service.Update(updatedTicket)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "cannot reopen closed ticket", err.Error())
}

func (suite *TicketServiceTestSuite) TestUpdate_InvalidAssigneeRole() {
	existingTicket := &models.Ticket{
		BaseModel:    models.BaseModel{ID: 1},
		Status:       models.TicketStatusOpen,
		AssignedToID: uintPtr(2),
	}

	updatedTicket := &models.Ticket{
		BaseModel:    models.BaseModel{ID: 1},
		Status:       models.TicketStatusOpen,
		AssignedToID: uintPtr(3),
	}

	newAssignee := &models.User{
		BaseModel: models.BaseModel{ID: 3},
		Role:      models.RoleCustomer, // Invalid role
	}

	suite.mockTicketRepo.On("GetByID", uint(1)).Return(existingTicket, nil)
	suite.mockUserRepo.On("GetByID", uint(3)).Return(newAssignee, nil)

	err := suite.service.Update(updatedTicket)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "tickets can only be assigned to support or admin users", err.Error())
}

func (suite *TicketServiceTestSuite) TestDelete_Success() {
	ticket := &models.Ticket{
		BaseModel: models.BaseModel{ID: 1},
		Title:     "Test Ticket",
	}

	suite.mockTicketRepo.On("GetByID", uint(1)).Return(ticket, nil)
	suite.mockTicketRepo.On("Delete", uint(1)).Return(nil)

	err := suite.service.Delete(1)
	assert.NoError(suite.T(), err)
}

func (suite *TicketServiceTestSuite) TestDelete_NotFound() {
	suite.mockTicketRepo.On("GetByID", uint(999)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Delete(999)
	assert.Error(suite.T(), err)
}

func (suite *TicketServiceTestSuite) TestList_Success() {
	expectedTickets := []models.Ticket{
		{BaseModel: models.BaseModel{ID: 1}, Title: "Ticket 1"},
		{BaseModel: models.BaseModel{ID: 2}, Title: "Ticket 2"},
	}

	suite.mockTicketRepo.On("List", 0, 10).Return(expectedTickets, nil)
	suite.mockTicketRepo.On("Count").Return(int64(2), nil)

	tickets, total, err := suite.service.List(0, 10)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedTickets, tickets)
	assert.Equal(suite.T(), int64(2), total)
}

// Helper function
func uintPtr(u uint) *uint {
	return &u
}

func TestTicketServiceTestSuite(t *testing.T) {
	suite.Run(t, new(TicketServiceTestSuite))
}