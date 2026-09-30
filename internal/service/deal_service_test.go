package service

import (
	"errors"
	"strings"
	"testing"
	"time"

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

// stubCurrency is the configuration read the service makes, with the answer
// the test wants.
type stubCurrency struct {
	value string
	err   error
	calls int
}

func (s *stubCurrency) GetString(string) (string, error) {
	s.calls++
	return s.value, s.err
}

// --- Validation and link checks, on mocks -------------------------------------

type DealServiceTestSuite struct {
	suite.Suite
	mockRepo     *mocks.DealRepository
	mockCompany  *mocks.CompanyRepository
	mockCustomer *mocks.CustomerRepository
	mockLead     *mocks.LeadRepository
	mockUser     *mocks.UserRepository
	currency     *stubCurrency
	service      DealService
}

func (suite *DealServiceTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
}

func (suite *DealServiceTestSuite) SetupTest() {
	suite.mockRepo = new(mocks.DealRepository)
	suite.mockCompany = new(mocks.CompanyRepository)
	suite.mockCustomer = new(mocks.CustomerRepository)
	suite.mockLead = new(mocks.LeadRepository)
	suite.mockUser = new(mocks.UserRepository)
	suite.currency = &stubCurrency{value: "USD"}
	// The mock-based tests stop before the transaction; the writes have their
	// own tests below on a real database.
	suite.service = NewDealService(suite.mockRepo, suite.mockCompany, suite.mockCustomer, suite.mockLead, suite.mockUser, suite.currency, nil)
}

func (suite *DealServiceTestSuite) TearDownTest() {
	suite.mockRepo.AssertExpectations(suite.T())
	suite.mockCompany.AssertExpectations(suite.T())
	suite.mockCustomer.AssertExpectations(suite.T())
	suite.mockLead.AssertExpectations(suite.T())
	suite.mockUser.AssertExpectations(suite.T())
}

func (suite *DealServiceTestSuite) ownerExists() {
	suite.mockUser.On("GetByID", uint(1)).Return(&models.User{BaseModel: models.BaseModel{ID: 1}}, nil).Maybe()
}

func (suite *DealServiceTestSuite) TestCreate_ValidationFailuresNeverReachTheWrite() {
	suite.ownerExists()
	for name, deal := range map[string]*models.Deal{
		"blank title":          {Title: "   ", Currency: "EUR", OwnerID: 1},
		"title too long":       {Title: strings.Repeat("x", models.DealTitleMaxLength+1), Currency: "EUR", OwnerID: 1},
		"negative amount":      {Title: "X", Currency: "EUR", AmountCents: -1, OwnerID: 1},
		"lowercase currency":   {Title: "X", Currency: "eur", OwnerID: 1},
		"two-letter currency":  {Title: "X", Currency: "EU", OwnerID: 1},
		"four-letter currency": {Title: "X", Currency: "EURO", OwnerID: 1},
		"unknown stage":        {Title: "X", Currency: "EUR", Stage: "closed", OwnerID: 1},
		"no owner":             {Title: "X", Currency: "EUR"},
	} {
		err := suite.service.Create(deal, nil, 1)
		assert.Truef(suite.T(), errors.Is(err, apperrors.ErrValidation), "%s: got %v", name, err)
	}

	over := 101
	err := suite.service.Create(&models.Deal{Title: "X", Currency: "EUR", OwnerID: 1}, &over, 1)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrValidation), "probability 101: got %v", err)
	under := -1
	err = suite.service.Create(&models.Deal{Title: "X", Currency: "EUR", OwnerID: 1}, &under, 1)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrValidation), "probability -1: got %v", err)

	suite.mockRepo.AssertNotCalled(suite.T(), "Create", mock.Anything)
	suite.mockRepo.AssertNotCalled(suite.T(), "WithTx", mock.Anything)
}

// Each foreign key is checked against the live rows and answered with its own
// sentinel, and a lookup failure is not a missing row.
func (suite *DealServiceTestSuite) TestCreate_EachLinkMustBeALiveRow() {
	nine := uint(9)

	suite.mockUser.On("GetByID", uint(9)).Return(nil, gorm.ErrRecordNotFound).Once()
	err := suite.service.Create(&models.Deal{Title: "X", Currency: "EUR", OwnerID: 9}, nil, 1)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrAssigneeNotFound), "owner: got %v", err)

	suite.ownerExists()
	suite.mockCompany.On("GetByID", uint(9)).Return(nil, gorm.ErrRecordNotFound).Once()
	err = suite.service.Create(&models.Deal{Title: "X", Currency: "EUR", OwnerID: 1, CompanyID: &nine}, nil, 1)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrCompanyNotFound), "company: got %v", err)

	suite.mockCustomer.On("GetByID", uint(9)).Return(nil, gorm.ErrRecordNotFound).Once()
	err = suite.service.Create(&models.Deal{Title: "X", Currency: "EUR", OwnerID: 1, CustomerID: &nine}, nil, 1)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrCustomerNotFound), "customer: got %v", err)

	suite.mockLead.On("GetByID", uint(9)).Return(nil, gorm.ErrRecordNotFound).Once()
	err = suite.service.Create(&models.Deal{Title: "X", Currency: "EUR", OwnerID: 1, LeadID: &nine}, nil, 1)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrLeadNotFound), "lead: got %v", err)

	suite.mockCompany.On("GetByID", uint(9)).Return(nil, errors.New("db down")).Once()
	err = suite.service.Create(&models.Deal{Title: "X", Currency: "EUR", OwnerID: 1, CompanyID: &nine}, nil, 1)
	assert.Error(suite.T(), err)
	assert.False(suite.T(), errors.Is(err, apperrors.ErrCompanyNotFound), "a lookup failure must not read as a missing company")

	suite.mockRepo.AssertNotCalled(suite.T(), "WithTx", mock.Anything)
}

