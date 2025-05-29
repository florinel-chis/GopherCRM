package service

import (
	"errors"
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

type CustomerServiceTestSuite struct {
	suite.Suite
	mockRepo *mocks.CustomerRepository
	service  CustomerService
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
	suite.service = NewCustomerService(suite.mockRepo, nil)
}

func (suite *CustomerServiceTestSuite) TearDownTest() {
	suite.mockRepo.AssertExpectations(suite.T())
}

func (suite *CustomerServiceTestSuite) TestCreate_Success() {
	customer := &models.Customer{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Company:   "Acme Corp",
	}

	// Mock GetByEmail to return not found (no duplicate)
	suite.mockRepo.On("GetByEmail", "john@example.com").Return(nil, gorm.ErrRecordNotFound)
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

	// Mock GetByEmail to return existing customer
	suite.mockRepo.On("GetByEmail", "john@example.com").Return(existingCustomer, nil)

	err := suite.service.Create(customer)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "customer with this email already exists", err.Error())
}

func (suite *CustomerServiceTestSuite) TestCreate_RepoError() {
	customer := &models.Customer{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}

	suite.mockRepo.On("GetByEmail", "john@example.com").Return(nil, gorm.ErrRecordNotFound)
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

func (suite *CustomerServiceTestSuite) TestGetByID_NotFound() {
	suite.mockRepo.On("GetByID", uint(1)).Return(nil, gorm.ErrRecordNotFound)

	customer, err := suite.service.GetByID(1)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), customer)
}

func (suite *CustomerServiceTestSuite) TestUpdate_Success() {
	customer := &models.Customer{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john.doe@example.com",
		Company:   "Acme Corp",
	}

	// Mock GetByEmail to check for duplicates
	suite.mockRepo.On("GetByEmail", "john.doe@example.com").Return(nil, gorm.ErrRecordNotFound)
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

	// Mock GetByEmail to return existing customer with different ID
	suite.mockRepo.On("GetByEmail", "john@example.com").Return(existingCustomer, nil)

	err := suite.service.Update(customer)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "customer with this email already exists", err.Error())
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

	// Mock GetByEmail to return existing customer with same ID (self)
	suite.mockRepo.On("GetByEmail", "john@example.com").Return(existingCustomer, nil)
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