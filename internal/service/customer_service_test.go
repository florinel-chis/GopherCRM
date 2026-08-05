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
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
)

type CustomerServiceTestSuite struct {
	suite.Suite
	mockRepo     *mocks.CustomerRepository
	mockUserRepo *mocks.UserRepository
	service      CustomerService
}

func (suite *CustomerServiceTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)
}

func (suite *CustomerServiceTestSuite) SetupTest() {
	suite.mockRepo = new(mocks.CustomerRepository)
	suite.mockUserRepo = new(mocks.UserRepository)
	suite.service = NewCustomerService(suite.mockRepo, suite.mockUserRepo)
}

func (suite *CustomerServiceTestSuite) TearDownTest() {
	suite.mockRepo.AssertExpectations(suite.T())
	suite.mockUserRepo.AssertExpectations(suite.T())
}

func (suite *CustomerServiceTestSuite) TestCreate_Success() {
	customer := &models.Customer{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Company:   "Acme Corp",
	}

	// Mock GetByEmailUnscoped to return not found (no duplicate)
	suite.mockRepo.On("GetByEmailUnscoped", "john@example.com").Return(nil, gorm.ErrRecordNotFound)
	suite.mockRepo.On("Create", customer).Return(nil).Run(func(args mock.Arguments) {
		// Simulate DB setting the ID
		c := args.Get(0).(*models.Customer)
		c.ID = 1
	})

	err := suite.service.Create(customer)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(1), customer.ID)
}

func (suite *CustomerServiceTestSuite) TestCreate_DuplicateEmail() {
	customer := &models.Customer{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}

	existingCustomer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "john@example.com",
	}

	// Mock GetByEmailUnscoped to return existing customer
	suite.mockRepo.On("GetByEmailUnscoped", "john@example.com").Return(existingCustomer, nil)

	err := suite.service.Create(customer)
	assert.Error(suite.T(), err)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrDuplicateEmail))
}

// TestCreate_SoftDeletedCustomerExists pins the duplicate pre-check to the
// *unscoped* lookup. The unique index on customers.email is not scoped to
// deleted_at, so a soft-deleted row still reserves the address; a scoped
// GetByEmail would report it free and let the insert reach the database, where
// it fails with a raw driver error. Create must reject it up front, without
// ever calling the repository's Create.
func (suite *CustomerServiceTestSuite) TestCreate_SoftDeletedCustomerExists() {
	softDeleted := &models.Customer{
		BaseModel: models.BaseModel{
			ID:        7,
			DeletedAt: gorm.DeletedAt{Time: time.Now(), Valid: true},
		},
		Email: "deleted@example.com",
	}

	suite.mockRepo.On("GetByEmailUnscoped", "deleted@example.com").Return(softDeleted, nil)

	err := suite.service.Create(&models.Customer{
		FirstName: "New",
		LastName:  "Customer",
		Email:     "deleted@example.com",
	})

	assert.Error(suite.T(), err)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrDuplicateEmail))
	suite.mockRepo.AssertNotCalled(suite.T(), "Create", mock.Anything)
}

func (suite *CustomerServiceTestSuite) TestCreate_RepoError() {
	customer := &models.Customer{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}

	suite.mockRepo.On("GetByEmailUnscoped", "john@example.com").Return(nil, gorm.ErrRecordNotFound)
	suite.mockRepo.On("Create", customer).Return(errors.New("database error"))

	err := suite.service.Create(customer)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "database error", err.Error())
}

func (suite *CustomerServiceTestSuite) TestGetByID_Success() {
	expectedCustomer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}

	suite.mockRepo.On("GetByID", uint(1)).Return(expectedCustomer, nil)

	customer, err := suite.service.GetByID(1)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedCustomer, customer)
}

// TestGetByID_NotFound pins the classification the caller depends on: a missing
// row must come back as the not-found sentinel, so a handler can tell it apart
// from a database failure and answer 404 rather than collapsing every error into
// one status.
func (suite *CustomerServiceTestSuite) TestGetByID_NotFound() {
	suite.mockRepo.On("GetByID", uint(1)).Return(nil, gorm.ErrRecordNotFound)

	customer, err := suite.service.GetByID(1)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), customer)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrNotFound),
		"a missing customer must satisfy errors.Is(err, apperrors.ErrNotFound), got %v", err)
}

// TestGetByID_RepoError is the other half of the same contract: anything that is
// not a missing row must NOT be dressed up as one.
func (suite *CustomerServiceTestSuite) TestGetByID_RepoError() {
	dbErr := errors.New("dial tcp 127.0.0.1:3306: connect: connection refused")
	suite.mockRepo.On("GetByID", uint(1)).Return(nil, dbErr)

	customer, err := suite.service.GetByID(1)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), customer)
	assert.False(suite.T(), errors.Is(err, apperrors.ErrNotFound),
		"a database failure must not be classified as not-found")
	assert.ErrorIs(suite.T(), err, dbErr)
}