func (suite *DealServiceTestSuite) TestGetByID_MissWrapsTheNotFoundSentinel() {
	suite.mockRepo.On("GetByID", uint(404)).Return(nil, gorm.ErrRecordNotFound)
	_, err := suite.service.GetByID(404)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrNotFound))

	suite.mockRepo.On("GetByID", uint(500)).Return(nil, errors.New("db down"))
	_, err = suite.service.GetByID(500)
	assert.Error(suite.T(), err)
	assert.False(suite.T(), apperrors.IsNotFound(err), "a lookup failure must not read as a miss")
}

func (suite *DealServiceTestSuite) TestChangeStage_RejectsBadInputBeforeAnyRead() {
	_, err := suite.service.ChangeStage(1, "closed", nil, nil, 1)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrValidation), "got %v", err)
	over := 101
	_, err = suite.service.ChangeStage(1, models.DealStageProposal, &over, nil, 1)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrValidation), "got %v", err)
	suite.mockRepo.AssertNotCalled(suite.T(), "GetByID", mock.Anything)
}

func (suite *DealServiceTestSuite) TestChangeStage_SameStageWritesNothing() {
	deal := &models.Deal{BaseModel: models.BaseModel{ID: 5}, Title: "X", Stage: models.DealStageProposal, Probability: 40, Currency: "EUR", OwnerID: 1}
	suite.mockRepo.On("GetByID", uint(5)).Return(deal, nil).Once()

	explicit := 55
	got, err := suite.service.ChangeStage(5, models.DealStageProposal, &explicit, nil, 1)
	require.NoError(suite.T(), err)
	assert.Equal(suite.T(), 40, got.Probability, "nothing changes, not even an explicit probability")
	suite.mockRepo.AssertNotCalled(suite.T(), "WithTx", mock.Anything)
	suite.mockRepo.AssertNotCalled(suite.T(), "Update", mock.Anything)
	suite.mockRepo.AssertNotCalled(suite.T(), "CreateStageChange", mock.Anything)
}

func (suite *DealServiceTestSuite) TestListByCompany_UnknownCompanyIsNotFound() {
	suite.mockCompany.On("GetByID", uint(404)).Return(nil, gorm.ErrRecordNotFound)
	_, _, err := suite.service.ListByCompany(404, nil, 0, 20)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrNotFound))
	suite.mockRepo.AssertNotCalled(suite.T(), "List", mock.Anything, mock.Anything, mock.Anything)

	suite.mockCustomer.On("GetByID", uint(404)).Return(nil, gorm.ErrRecordNotFound)
	_, _, err = suite.service.ListByCustomer(404, nil, 0, 20)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrNotFound))
}

func (suite *DealServiceTestSuite) TestListByCompany_ScopesTheFilterToTheCompanyAndOwner() {
	owner := uint(7)
	suite.mockCompany.On("GetByID", uint(3)).Return(&models.Company{BaseModel: models.BaseModel{ID: 3}}, nil)
	suite.mockRepo.On("List", 0, 20, mock.MatchedBy(func(f repository.DealListFilter) bool {
		return f.CompanyID != nil && *f.CompanyID == 3 && f.OwnerID != nil && *f.OwnerID == 7 && f.CustomerID == nil && !f.Open
	})).Return([]models.Deal{}, int64(0), nil)

	_, _, err := suite.service.ListByCompany(3, &owner, 0, 20)
	require.NoError(suite.T(), err)
}

func TestDealServiceTestSuite(t *testing.T) {
	suite.Run(t, new(DealServiceTestSuite))
}

// --- The transition table -----------------------------------------------------

// applyDealTransition is the one place the stage rules live; every entry point
// (create, update, the stage endpoint) goes through it.
func TestApplyDealTransition(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	earlier := now.Add(-time.Hour)
	intOf := func(v int) *int { return &v }

	for _, tc := range []struct {
		name        string
		before      models.Deal
		to          models.DealStage
		probability *int
		lostReason  string
		wantProb    int
		wantClosed  *time.Time
		wantReason  string
	}{
		{"open to open takes the default", models.Deal{Stage: models.DealStageQualification, Probability: 10}, models.DealStageProposal, nil, "", 40, nil, ""},
		{"open to open keeps an explicit probability", models.Deal{Stage: models.DealStageQualification, Probability: 10}, models.DealStageNegotiation, intOf(55), "", 55, nil, ""},
		{"won is always 100 and sets closed_at", models.Deal{Stage: models.DealStageNegotiation, Probability: 70}, models.DealStageWon, intOf(30), "ignored", 100, &now, ""},
		{"lost is always 0, sets closed_at and keeps the reason", models.Deal{Stage: models.DealStageProposal, Probability: 40}, models.DealStageLost, intOf(30), "  no budget ", 0, &now, "no budget"},
		{"lost without a reason is stored empty", models.Deal{Stage: models.DealStageProposal, Probability: 40}, models.DealStageLost, nil, "", 0, &now, ""},
		{"back to open clears closed_at and the reason", models.Deal{Stage: models.DealStageLost, Probability: 0, ClosedAt: &earlier, LostReason: "no budget"}, models.DealStageNegotiation, nil, "no budget", 70, nil, ""},
		{"won to lost re-stamps closed_at and takes the reason", models.Deal{Stage: models.DealStageWon, Probability: 100, ClosedAt: &earlier}, models.DealStageLost, nil, "churned", 0, &now, "churned"},
		{"create into qualification", models.Deal{}, models.DealStageQualification, nil, "", 10, nil, ""},
		{"create into won", models.Deal{}, models.DealStageWon, nil, "", 100, &now, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deal := tc.before
			applyDealTransition(&deal, tc.to, tc.probability, tc.lostReason, now)
			assert.Equal(t, tc.to, deal.Stage)
			assert.Equal(t, tc.wantProb, deal.Probability)
			assert.Equal(t, tc.wantReason, deal.LostReason)
			if tc.wantClosed == nil {
				assert.Nil(t, deal.ClosedAt)
			} else {
				require.NotNil(t, deal.ClosedAt)
				assert.True(t, tc.wantClosed.Equal(*deal.ClosedAt))
			}
		})
	}
}

