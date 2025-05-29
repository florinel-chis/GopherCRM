package service

import (
	"errors"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

type LeadServiceTestSuite struct {
	suite.Suite
	mockLeadRepo     *mocks.LeadRepository
	mockCustomerRepo *mocks.CustomerRepository
	leadService      LeadService
}

func (suite *LeadServiceTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)
}

func (suite *LeadServiceTestSuite) SetupTest() {
	suite.mockLeadRepo = new(mocks.LeadRepository)
	suite.mockCustomerRepo = new(mocks.CustomerRepository)
	suite.leadService = NewLeadService(suite.mockLeadRepo, suite.mockCustomerRepo)
}

func (suite *LeadServiceTestSuite) TearDownTest() {
	suite.mockLeadRepo.AssertExpectations(suite.T())
	suite.mockCustomerRepo.AssertExpectations(suite.T())
}

func (suite *LeadServiceTestSuite) TestCreate_Success() {
	lead := &models.Lead{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		OwnerID:   1,
	}

	suite.mockLeadRepo.On("Create", mock.MatchedBy(func(l *models.Lead) bool {
		return l.FirstName == "John" && 
			l.LastName == "Doe" && 
			l.Email == "john@example.com" &&
			l.Status == models.LeadStatusNew
	})).Return(nil).Run(func(args mock.Arguments) {
		// Simulate database setting ID
		l := args.Get(0).(*models.Lead)
		l.ID = 1
	})

	err := suite.leadService.Create(lead)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.LeadStatusNew, lead.Status)
	assert.Equal(suite.T(), uint(1), lead.ID)
}

func (suite *LeadServiceTestSuite) TestCreate_WithCustomStatus() {
	lead := &models.Lead{
		FirstName: "Jane",
		LastName:  "Smith",
		Email:     "jane@example.com",
		Status:    models.LeadStatusContacted,
		OwnerID:   1,
	}

	suite.mockLeadRepo.On("Create", mock.MatchedBy(func(l *models.Lead) bool {
		return l.Status == models.LeadStatusContacted
	})).Return(nil)

	err := suite.leadService.Create(lead)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.LeadStatusContacted, lead.Status)
}

func (suite *LeadServiceTestSuite) TestCreate_Error() {
	lead := &models.Lead{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		OwnerID:   1,
	}

	suite.mockLeadRepo.On("Create", mock.Anything).Return(errors.New("database error"))

	err := suite.leadService.Create(lead)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "database error", err.Error())
}

func (suite *LeadServiceTestSuite) TestGetByID_Success() {
	expectedLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Status:    models.LeadStatusNew,
	}

	suite.mockLeadRepo.On("GetByID", uint(1)).Return(expectedLead, nil)

	lead, err := suite.leadService.GetByID(1)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedLead, lead)
}

func (suite *LeadServiceTestSuite) TestGetByID_NotFound() {
	suite.mockLeadRepo.On("GetByID", uint(999)).Return(nil, errors.New("record not found"))

	lead, err := suite.leadService.GetByID(999)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), lead)
}

func (suite *LeadServiceTestSuite) TestUpdate_Success() {
	existingLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Status:    models.LeadStatusNew,
		OwnerID:   1,
	}

	updates := map[string]interface{}{
		"first_name": "Jane",
		"status":     models.LeadStatusContacted,
		"notes":      "Follow up needed",
	}

	suite.mockLeadRepo.On("GetByID", uint(1)).Return(existingLead, nil)
	suite.mockLeadRepo.On("Update", mock.MatchedBy(func(l *models.Lead) bool {
		return l.FirstName == "Jane" && 
			l.Status == models.LeadStatusContacted &&
			l.Notes == "Follow up needed"
	})).Return(nil)

	updatedLead, err := suite.leadService.Update(1, updates)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "Jane", updatedLead.FirstName)
	assert.Equal(suite.T(), models.LeadStatusContacted, updatedLead.Status)
	assert.Equal(suite.T(), "Follow up needed", updatedLead.Notes)
}

func (suite *LeadServiceTestSuite) TestUpdate_LeadNotFound() {
	updates := map[string]interface{}{
		"first_name": "Jane",
	}

	suite.mockLeadRepo.On("GetByID", uint(999)).Return(nil, errors.New("record not found"))

	updatedLead, err := suite.leadService.Update(999, updates)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), updatedLead)
}