func (suite *CustomerServiceTestSuite) TestUpdate_Success() {
	customer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john.doe@example.com",
		Company:   "Acme Corp",
	}

	// Mock GetByEmailUnscoped to check for duplicates
	suite.mockRepo.On("GetByEmailUnscoped", "john.doe@example.com").Return(nil, gorm.ErrRecordNotFound)
	suite.mockRepo.On("Update", customer).Return(nil)

	err := suite.service.Update(customer)
	assert.NoError(suite.T(), err)
}

func (suite *CustomerServiceTestSuite) TestUpdate_DuplicateEmail() {
	customer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}

	existingCustomer := &models.Customer{
		BaseModel: models.BaseModel{ID: 2}, // Different ID
		Email:     "john@example.com",
	}

	// Mock GetByEmailUnscoped to return existing customer with different ID
	suite.mockRepo.On("GetByEmailUnscoped", "john@example.com").Return(existingCustomer, nil)

	err := suite.service.Update(customer)
	assert.Error(suite.T(), err)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrDuplicateEmail))
}

func (suite *CustomerServiceTestSuite) TestUpdate_SameEmail() {
	customer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}

	existingCustomer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1}, // Same ID
		Email:     "john@example.com",
	}

	// Mock GetByEmailUnscoped to return existing customer with same ID (self)
	suite.mockRepo.On("GetByEmailUnscoped", "john@example.com").Return(existingCustomer, nil)
	suite.mockRepo.On("Update", customer).Return(nil)

	err := suite.service.Update(customer)
	assert.NoError(suite.T(), err)
}

func (suite *CustomerServiceTestSuite) TestDelete_Success() {
	customer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
	}

	suite.mockRepo.On("GetByID", uint(1)).Return(customer, nil)
	suite.mockRepo.On("Delete", uint(1)).Return(nil)

	err := suite.service.Delete(1)
	assert.NoError(suite.T(), err)
}

func (suite *CustomerServiceTestSuite) TestDelete_NotFound() {
	suite.mockRepo.On("GetByID", uint(1)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Delete(1)
	assert.Error(suite.T(), err)
}

func (suite *CustomerServiceTestSuite) TestList_Success() {
	expectedCustomers := []models.Customer{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", LastName: "Doe", Email: "john@example.com"},
		{BaseModel: models.BaseModel{ID: 2}, FirstName: "Jane", LastName: "Smith", Email: "jane@example.com"},
	}

	suite.mockRepo.On("List", 0, 10).Return(expectedCustomers, nil)
	suite.mockRepo.On("Count").Return(int64(2), nil)

	customers, total, err := suite.service.List(0, 10)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedCustomers, customers)
	assert.Equal(suite.T(), int64(2), total)
}

func (suite *CustomerServiceTestSuite) TestList_EmptyResult() {
	suite.mockRepo.On("List", 0, 10).Return([]models.Customer{}, nil)
	suite.mockRepo.On("Count").Return(int64(0), nil)

	customers, total, err := suite.service.List(0, 10)
	assert.NoError(suite.T(), err)
	assert.Empty(suite.T(), customers)
	assert.Equal(suite.T(), int64(0), total)
}

func (suite *CustomerServiceTestSuite) TestList_RepoError() {
	suite.mockRepo.On("List", 0, 10).Return(nil, errors.New("database error"))

	customers, total, err := suite.service.List(0, 10)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), customers)
	assert.Equal(suite.T(), int64(0), total)
}

func TestCustomerServiceTestSuite(t *testing.T) {
	suite.Run(t, new(CustomerServiceTestSuite))
}
// --- ExportAll ---------------------------------------------------------------

func (suite *CustomerServiceTestSuite) TestExportAll_Success() {
	expected := []models.Customer{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", LastName: "Doe", Email: "john@example.com"},
		{BaseModel: models.BaseModel{ID: 2}, FirstName: "Jane", LastName: "Smith", Email: "jane@example.com"},
	}

	suite.mockRepo.On("ListAllForExport", "", "", "").Return(expected, nil)

	customers, err := suite.service.ExportAll("", "", "")
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), customers, 2)
}

func (suite *CustomerServiceTestSuite) TestExportAll_PassesFiltersThrough() {
	suite.mockRepo.On("ListAllForExport", "acme", "email", "desc").Return([]models.Customer{}, nil)

	customers, err := suite.service.ExportAll("acme", "email", "desc")
	assert.NoError(suite.T(), err)
	assert.Empty(suite.T(), customers)
}

func (suite *CustomerServiceTestSuite) TestExportAll_RepoError() {
	suite.mockRepo.On("ListAllForExport", "", "", "").Return(nil, errors.New("database error"))

	customers, err := suite.service.ExportAll("", "", "")
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), customers)
}

// --- Assign ------------------------------------------------------------------