// --- The writes, on a real database -------------------------------------------

func setupDealServiceDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.Migrator().DropTable(&models.DealStageChange{}, &models.Deal{}, &models.Lead{}, &models.Customer{}, &models.Company{}, &models.User{}))
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Lead{}, &models.Customer{}, &models.Deal{}, &models.DealStageChange{}))
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func newRealDealService(t *testing.T, db *gorm.DB, dealRepo repository.DealRepository, currency DealCurrencySource) DealService {
	t.Helper()
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	return NewDealService(dealRepo, repository.NewCompanyRepository(db), repository.NewCustomerRepository(db),
		repository.NewLeadRepository(db), repository.NewUserRepository(db), currency, utils.NewTransactionManager(db))
}

func seedDealOwner(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	owner := &models.User{Email: "owner@example.com", Password: "x", FirstName: "O", LastName: "W", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(owner).Error)
	return owner
}

func historyOf(t *testing.T, db *gorm.DB, dealID uint) []models.DealStageChange {
	t.Helper()
	changes, err := repository.NewDealRepository(db).ListStageChanges(dealID)
	require.NoError(t, err)
	return changes
}

func TestDealService_CreateWritesTheDealAndItsFirstHistoryRowTogether(t *testing.T) {
	db := setupDealServiceDB(t)
	owner := seedDealOwner(t, db)
	svc := newRealDealService(t, db, repository.NewDealRepository(db), &stubCurrency{value: "RON"})

	deal := &models.Deal{Title: "  First  ", OwnerID: owner.ID}
	require.NoError(t, svc.Create(deal, nil, owner.ID))

	stored, err := svc.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Equal(t, "First", stored.Title, "trimmed")
	assert.Equal(t, models.DealStageQualification, stored.Stage, "stage defaults to qualification")
	assert.Equal(t, 10, stored.Probability, "probability defaults to the stage's")
	assert.Equal(t, "RON", stored.Currency, "currency comes from the configuration")
	assert.Nil(t, stored.ClosedAt)

	history := historyOf(t, db, deal.ID)
	require.Len(t, history, 1)
	assert.Nil(t, history[0].FromStage)
	assert.Equal(t, models.DealStageQualification, history[0].ToStage)
	assert.Equal(t, owner.ID, history[0].ChangedByID)

	// Created straight into lost: 0, closed, reason kept.
	lost := &models.Deal{Title: "Lost at once", Stage: models.DealStageLost, LostReason: "competitor", OwnerID: owner.ID}
	explicit := 60
	require.NoError(t, svc.Create(lost, &explicit, owner.ID))
	stored, err = svc.GetByID(lost.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, stored.Probability)
	assert.NotNil(t, stored.ClosedAt)
	assert.Equal(t, "competitor", stored.LostReason)
}

// The configured default is used only when the request sends no currency, is
// normalised, and falls back to EUR when it is unusable.
func TestDealService_CurrencyDefaultComesFromConfiguration(t *testing.T) {
	db := setupDealServiceDB(t)
	owner := seedDealOwner(t, db)
	currency := &stubCurrency{}
	svc := newRealDealService(t, db, repository.NewDealRepository(db), currency)

	for _, tc := range []struct {
		configured string
		err        error
		want       string
	}{
		{"USD", nil, "USD"},
		{" ron ", nil, "RON"},
		{"", nil, models.DealDefaultCurrency},
		{"Euro", nil, models.DealDefaultCurrency},
		{"eur", nil, "EUR"},
		{"", errors.New("not seeded"), models.DealDefaultCurrency},
	} {
		currency.value, currency.err = tc.configured, tc.err
		deal := &models.Deal{Title: "X", OwnerID: owner.ID}
		require.NoError(t, svc.Create(deal, nil, owner.ID))
		stored, err := svc.GetByID(deal.ID)
		require.NoError(t, err)
		assert.Equalf(t, tc.want, stored.Currency, "configured %q", tc.configured)
	}

	// An explicit currency is never overridden by the configuration, which is
	// not even read.
	before := currency.calls
	deal := &models.Deal{Title: "X", Currency: "GBP", OwnerID: owner.ID}
	require.NoError(t, svc.Create(deal, nil, owner.ID))
	stored, err := svc.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Equal(t, "GBP", stored.Currency)
	assert.Equal(t, before, currency.calls)

	// A nil source (no configuration wired at all) is the shipped default.
	bare := newRealDealService(t, db, repository.NewDealRepository(db), nil)
	deal = &models.Deal{Title: "X", OwnerID: owner.ID}
	require.NoError(t, bare.Create(deal, nil, owner.ID))
	stored, err = bare.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Equal(t, models.DealDefaultCurrency, stored.Currency)
}

// failingHistoryRepo is the real repository with the history write sabotaged,
// so the deal write runs for real on the transaction and then has to be
// rolled back.
type failingHistoryRepo struct {
	repository.DealRepository
}

