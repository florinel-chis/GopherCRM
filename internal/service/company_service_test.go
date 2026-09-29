package service

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type CompanyServiceTestSuite struct {
	suite.Suite
	mockRepo     *mocks.CompanyRepository
	mockUserRepo *mocks.UserRepository
	service      CompanyService
}

func (suite *CompanyServiceTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
}

func (suite *CompanyServiceTestSuite) SetupTest() {
	suite.mockRepo = new(mocks.CompanyRepository)
	suite.mockUserRepo = new(mocks.UserRepository)
	// The mock-based tests never reach the transaction; Delete has its own
	// tests below on a real database.
	suite.service = NewCompanyService(suite.mockRepo, suite.mockUserRepo, nil)
}

func (suite *CompanyServiceTestSuite) TearDownTest() {
	suite.mockRepo.AssertExpectations(suite.T())
	suite.mockUserRepo.AssertExpectations(suite.T())
}

func (suite *CompanyServiceTestSuite) TestCreate_TrimsNormalisesAndChecksTheDomainBeforeWriting() {
	company := &models.Company{Name: "  Acme GmbH ", Domain: "https://WWW.Acme.Example/about?x=1", Website: "https://acme.example"}

	suite.mockRepo.On("ExistsByDomain", "acme.example", uint(0)).Return(false, nil)
	suite.mockRepo.On("Create", mock.MatchedBy(func(c *models.Company) bool {
		return c.Name == "Acme GmbH" && c.Domain == "acme.example"
	})).Return(nil).Run(func(args mock.Arguments) {
		args.Get(0).(*models.Company).ID = 3
	})

	require.NoError(suite.T(), suite.service.Create(company))
	assert.Equal(suite.T(), uint(3), company.ID)
}

func (suite *CompanyServiceTestSuite) TestCreate_DuplicateDomainIsRefusedBeforeTheWrite() {
	suite.mockRepo.On("ExistsByDomain", "acme.example", uint(0)).Return(true, nil)

	err := suite.service.Create(&models.Company{Name: "Acme", Domain: "Acme.Example"})
	assert.True(suite.T(), errors.Is(err, apperrors.ErrDuplicateCompanyDomain), "got %v", err)
	suite.mockRepo.AssertNotCalled(suite.T(), "Create", mock.Anything)
}

func (suite *CompanyServiceTestSuite) TestCreate_WithoutDomainSkipsTheDuplicateCheck() {
	suite.mockRepo.On("Create", mock.Anything).Return(nil)

	require.NoError(suite.T(), suite.service.Create(&models.Company{Name: "Acme"}))
	suite.mockRepo.AssertNotCalled(suite.T(), "ExistsByDomain", mock.Anything, mock.Anything)
}