func (suite *CustomerServiceTestSuite) TestAssign_Success() {
	customer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}
	assignee := &models.User{
		BaseModel: models.BaseModel{ID: 7},
		Email:     "sales@example.com",
		Role:      models.RoleSales,
		IsActive:  true,
	}

	suite.mockRepo.On("GetByID", uint(1)).Return(customer, nil)
	suite.mockUserRepo.On("GetByID", uint(7)).Return(assignee, nil)
	suite.mockRepo.On("Update", mock.MatchedBy(func(c *models.Customer) bool {
		return c.ID == 1 && c.AssignedToID != nil && *c.AssignedToID == 7
	})).Return(nil)

	updated, err := suite.service.Assign(1, 7)
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), updated)
	assert.NotNil(suite.T(), updated.AssignedToID)
	assert.Equal(suite.T(), uint(7), *updated.AssignedToID)
}

func (suite *CustomerServiceTestSuite) TestAssign_AdminAssigneeAllowed() {
	customer := &models.Customer{BaseModel: models.BaseModel{ID: 1}, Email: "john@example.com"}
	assignee := &models.User{BaseModel: models.BaseModel{ID: 3}, Role: models.RoleAdmin, IsActive: true}

	suite.mockRepo.On("GetByID", uint(1)).Return(customer, nil)
	suite.mockUserRepo.On("GetByID", uint(3)).Return(assignee, nil)
	suite.mockRepo.On("Update", mock.Anything).Return(nil)

	updated, err := suite.service.Assign(1, 3)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(3), *updated.AssignedToID)
}

func (suite *CustomerServiceTestSuite) TestAssign_CustomerNotFound() {
	suite.mockRepo.On("GetByID", uint(1)).Return(nil, gorm.ErrRecordNotFound)

	updated, err := suite.service.Assign(1, 7)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), updated)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrNotFound))
}

// A failed lookup is not a missing customer, and must not be reported as one.
func (suite *CustomerServiceTestSuite) TestAssign_CustomerLookupFailureIsNotNotFound() {
	suite.mockRepo.On("GetByID", uint(1)).Return(nil, errors.New("connection refused"))

	updated, err := suite.service.Assign(1, 7)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), updated)
	assert.False(suite.T(), apperrors.IsNotFound(err))
}

func (suite *CustomerServiceTestSuite) TestAssign_AssigneeNotFound() {
	customer := &models.Customer{BaseModel: models.BaseModel{ID: 1}, Email: "john@example.com"}

	suite.mockRepo.On("GetByID", uint(1)).Return(customer, nil)
	suite.mockUserRepo.On("GetByID", uint(99)).Return(nil, gorm.ErrRecordNotFound)

	updated, err := suite.service.Assign(1, 99)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), updated)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrAssigneeNotFound))
}

func (suite *CustomerServiceTestSuite) TestAssign_InactiveAssigneeRejected() {
	customer := &models.Customer{BaseModel: models.BaseModel{ID: 1}, Email: "john@example.com"}
	assignee := &models.User{BaseModel: models.BaseModel{ID: 7}, Role: models.RoleSales, IsActive: false}

	suite.mockRepo.On("GetByID", uint(1)).Return(customer, nil)
	suite.mockUserRepo.On("GetByID", uint(7)).Return(assignee, nil)

	updated, err := suite.service.Assign(1, 7)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), updated)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrInactiveUser))
}

// Customer ownership is a sales responsibility: a support account has no
// business holding a book of customers, and a customer-role account holding one
// would be a data-protection incident waiting to happen.
func (suite *CustomerServiceTestSuite) TestAssign_CustomerRoleAssigneeRejected() {
	customer := &models.Customer{BaseModel: models.BaseModel{ID: 1}, Email: "john@example.com"}
	assignee := &models.User{BaseModel: models.BaseModel{ID: 7}, Role: models.RoleCustomer, IsActive: true}

	suite.mockRepo.On("GetByID", uint(1)).Return(customer, nil)
	suite.mockUserRepo.On("GetByID", uint(7)).Return(assignee, nil)

	updated, err := suite.service.Assign(1, 7)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), updated)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrInvalidCustomerAssignee))
}

func (suite *CustomerServiceTestSuite) TestAssign_SupportRoleAssigneeRejected() {
	customer := &models.Customer{BaseModel: models.BaseModel{ID: 1}, Email: "john@example.com"}
	assignee := &models.User{BaseModel: models.BaseModel{ID: 8}, Role: models.RoleSupport, IsActive: true}

	suite.mockRepo.On("GetByID", uint(1)).Return(customer, nil)
	suite.mockUserRepo.On("GetByID", uint(8)).Return(assignee, nil)

	updated, err := suite.service.Assign(1, 8)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), updated)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrInvalidCustomerAssignee))
}

func (suite *CustomerServiceTestSuite) TestAssign_PersistFailureIsReported() {
	customer := &models.Customer{BaseModel: models.BaseModel{ID: 1}, Email: "john@example.com"}
	assignee := &models.User{BaseModel: models.BaseModel{ID: 7}, Role: models.RoleSales, IsActive: true}

	suite.mockRepo.On("GetByID", uint(1)).Return(customer, nil)
	suite.mockUserRepo.On("GetByID", uint(7)).Return(assignee, nil)
	suite.mockRepo.On("Update", mock.Anything).Return(errors.New("database error"))

	updated, err := suite.service.Assign(1, 7)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), updated)
}