func (r failingHistoryRepo) CreateStageChange(*models.DealStageChange) error {
	return errors.New("disk full")
}
func (r failingHistoryRepo) WithTx(tx *gorm.DB) repository.DealRepository {
	return failingHistoryRepo{r.DealRepository.WithTx(tx)}
}

func TestDealService_AFailedHistoryWriteRollsBackTheDeal(t *testing.T) {
	db := setupDealServiceDB(t)
	owner := seedDealOwner(t, db)
	svc := newRealDealService(t, db, failingHistoryRepo{repository.NewDealRepository(db)}, &stubCurrency{value: "EUR"})

	// Create: no deal row survives.
	deal := &models.Deal{Title: "Never", OwnerID: owner.ID}
	err := svc.Create(deal, nil, owner.ID)
	require.Error(t, err)
	assert.False(t, apperrors.IsNotFound(err), "a failed write must not read as a miss")
	var count int64
	require.NoError(t, db.Model(&models.Deal{}).Count(&count).Error)
	assert.Equal(t, int64(0), count, "the deal insert was rolled back with the failed history row")

	// Stage change: the deal keeps its old stage.
	healthy := newRealDealService(t, db, repository.NewDealRepository(db), &stubCurrency{value: "EUR"})
	require.NoError(t, healthy.Create(deal, nil, owner.ID))
	_, err = svc.ChangeStage(deal.ID, models.DealStageWon, nil, nil, owner.ID)
	require.Error(t, err)
	stored, err := healthy.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Equal(t, models.DealStageQualification, stored.Stage, "the stage update was rolled back")
	assert.Nil(t, stored.ClosedAt)
	assert.Len(t, historyOf(t, db, deal.ID), 1, "only the creating row")

	// Update with a stage change: same thing.
	stored.Stage = models.DealStageProposal
	stored.Owner = nil
	err = svc.Update(stored, nil, owner.ID)
	require.Error(t, err)
	again, err := healthy.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Equal(t, models.DealStageQualification, again.Stage)
}

func TestDealService_ChangeStageAppliesTheRulesAndAppendsHistory(t *testing.T) {
	db := setupDealServiceDB(t)
	owner := seedDealOwner(t, db)
	svc := newRealDealService(t, db, repository.NewDealRepository(db), &stubCurrency{value: "EUR"})
	deal := &models.Deal{Title: "Journey", OwnerID: owner.ID}
	require.NoError(t, svc.Create(deal, nil, owner.ID))

	// qualification → proposal: default probability, still open.
	moved, err := svc.ChangeStage(deal.ID, models.DealStageProposal, nil, nil, owner.ID)
	require.NoError(t, err)
	assert.Equal(t, 40, moved.Probability)
	assert.Nil(t, moved.ClosedAt)

	// → negotiation with an explicit probability.
	explicit := 80
	moved, err = svc.ChangeStage(deal.ID, models.DealStageNegotiation, &explicit, nil, owner.ID)
	require.NoError(t, err)
	assert.Equal(t, 80, moved.Probability)

	// → won: 100, closed_at set, whatever probability was sent.
	low := 5
	moved, err = svc.ChangeStage(deal.ID, models.DealStageWon, &low, nil, owner.ID)
	require.NoError(t, err)
	assert.Equal(t, 100, moved.Probability)
	require.NotNil(t, moved.ClosedAt)
	wonAt := *moved.ClosedAt

	// The same stage again: 200-equivalent, nothing written.
	moved, err = svc.ChangeStage(deal.ID, models.DealStageWon, nil, nil, owner.ID)
	require.NoError(t, err)
	assert.True(t, wonAt.Equal(*moved.ClosedAt))
	assert.Len(t, historyOf(t, db, deal.ID), 4, "no history row for a same-stage request")

	// → lost with a reason: 0, closed, reason kept.
	reason := "went with a competitor"
	moved, err = svc.ChangeStage(deal.ID, models.DealStageLost, nil, &reason, owner.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, moved.Probability)
	assert.Equal(t, reason, moved.LostReason)
	require.NotNil(t, moved.ClosedAt)

	// → lost again is a no-op even with a new reason.
	other := "changed my mind"
	moved, err = svc.ChangeStage(deal.ID, models.DealStageLost, nil, &other, owner.ID)
	require.NoError(t, err)
	assert.Equal(t, reason, moved.LostReason)

	// → back to an open stage: closed_at and the reason are cleared.
	moved, err = svc.ChangeStage(deal.ID, models.DealStageQualification, nil, nil, owner.ID)
	require.NoError(t, err)
	assert.Equal(t, 10, moved.Probability)
	assert.Nil(t, moved.ClosedAt)
	assert.Equal(t, "", moved.LostReason, "lost_reason is cleared when leaving lost")

	history := historyOf(t, db, deal.ID)
	require.Len(t, history, 6)
	got := make([]string, 0, len(history))
	for _, h := range history {
		from := "nil"
		if h.FromStage != nil {
			from = string(*h.FromStage)
		}
		got = append(got, from+"→"+string(h.ToStage))
		assert.Equal(t, owner.ID, h.ChangedByID)
	}
	assert.Equal(t, []string{
		"nil→qualification", "qualification→proposal", "proposal→negotiation",
		"negotiation→won", "won→lost", "lost→qualification",
	}, got)
	for i := 1; i < len(history); i++ {
		assert.False(t, history[i].ChangedAt.Before(history[i-1].ChangedAt), "oldest first")
	}

	// An unknown deal is a miss.
	_, err = svc.ChangeStage(999, models.DealStageWon, nil, nil, owner.ID)
	assert.True(t, errors.Is(err, apperrors.ErrNotFound))
}