func (suite *CompanyServiceTestSuite) TestCreate_OwnerMustBeALiveUser() {
	owner := uint(7)
	suite.mockUserRepo.On("GetByID", uint(7)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.Create(&models.Company{Name: "Acme", OwnerID: &owner})
	assert.True(suite.T(), errors.Is(err, apperrors.ErrAssigneeNotFound), "got %v", err)
	suite.mockRepo.AssertNotCalled(suite.T(), "Create", mock.Anything)

	// A lookup failure is not a missing user.
	suite.mockUserRepo.On("GetByID", uint(8)).Return(nil, errors.New("db down"))
	other := uint(8)
	err = suite.service.Create(&models.Company{Name: "Acme", OwnerID: &other})
	assert.Error(suite.T(), err)
	assert.False(suite.T(), errors.Is(err, apperrors.ErrAssigneeNotFound))
}

func (suite *CompanyServiceTestSuite) TestCreate_ValidationFailures() {
	for name, company := range map[string]*models.Company{
		"blank name":        {Name: "   "},
		"ftp website":       {Name: "Acme", Website: "ftp://acme.example"},
		"website no scheme": {Name: "Acme", Website: "acme.example"},
		"domain with space": {Name: "Acme", Domain: "acme example.com"},
		"domain only www":   {Name: "Acme", Domain: "https://www./"},
	} {
		err := suite.service.Create(company)
		assert.Truef(suite.T(), errors.Is(err, apperrors.ErrValidation), "%s: got %v", name, err)
	}
	suite.mockRepo.AssertNotCalled(suite.T(), "Create", mock.Anything)
}

// The company being updated may keep its own domain: the pre-check excludes
// its id, so a rename that leaves the domain alone is not a duplicate.
func (suite *CompanyServiceTestSuite) TestUpdate_ExcludesItselfFromTheDuplicateCheck() {
	company := &models.Company{BaseModel: models.BaseModel{ID: 5}, Name: "Acme Renamed", Domain: "acme.example"}
	suite.mockRepo.On("ExistsByDomain", "acme.example", uint(5)).Return(false, nil)
	suite.mockRepo.On("Update", company).Return(nil)

	require.NoError(suite.T(), suite.service.Update(company))
}

func (suite *CompanyServiceTestSuite) TestUpdate_DuplicateDomainIs409Sentinel() {
	company := &models.Company{BaseModel: models.BaseModel{ID: 5}, Name: "Acme", Domain: "taken.example"}
	suite.mockRepo.On("ExistsByDomain", "taken.example", uint(5)).Return(true, nil)

	err := suite.service.Update(company)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrDuplicateCompanyDomain), "got %v", err)
	suite.mockRepo.AssertNotCalled(suite.T(), "Update", mock.Anything)
}

func (suite *CompanyServiceTestSuite) TestGetByID_MissWrapsTheNotFoundSentinel() {
	suite.mockRepo.On("GetByID", uint(404)).Return(nil, gorm.ErrRecordNotFound)
	_, err := suite.service.GetByID(404)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrNotFound))

	suite.mockRepo.On("GetByID", uint(500)).Return(nil, errors.New("db down"))
	_, err = suite.service.GetByID(500)
	assert.Error(suite.T(), err)
	assert.False(suite.T(), apperrors.IsNotFound(err), "a lookup failure must not read as a miss")
}

func (suite *CompanyServiceTestSuite) TestListLeads_UnknownCompanyIsNotFound() {
	suite.mockRepo.On("GetByID", uint(404)).Return(nil, gorm.ErrRecordNotFound)
	_, _, err := suite.service.ListLeads(404, nil, 0, 20)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrNotFound))
	suite.mockRepo.AssertNotCalled(suite.T(), "ListLeads", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestCompanyServiceTestSuite(t *testing.T) {
	suite.Run(t, new(CompanyServiceTestSuite))
}

// --- Domain normalisation ----------------------------------------------------

func TestNormalizeCompanyDomain(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"acme.example", "acme.example"},
		{"ACME.Example", "acme.example"},
		{"www.acme.example", "acme.example"},
		{"WWW.ACME.EXAMPLE", "acme.example"},
		{"https://acme.example", "acme.example"},
		{"http://www.acme.example/", "acme.example"},
		{"https://www.acme.example/about/team?utm=1#top", "acme.example"},
		{"acme.example/contact", "acme.example"},
		{"acme.example.", "acme.example"},
		{" https://Shop.Acme.Example ", "shop.acme.example"},
		{"münchen.example", "münchen.example"},
		{"acme-corp.co.uk", "acme-corp.co.uk"},
	} {
		got, err := NormalizeCompanyDomain(tc.in)
		require.NoErrorf(t, err, "%q", tc.in)
		assert.Equalf(t, tc.want, got, "%q", tc.in)
	}

	for _, in := range []string{
		"https://",
		"www.",
		"acme example.com",
		"user@acme.example",
		"acme.example:8080",
		"https://acme.example:8443/x",
		"acme_corp.example",
	} {
		_, err := NormalizeCompanyDomain(in)
		assert.Truef(t, errors.Is(err, apperrors.ErrValidation), "%q must be rejected, got %v", in, err)
	}
}

// --- Delete on a real database -----------------------------------------------

func setupCompanyServiceDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.Migrator().DropTable(&models.Lead{}, &models.Customer{}, &models.Company{}, &models.User{}))
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Lead{}, &models.Customer{}))
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func seedLinkedCompany(t *testing.T, db *gorm.DB) (*models.Company, *models.Lead, *models.Customer) {
	t.Helper()
	owner := &models.User{Email: "owner@example.com", Password: "x", FirstName: "O", LastName: "W", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(owner).Error)
	company := &models.Company{Name: "Acme", Domain: "acme.example"}
	require.NoError(t, db.Create(company).Error)
	lead := &models.Lead{FirstName: "L", LastName: "One", Email: "lead@example.com", OwnerID: owner.ID, CompanyID: &company.ID}
	require.NoError(t, db.Create(lead).Error)
	customer := &models.Customer{FirstName: "C", LastName: "One", Email: "customer@example.com", CompanyID: &company.ID}
	require.NoError(t, db.Create(customer).Error)
	return company, lead, customer
}

func TestCompanyServiceDelete_UnlinksLeadsAndCustomersThenSoftDeletes(t *testing.T) {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	db := setupCompanyServiceDB(t)
	company, lead, customer := seedLinkedCompany(t, db)
	svc := NewCompanyService(repository.NewCompanyRepository(db), repository.NewUserRepository(db), utils.NewTransactionManager(db))

	require.NoError(t, svc.Delete(company.ID))

	var gone models.Company
	assert.True(t, errors.Is(db.First(&gone, company.ID).Error, gorm.ErrRecordNotFound), "the company is hidden")
	require.NoError(t, db.Unscoped().First(&gone, company.ID).Error)
	assert.True(t, gone.DeletedAt.Valid, "soft-deleted, not destroyed")

	var reloadedLead models.Lead
	require.NoError(t, db.First(&reloadedLead, lead.ID).Error)
	assert.Nil(t, reloadedLead.CompanyID, "the lead no longer points at the deleted company")
	var reloadedCustomer models.Customer
	require.NoError(t, db.First(&reloadedCustomer, customer.ID).Error)
	assert.Nil(t, reloadedCustomer.CompanyID, "the customer no longer points at the deleted company")

	// A second delete is a miss, not a success.
	assert.True(t, errors.Is(svc.Delete(company.ID), apperrors.ErrNotFound))
}

// failingDeleteRepo is the real repository with the final step sabotaged, so
// the unlinks run for real on the transaction and the delete then fails.
type failingDeleteRepo struct {
	repository.CompanyRepository
}

func (r failingDeleteRepo) Delete(uint) error { return errors.New("disk full") }
func (r failingDeleteRepo) WithTx(tx *gorm.DB) repository.CompanyRepository {
	return failingDeleteRepo{r.CompanyRepository.WithTx(tx)}
}

func TestCompanyServiceDelete_RollsBackTheUnlinksWhenTheDeleteFails(t *testing.T) {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	db := setupCompanyServiceDB(t)
	company, lead, customer := seedLinkedCompany(t, db)
	svc := NewCompanyService(failingDeleteRepo{repository.NewCompanyRepository(db)}, repository.NewUserRepository(db), utils.NewTransactionManager(db))

	err := svc.Delete(company.ID)
	require.Error(t, err)
	assert.False(t, apperrors.IsNotFound(err), "a failed delete must not read as a miss")

	var reloadedLead models.Lead
	require.NoError(t, db.First(&reloadedLead, lead.ID).Error)
	require.NotNil(t, reloadedLead.CompanyID, "the unlink was rolled back with the failed delete")
	assert.Equal(t, company.ID, *reloadedLead.CompanyID)
	var reloadedCustomer models.Customer
	require.NoError(t, db.First(&reloadedCustomer, customer.ID).Error)
	require.NotNil(t, reloadedCustomer.CompanyID)
	assert.Equal(t, company.ID, *reloadedCustomer.CompanyID)

	var still models.Company
	assert.NoError(t, db.First(&still, company.ID).Error, "the company is still there")
}