func (suite *LeadServiceTestSuite) TestDelete_Success() {
	suite.mockLeadRepo.On("Delete", uint(1)).Return(nil)

	err := suite.leadService.Delete(1)
	assert.NoError(suite.T(), err)
}

func (suite *LeadServiceTestSuite) TestList_Success() {
	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", Email: "john@example.com"},
		{BaseModel: models.BaseModel{ID: 2}, FirstName: "Jane", Email: "jane@example.com"},
	}

	suite.mockLeadRepo.On("List", 0, 10).Return(expectedLeads, nil)
	suite.mockLeadRepo.On("Count").Return(int64(2), nil)

	leads, total, err := suite.leadService.List(0, 10)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedLeads, leads)
	assert.Equal(suite.T(), int64(2), total)
}

func (suite *LeadServiceTestSuite) TestConvertToCustomer_Success() {
	lead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Phone:     "+1234567890",
		Company:   "Acme Inc",
		Status:    models.LeadStatusQualified,
	}

	customerData := &models.Customer{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Company:   "Acme Corp",
	}

	suite.mockLeadRepo.On("GetByID", uint(1)).Return(lead, nil)
	suite.mockCustomerRepo.On("Create", mock.MatchedBy(func(c *models.Customer) bool {
		return c.FirstName == "John" && 
			c.LastName == "Doe" && 
			c.Email == "john@example.com" &&
			c.Phone == "+1234567890" && // Should inherit from lead
			c.Company == "Acme Corp" // Should use provided value
	})).Return(nil).Run(func(args mock.Arguments) {
		// Simulate database setting ID
		c := args.Get(0).(*models.Customer)
		c.ID = 1
	})
	suite.mockLeadRepo.On("ConvertToCustomer", uint(1), uint(1)).Return(nil)

	customer, err := suite.leadService.ConvertToCustomer(1, customerData)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "John", customer.FirstName)
	assert.Equal(suite.T(), "+1234567890", customer.Phone)
	assert.Equal(suite.T(), "Acme Corp", customer.Company)
}

func (suite *LeadServiceTestSuite) TestConvertToCustomer_LeadNotFound() {
	customerData := &models.Customer{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}

	suite.mockLeadRepo.On("GetByID", uint(999)).Return(nil, errors.New("record not found"))

	customer, err := suite.leadService.ConvertToCustomer(999, customerData)
	assert.Error(suite.T(), err)
	assert.Nil(suite.T(), customer)
}

func (suite *LeadServiceTestSuite) TestConvertToCustomer_AlreadyConverted() {
	lead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Status:    models.LeadStatusConverted,
	}

	customerData := &models.Customer{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}

	suite.mockLeadRepo.On("GetByID", uint(1)).Return(lead, nil)

	customer, err := suite.leadService.ConvertToCustomer(1, customerData)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "lead already converted", err.Error())
	assert.Nil(suite.T(), customer)
}

func (suite *LeadServiceTestSuite) TestConvertToCustomer_CustomerCreationFails() {
	lead := &models.Lead{
		BaseModel: models.BaseModel{ID: 1},
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
		Status:    models.LeadStatusQualified,
	}

	customerData := &models.Customer{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john@example.com",
	}

	suite.mockLeadRepo.On("GetByID", uint(1)).Return(lead, nil)
	suite.mockCustomerRepo.On("Create", mock.Anything).Return(errors.New("email already exists"))

	customer, err := suite.leadService.ConvertToCustomer(1, customerData)
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), "email already exists", err.Error())
	assert.Nil(suite.T(), customer)
}

func (suite *LeadServiceTestSuite) TestGetByOwner_Success() {
	expectedLeads := []models.Lead{
		{BaseModel: models.BaseModel{ID: 1}, FirstName: "John", OwnerID: 1},
		{BaseModel: models.BaseModel{ID: 2}, FirstName: "Jane", OwnerID: 1},
	}

	suite.mockLeadRepo.On("GetByOwnerID", uint(1), 0, 10).Return(expectedLeads, nil)

	leads, err := suite.leadService.GetByOwner(1, 0, 10)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expectedLeads, leads)
}

func TestLeadServiceTestSuite(t *testing.T) {
	suite.Run(t, new(LeadServiceTestSuite))
}