func TestDealService_UpdateGoesThroughTheTransitionOnlyWhenTheStageChanges(t *testing.T) {
	db := setupDealServiceDB(t)
	owner := seedDealOwner(t, db)
	svc := newRealDealService(t, db, repository.NewDealRepository(db), &stubCurrency{value: "EUR"})
	deal := &models.Deal{Title: "Edit me", OwnerID: owner.ID, LostReason: "should not be stored on an open deal"}
	require.NoError(t, svc.Create(deal, nil, owner.ID))
	stored, err := svc.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Equal(t, "", stored.LostReason, "lost_reason is only stored on a lost deal")

	// Same stage: text changes, no history, probability kept unless sent.
	stored.Title = "Edited"
	stored.Currency = ""
	stored.Owner = nil
	require.NoError(t, svc.Update(stored, nil, owner.ID))
	stored, err = svc.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Equal(t, "Edited", stored.Title)
	assert.Equal(t, "EUR", stored.Currency, "an empty currency on update keeps the stored one")
	assert.Equal(t, 10, stored.Probability)
	assert.Len(t, historyOf(t, db, deal.ID), 1)

	explicit := 25
	stored.Owner = nil
	require.NoError(t, svc.Update(stored, &explicit, owner.ID))
	stored, err = svc.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Equal(t, 25, stored.Probability, "an explicit probability is applied on the same stage")
	assert.Len(t, historyOf(t, db, deal.ID), 1)

	// Stage changed through PUT: the transition rules and a history row.
	stored.Stage = models.DealStageLost
	stored.LostReason = "priced out"
	stored.Owner = nil
	require.NoError(t, svc.Update(stored, nil, owner.ID))
	stored, err = svc.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, stored.Probability)
	assert.NotNil(t, stored.ClosedAt)
	assert.Equal(t, "priced out", stored.LostReason)
	history := historyOf(t, db, deal.ID)
	require.Len(t, history, 2)
	require.NotNil(t, history[1].FromStage)
	assert.Equal(t, models.DealStageQualification, *history[1].FromStage)
	assert.Equal(t, models.DealStageLost, history[1].ToStage)

	// PUT back to open with the reason still in the body: the reason is
	// dropped because the deal is no longer lost.
	stored.Stage = models.DealStageProposal
	stored.Owner = nil
	require.NoError(t, svc.Update(stored, nil, owner.ID))
	stored, err = svc.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Equal(t, 40, stored.Probability)
	assert.Nil(t, stored.ClosedAt)
	assert.Equal(t, "", stored.LostReason)
	assert.Len(t, historyOf(t, db, deal.ID), 3)

	// An unknown deal is a miss.
	err = svc.Update(&models.Deal{BaseModel: models.BaseModel{ID: 999}, Title: "X", OwnerID: owner.ID}, nil, owner.ID)
	assert.True(t, errors.Is(err, apperrors.ErrNotFound))
}

// A deal keeps its links when the customer, the lead or the owning user behind
// them is erased (erasure soft-deletes the row), and it must stay editable: an
// update that leaves the links as they are goes through, a link the request
// clears is cleared, and a link the request sets is still checked against the
// live rows.
func TestDealService_UpdateSurvivesTheErasureOfItsLinks(t *testing.T) {
	db := setupDealServiceDB(t)
	// Erasure purges the owner's credentials and scrubs the lead's form
	// submissions in the same transaction, so those tables have to exist.
	require.NoError(t, db.AutoMigrate(&models.APIKey{}, &models.RefreshToken{}, &models.PasswordResetToken{},
		&models.Form{}, &models.FormSubmission{}, &models.FormConfirmationToken{}))
	owner := seedDealOwner(t, db)
	actor := &models.User{Email: "deal-editor@example.com", Password: "x", FirstName: "A", LastName: "D", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(actor).Error)
	customer := &models.Customer{FirstName: "Erased", LastName: "Customer", Email: "erased-customer@example.com"}
	require.NoError(t, db.Create(customer).Error)
	lead := &models.Lead{FirstName: "Erased", LastName: "Lead", Email: "erased-lead@example.com", OwnerID: owner.ID}
	require.NoError(t, db.Create(lead).Error)
	svc := newRealDealService(t, db, repository.NewDealRepository(db), &stubCurrency{value: "EUR"})

	deal := &models.Deal{Title: "Outlives its links", OwnerID: owner.ID, CustomerID: &customer.ID, LeadID: &lead.ID}
	require.NoError(t, svc.Create(deal, nil, actor.ID))

	require.NoError(t, repository.NewCustomerRepository(db).Delete(customer.ID))
	require.NoError(t, repository.NewLeadRepository(db).Delete(lead.ID))
	require.NoError(t, repository.NewUserRepository(db).Delete(owner.ID))

	// The handler hands over the stored deal with the body applied; a link
	// absent from the body is exactly as stored.
	stored, err := svc.GetByID(deal.ID)
	require.NoError(t, err)
	stored.Owner, stored.Customer, stored.Lead = nil, nil, nil
	stored.Title = "Edited after the erasure"
	require.NoError(t, svc.Update(stored, nil, actor.ID), "links the request did not set are not re-checked")

	stored, err = svc.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Equal(t, "Edited after the erasure", stored.Title)
	require.NotNil(t, stored.CustomerID)
	assert.Equal(t, customer.ID, *stored.CustomerID, "the link to the erased customer is kept")
	require.NotNil(t, stored.LeadID)
	assert.Equal(t, lead.ID, *stored.LeadID, "the link to the erased lead is kept")
	assert.Equal(t, owner.ID, stored.OwnerID, "the erased owner is kept")
	assert.Len(t, historyOf(t, db, deal.ID), 1, "a same-stage edit writes no history row")

	// A link the request does set is still checked: an unknown customer is
	// refused, 0 (nil on the deal) clears, a live customer is taken.
	unknown := uint(999999)
	stored.Owner, stored.Customer, stored.Lead = nil, nil, nil
	stored.CustomerID = &unknown
	err = svc.Update(stored, nil, actor.ID)
	assert.True(t, errors.Is(err, apperrors.ErrCustomerNotFound), "got %v", err)

	stored, err = svc.GetByID(deal.ID)
	require.NoError(t, err)
	stored.Owner, stored.Customer, stored.Lead = nil, nil, nil
	stored.CustomerID = nil
	require.NoError(t, svc.Update(stored, nil, actor.ID))
	stored, err = svc.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Nil(t, stored.CustomerID, "clearing the link to an erased customer still works")
	require.NotNil(t, stored.LeadID, "the other links are untouched")

	live := &models.Customer{FirstName: "Live", LastName: "Customer", Email: "live-customer@example.com"}
	require.NoError(t, db.Create(live).Error)
	stored.Owner, stored.Customer, stored.Lead = nil, nil, nil
	stored.CustomerID = &live.ID
	require.NoError(t, svc.Update(stored, nil, actor.ID))
	stored, err = svc.GetByID(deal.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.CustomerID)
	assert.Equal(t, live.ID, *stored.CustomerID, "a live customer replaces the erased one")
}

