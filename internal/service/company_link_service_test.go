package service

import (
	"errors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"gorm.io/gorm"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
)

// The company link on leads and customers is checked against the live
// companies: an unknown id is apperrors.ErrCompanyNotFound, a nil id is no
// link, and the write never happens for a bad link.

func (suite *LeadServiceTestSuite) TestCreate_UnknownCompanyIsRejectedBeforeTheWrite() {
	companyID := uint(9)
	suite.mockCompanyRepo.On("GetByID", uint(9)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.leadService.Create(&models.Lead{FirstName: "J", LastName: "D", Email: "j@example.com", OwnerID: 1, CompanyID: &companyID})
	assert.True(suite.T(), errors.Is(err, apperrors.ErrCompanyNotFound), "got %v", err)
	suite.mockLeadRepo.AssertNotCalled(suite.T(), "Create", mock.Anything)
}

func (suite *LeadServiceTestSuite) TestCreate_KnownCompanyIsAccepted() {
	companyID := uint(9)
	suite.mockCompanyRepo.On("GetByID", uint(9)).Return(&models.Company{BaseModel: models.BaseModel{ID: 9}}, nil)
	suite.mockLeadRepo.On("Create", mock.MatchedBy(func(l *models.Lead) bool {
		return l.CompanyID != nil && *l.CompanyID == 9
	})).Return(nil)

	assert.NoError(suite.T(), suite.leadService.Create(&models.Lead{FirstName: "J", LastName: "D", Email: "j@example.com", OwnerID: 1, CompanyID: &companyID}))
}

func (suite *LeadServiceTestSuite) TestUpdate_CompanyLinkNilClearsValueSetsUnknownRejects() {
	linked := uint(4)
	existing := func() *models.Lead {
		id := linked
		return &models.Lead{BaseModel: models.BaseModel{ID: 1}, FirstName: "J", OwnerID: 1, CompanyID: &id}
	}

	// nil pointer under the key: cleared.
	suite.mockLeadRepo.On("GetByID", uint(1)).Return(existing(), nil).Once()
	suite.mockLeadRepo.On("Update", mock.MatchedBy(func(l *models.Lead) bool { return l.CompanyID == nil })).Return(nil).Once()
	updated, err := suite.leadService.Update(1, map[string]interface{}{"company_id": (*uint)(nil)})
	assert.NoError(suite.T(), err)
	assert.Nil(suite.T(), updated.CompanyID)

	// key absent: untouched, and the company repository is not consulted.
	suite.mockLeadRepo.On("GetByID", uint(1)).Return(existing(), nil).Once()
	suite.mockLeadRepo.On("Update", mock.MatchedBy(func(l *models.Lead) bool { return l.CompanyID != nil && *l.CompanyID == 4 })).Return(nil).Once()
	_, err = suite.leadService.Update(1, map[string]interface{}{"first_name": "Jane"})
	assert.NoError(suite.T(), err)

	// a value: checked, then set.
	nine := uint(9)
	suite.mockCompanyRepo.On("GetByID", uint(9)).Return(&models.Company{BaseModel: models.BaseModel{ID: 9}}, nil).Once()
	suite.mockLeadRepo.On("GetByID", uint(1)).Return(existing(), nil).Once()
	suite.mockLeadRepo.On("Update", mock.MatchedBy(func(l *models.Lead) bool { return l.CompanyID != nil && *l.CompanyID == 9 })).Return(nil).Once()
	_, err = suite.leadService.Update(1, map[string]interface{}{"company_id": &nine})
	assert.NoError(suite.T(), err)

	// unknown: rejected before the write.
	missing := uint(77)
	suite.mockCompanyRepo.On("GetByID", uint(77)).Return(nil, gorm.ErrRecordNotFound).Once()
	suite.mockLeadRepo.On("GetByID", uint(1)).Return(existing(), nil).Once()
	_, err = suite.leadService.Update(1, map[string]interface{}{"company_id": &missing})
	assert.True(suite.T(), errors.Is(err, apperrors.ErrCompanyNotFound), "got %v", err)
	suite.mockLeadRepo.AssertNumberOfCalls(suite.T(), "Update", 3)
}

func (suite *CustomerServiceTestSuite) TestCreate_UnknownCompanyIsRejectedBeforeTheWrite() {
	companyID := uint(9)
	customer := &models.Customer{FirstName: "J", LastName: "D", Email: "j@example.com", CompanyID: &companyID}
	suite.mockRepo.On("GetByEmailUnscoped", "j@example.com").Return(nil, gorm.ErrRecordNotFound)
	suite.mockCompany.On("GetByID", uint(9)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Create(customer)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrCompanyNotFound), "got %v", err)
	suite.mockRepo.AssertNotCalled(suite.T(), "Create", mock.Anything)
}

func (suite *CustomerServiceTestSuite) TestUpdate_CompanyLinkIsCheckedAndNilIsNoLink() {
	companyID := uint(9)
	customer := &models.Customer{BaseModel: models.BaseModel{ID: 1}, FirstName: "J", LastName: "D", Email: "j@example.com", CompanyID: &companyID}
	suite.mockRepo.On("GetByEmailUnscoped", "j@example.com").Return(nil, gorm.ErrRecordNotFound)

	suite.mockCompany.On("GetByID", uint(9)).Return(nil, gorm.ErrRecordNotFound).Once()
	err := suite.service.Update(customer)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrCompanyNotFound), "got %v", err)
	suite.mockRepo.AssertNotCalled(suite.T(), "Update", mock.Anything)

	suite.mockCompany.On("GetByID", uint(9)).Return(&models.Company{BaseModel: models.BaseModel{ID: 9}}, nil).Once()
	suite.mockRepo.On("Update", customer).Return(nil).Once()
	assert.NoError(suite.T(), suite.service.Update(customer))

	customer.CompanyID = nil
	suite.mockRepo.On("Update", customer).Return(nil).Once()
	assert.NoError(suite.T(), suite.service.Update(customer))
	suite.mockCompany.AssertNumberOfCalls(suite.T(), "GetByID", 2)
}