func TestDealService_DeleteIsSoftAndHistoryStays(t *testing.T) {
	db := setupDealServiceDB(t)
	owner := seedDealOwner(t, db)
	svc := newRealDealService(t, db, repository.NewDealRepository(db), &stubCurrency{value: "EUR"})
	deal := &models.Deal{Title: "Doomed", OwnerID: owner.ID}
	require.NoError(t, svc.Create(deal, nil, owner.ID))

	require.NoError(t, svc.Delete(deal.ID))
	_, err := svc.GetByID(deal.ID)
	assert.True(t, errors.Is(err, apperrors.ErrNotFound))
	assert.True(t, errors.Is(svc.Delete(deal.ID), apperrors.ErrNotFound), "a second delete is a miss")
	assert.Len(t, historyOf(t, db, deal.ID), 1, "history rows stay")

	_, err = svc.History(deal.ID)
	assert.True(t, errors.Is(err, apperrors.ErrNotFound), "history of a deleted deal is a miss through the service")
}

// The company delete from CD-1 also has to let go of its deals.
func TestCompanyServiceDelete_UnlinksDealsToo(t *testing.T) {
	db := setupDealServiceDB(t)
	owner := seedDealOwner(t, db)
	company := &models.Company{Name: "Acme"}
	require.NoError(t, db.Create(company).Error)
	deal := &models.Deal{Title: "Linked", Currency: "EUR", Stage: models.DealStageQualification, Probability: 10, OwnerID: owner.ID, CompanyID: &company.ID}
	require.NoError(t, db.Create(deal).Error)
	companies := NewCompanyService(repository.NewCompanyRepository(db), repository.NewUserRepository(db), utils.NewTransactionManager(db))

	require.NoError(t, companies.Delete(company.ID))

	var reloaded models.Deal
	require.NoError(t, db.First(&reloaded, deal.ID).Error)
	assert.Nil(t, reloaded.CompanyID, "the deal no longer points at the deleted company")

	// ...and the unlink is rolled back with a failed delete.
	company = &models.Company{Name: "Sturdy"}
	require.NoError(t, db.Create(company).Error)
	require.NoError(t, db.Model(&reloaded).Update("company_id", company.ID).Error)
	failing := NewCompanyService(failingDeleteRepo{repository.NewCompanyRepository(db)}, repository.NewUserRepository(db), utils.NewTransactionManager(db))
	require.Error(t, failing.Delete(company.ID))
	require.NoError(t, db.First(&reloaded, deal.ID).Error)
	require.NotNil(t, reloaded.CompanyID)
	assert.Equal(t, company.ID, *reloaded.CompanyID)
}

// TestDealService_ClosedAtAndChangedAtAreStoredInUTC pins the stamps to UTC
// whatever zone the clock reports in. GORM's own created_at/updated_at are UTC,
// and on SQLite the columns are text with the offset, so a table holding mixed
// offsets would compare and sort wrongly in range queries over closed_at.
func TestDealService_ClosedAtAndChangedAtAreStoredInUTC(t *testing.T) {
	db := setupDealServiceDB(t)
	owner := seedDealOwner(t, db)
	svc := newRealDealService(t, db, repository.NewDealRepository(db), &stubCurrency{value: "EUR"})
	bucharest := time.FixedZone("EEST", 3*60*60)
	svc.(*dealService).now = func() time.Time { return time.Now().In(bucharest) }

	deal := &models.Deal{Title: "Zoned", OwnerID: owner.ID}
	require.NoError(t, svc.Create(deal, nil, owner.ID))

	moved, err := svc.ChangeStage(deal.ID, models.DealStageWon, nil, nil, owner.ID)
	require.NoError(t, err)
	require.NotNil(t, moved.ClosedAt)
	assert.Equal(t, time.UTC, moved.ClosedAt.Location(), "returned closed_at")

	stored, err := svc.GetByID(deal.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.ClosedAt)
	assert.Equal(t, time.UTC, stored.ClosedAt.Location(), "closed_at read back")

	history := historyOf(t, db, deal.ID)
	require.Len(t, history, 2)
	for _, h := range history {
		assert.Equal(t, time.UTC, h.ChangedAt.Location(), "changed_at read back for %s", h.ToStage)
	}

	var rawClosedAt, rawChangedAt string
	require.NoError(t, db.Raw("SELECT closed_at FROM deals WHERE id = ?", deal.ID).Scan(&rawClosedAt).Error)
	require.NoError(t, db.Raw("SELECT changed_at FROM deal_stage_changes WHERE deal_id = ? ORDER BY id DESC LIMIT 1", deal.ID).Scan(&rawChangedAt).Error)
	for name, raw := range map[string]string{"closed_at": rawClosedAt, "changed_at": rawChangedAt} {
		assert.True(t, strings.HasSuffix(raw, "Z") || strings.HasSuffix(raw, "+00:00"),
			"raw %s %q must carry a UTC offset", name, raw)
	}
	t.Logf("raw closed_at=%q changed_at=%q", rawClosedAt, rawChangedAt)
}

// --- Pipeline and the dashboard widgets --------------------------------------

// The weighted amount is Σ amount_cents × probability / 100, rounded half up
// once on the total.
func TestWeightedCents_RoundsHalfUpOnce(t *testing.T) {
	for _, tc := range []struct {
		sum, want int64
	}{
		{0, 0},
		{49, 0},              // 0.49 down
		{50, 1},              // 1 × 50: 0.5 half up
		{99, 1},              // 0.99 up
		{100, 1},             // exact
		{149, 1},             // 1.49 down
		{150, 2},             // 1.5 half up
		{333 * 33, 110},      // 10989 → 109.89 → 110
		{100000 * 40, 40000}, // exact
		{-50, 0},             // -0.5 half up is 0 (never produced; the rule holds anyway)
		{-150, -1},           // -1.5 half up
		{-151, -2},           // -1.51
	} {
		assert.Equalf(t, tc.want, weightedCents(tc.sum), "weightedCents(%d)", tc.sum)
	}
}

// Rounding happens on the group total, not per deal: two deals of 1 cent at
// 50 % are 0.5 + 0.5 = 1 cent, where per-deal rounding would give 2.
func TestBuildDealPipelineStages_RoundsTheTotalNotEachDeal(t *testing.T) {
	stages := buildDealPipelineStages([]models.DealPipelineRow{
		{Stage: models.DealStageProposal, Currency: "EUR", DealCount: 2, AmountCents: 2, AmountTimesProbability: 1*50 + 1*50},
	})
	assert.Equal(t, []models.DealPipelineTotal{{Currency: "EUR", AmountCents: 2, WeightedCents: 1}}, stages[1].Totals)
}

// All five stages in pipeline order, empty ones with count 0 and an empty
// (non-nil) totals list; a stage's currencies sorted by code; a stage outside
// the model appended after the five rather than dropped.
func TestBuildDealPipelineStages_Layout(t *testing.T) {
	stages := buildDealPipelineStages([]models.DealPipelineRow{
		{Stage: models.DealStageWon, Currency: "USD", DealCount: 1, AmountCents: 500, AmountTimesProbability: 50000},
		{Stage: models.DealStageNegotiation, Currency: "USD", DealCount: 1, AmountCents: 10, AmountTimesProbability: 700},
		{Stage: models.DealStageNegotiation, Currency: "CHF", DealCount: 2, AmountCents: 20, AmountTimesProbability: 1400},
		{Stage: "archived", Currency: "EUR", DealCount: 1, AmountCents: 1, AmountTimesProbability: 0},
	})

	names := []models.DealStage{}
	for _, stage := range stages {
		names = append(names, stage.Stage)
	}
	assert.Equal(t, []models.DealStage{"qualification", "proposal", "negotiation", "won", "lost", "archived"}, names)

	for _, i := range []int{0, 1, 4} {
		assert.Equal(t, int64(0), stages[i].Count)
		assert.NotNil(t, stages[i].Totals, "%s: an empty stage has [] not null", stages[i].Stage)
		assert.Empty(t, stages[i].Totals)
	}
	assert.Equal(t, int64(3), stages[2].Count)
	assert.Equal(t, []models.DealPipelineTotal{
		{Currency: "CHF", AmountCents: 20, WeightedCents: 14},
		{Currency: "USD", AmountCents: 10, WeightedCents: 7},
	}, stages[2].Totals)
	assert.Equal(t, []models.DealPipelineTotal{{Currency: "USD", AmountCents: 500, WeightedCents: 500}}, stages[3].Totals)

	empty := buildDealPipelineStages(nil)
	assert.Len(t, empty, 5)
}

func TestUTCMonthBounds(t *testing.T) {
	athens := time.FixedZone("UTC+3", 3*3600)
	for _, tc := range []struct {
		name     string
		now      time.Time
		from, to string
	}{
		{"mid month", time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC), "2026-09-01T00:00:00Z", "2026-10-01T00:00:00Z"},
		{"last instant", time.Date(2026, 9, 30, 23, 59, 59, 999999999, time.UTC), "2026-09-01T00:00:00Z", "2026-10-01T00:00:00Z"},
		{"first instant", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z"},
		// 01:30 on 1 October in UTC+3 is still September in UTC.
		{"local zone ahead", time.Date(2026, 10, 1, 1, 30, 0, 0, athens), "2026-09-01T00:00:00Z", "2026-10-01T00:00:00Z"},
		{"december rolls the year", time.Date(2026, 12, 31, 8, 0, 0, 0, time.UTC), "2026-12-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"february of a leap year", time.Date(2028, 2, 29, 8, 0, 0, 0, time.UTC), "2028-02-01T00:00:00Z", "2028-03-01T00:00:00Z"},
	} {
		from, to := utcMonthBounds(tc.now)
		assert.Equal(t, tc.from, from.Format(time.RFC3339), tc.name)
		assert.Equal(t, tc.to, to.Format(time.RFC3339), tc.name)
		assert.Equal(t, time.UTC, from.Location(), tc.name)
	}
}

type dealPipelineServiceFixture struct {
	repo    *mocks.DealRepository
	service *dealService
}

func newDealPipelineService(t *testing.T) dealPipelineServiceFixture {
	t.Helper()
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
	repo := new(mocks.DealRepository)
	t.Cleanup(func() { repo.AssertExpectations(t) })
	svc := NewDealService(repo, nil, nil, nil, nil, nil, nil).(*dealService)
	return dealPipelineServiceFixture{repo: repo, service: svc}
}

// Admin keeps the filter as given (owner included), sales is narrowed to its
// own id whatever it asked for, and every other role is refused before the
// repository is read.
func TestDealService_PipelineScoping(t *testing.T) {
	company := uint(3)
	asked := uint(4)

	f := newDealPipelineService(t)
	f.repo.On("Pipeline", repository.DealPipelineFilter{OwnerID: &asked, CompanyID: &company}).Return([]models.DealPipelineRow{}, nil).Once()
	pipeline, err := f.service.Pipeline(repository.DealPipelineFilter{OwnerID: &asked, CompanyID: &company}, 1, models.RoleAdmin)
	require.NoError(t, err)
	assert.Len(t, pipeline.Stages, 5)

	f.repo.On("Pipeline", repository.DealPipelineFilter{}).Return([]models.DealPipelineRow{}, nil).Once()
	_, err = f.service.Pipeline(repository.DealPipelineFilter{}, 1, models.RoleAdmin)
	require.NoError(t, err, "admin without an owner filter sees everybody")

	own := uint(7)
	f.repo.On("Pipeline", repository.DealPipelineFilter{OwnerID: &own, CompanyID: &company}).Return([]models.DealPipelineRow{}, nil).Once()
	_, err = f.service.Pipeline(repository.DealPipelineFilter{OwnerID: &asked, CompanyID: &company}, 7, models.RoleSales)
	require.NoError(t, err)
	f.repo.On("Pipeline", repository.DealPipelineFilter{OwnerID: &own}).Return([]models.DealPipelineRow{}, nil).Once()
	_, err = f.service.Pipeline(repository.DealPipelineFilter{}, 7, models.RoleSales)
	require.NoError(t, err, "sales without an owner filter still sees only itself")

	for _, role := range []models.UserRole{models.RoleSupport, models.RoleCustomer, ""} {
		_, err = f.service.Pipeline(repository.DealPipelineFilter{}, 7, role)
		assert.Truef(t, errors.Is(err, apperrors.ErrForbidden), "%q: got %v", role, err)
	}
}

// The dashboard variant: the same scoping, support refused with the forbidden
// sentinel, and the won range is the current UTC month from the service
// clock.
func TestDealService_DashboardPipeline(t *testing.T) {
	f := newDealPipelineService(t)
	f.service.now = func() time.Time { return time.Date(2026, 10, 1, 1, 30, 0, 0, time.FixedZone("UTC+3", 3*3600)) }
	september := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	october := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	f.repo.On("Pipeline", repository.DealPipelineFilter{}).Return([]models.DealPipelineRow{
		{Stage: models.DealStageProposal, Currency: "EUR", DealCount: 1, AmountCents: 333, AmountTimesProbability: 333 * 33},
	}, nil).Once()
	f.repo.On("WonBetween", (*uint)(nil), september, october).Return([]models.DealWonRow{
		{Currency: "USD", DealCount: 1, AmountCents: 5},
		{Currency: "EUR", DealCount: 2, AmountCents: 7},
	}, nil).Once()
	result, err := f.service.DashboardPipeline(1, models.RoleAdmin)
	require.NoError(t, err)
	assert.Len(t, result.Stages, 5)
	assert.Equal(t, []models.DealPipelineTotal{{Currency: "EUR", AmountCents: 333, WeightedCents: 110}}, result.Stages[1].Totals)
	assert.Equal(t, models.DealWonSummary{Count: 3, Totals: []models.DealWonTotal{
		{Currency: "EUR", AmountCents: 7},
		{Currency: "USD", AmountCents: 5},
	}}, result.WonThisMonth)

	own := uint(7)
	f.repo.On("Pipeline", repository.DealPipelineFilter{OwnerID: &own}).Return([]models.DealPipelineRow{}, nil).Once()
	f.repo.On("WonBetween", &own, september, october).Return([]models.DealWonRow{}, nil).Once()
	result, err = f.service.DashboardPipeline(7, models.RoleSales)
	require.NoError(t, err)
	assert.NotNil(t, result.WonThisMonth.Totals, "no won deal is [] not null")
	assert.Equal(t, int64(0), result.WonThisMonth.Count)

	for _, role := range []models.UserRole{models.RoleSupport, models.RoleCustomer} {
		_, err = f.service.DashboardPipeline(9, role)
		assert.Truef(t, errors.Is(err, apperrors.ErrForbidden), "%s: got %v", role, err)
	}
	f.repo.AssertNumberOfCalls(t, "Pipeline", 2)
	f.repo.AssertNumberOfCalls(t, "WonBetween", 2)

	// A repository failure is passed on, not turned into an empty widget.
	f.repo.On("Pipeline", repository.DealPipelineFilter{}).Return(nil, errors.New("boom")).Once()
	_, err = f.service.DashboardPipeline(1, models.RoleAdmin)
	assert.EqualError(t, err, "boom")
}
