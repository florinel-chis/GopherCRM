package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fakeAEOProvider is a Provider that never talks to a network. The service only
// ever asks a provider for its identity — the querying is the engine's job — so
// Query exists purely to satisfy the interface.
type fakeAEOProvider struct {
	name  string
	model string
}

func (p fakeAEOProvider) Name() string  { return p.name }
func (p fakeAEOProvider) Model() string { return p.model }
func (p fakeAEOProvider) Query(ctx context.Context, prompt string) (aeo.ProviderAnswer, error) {
	return aeo.ProviderAnswer{Text: "answer from " + p.name}, nil
}

// executorCall captures what StartRun handed the engine. The executor runs on
// its own goroutine, so the test reads the call off a channel rather than
// touching shared fields.
type executorCall struct {
	run     *models.AEORun
	prompts []models.AEOPrompt
	profile *models.AEOProfile
}

type fakeAEOExecutor struct {
	calls chan executorCall
}

func newFakeAEOExecutor() *fakeAEOExecutor {
	return &fakeAEOExecutor{calls: make(chan executorCall, 1)}
}

func (e *fakeAEOExecutor) Execute(ctx context.Context, run *models.AEORun, prompts []models.AEOPrompt, profile *models.AEOProfile) error {
	e.calls <- executorCall{run: run, prompts: prompts, profile: profile}
	return nil
}

type AEOServiceTestSuite struct {
	suite.Suite
	mockRepo  *mocks.AEORepository
	executor  *fakeAEOExecutor
	providers []aeo.Provider
	txManager *utils.TransactionManager
	db        *gorm.DB
	service   AEOService
}

func (suite *AEOServiceTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})

	// The transaction manager hands the transaction to the service through an
	// unexported context key, so it cannot be faked — the repository stays
	// mocked and this handle only ever opens and commits an empty transaction.
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	suite.Require().NoError(err)
	suite.db = db
	suite.txManager = utils.NewTransactionManager(db)
}

func (suite *AEOServiceTestSuite) SetupTest() {
	suite.mockRepo = new(mocks.AEORepository)
	suite.executor = newFakeAEOExecutor()
	suite.providers = []aeo.Provider{
		fakeAEOProvider{name: "openai", model: "gpt-4o-mini"},
		fakeAEOProvider{name: "anthropic", model: "claude-opus-5"},
	}
	suite.service = NewAEOService(suite.mockRepo, suite.executor, suite.providers, suite.txManager)
}

func (suite *AEOServiceTestSuite) TearDownTest() {
	suite.mockRepo.AssertExpectations(suite.T())
}

// withoutProviders rebuilds the service with no configured engine, which is the
// state of a fresh deployment with no API keys in the environment.
func (suite *AEOServiceTestSuite) withoutProviders() AEOService {
	return NewAEOService(suite.mockRepo, suite.executor, nil, suite.txManager)
}

func testAEOProfile() *models.AEOProfile {
	profile := &models.AEOProfile{
		BrandName:    "Acme",
		BrandAliases: []string{"Acme Inc"},
		OwnedDomains: []string{"acme.com"},
		Competitors: []models.AEOCompetitor{
			{Name: "Globex", Domain: "globex.com"},
			{Name: "Initech", Domain: "initech.com"},
		},
	}
	profile.ID = 1
	return profile
}

// ---------------------------------------------------------------- profile ---

func (suite *AEOServiceTestSuite) TestGetProfile_NotConfigured() {
	suite.mockRepo.On("GetProfile").Return(nil, gorm.ErrRecordNotFound)

	profile, err := suite.service.GetProfile()

	assert.Nil(suite.T(), profile)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrProfileNotConfigured))
}

func (suite *AEOServiceTestSuite) TestGetProfile_Success() {
	expected := testAEOProfile()
	suite.mockRepo.On("GetProfile").Return(expected, nil)

	profile, err := suite.service.GetProfile()

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), expected, profile)
}

func (suite *AEOServiceTestSuite) TestSaveProfile_NormalizesAndPinsSingletonID() {
	input := &models.AEOProfile{
		BrandName:    "  Acme  ",
		Description:  "  We sell anvils.  ",
		BrandAliases: []string{" Acme Inc ", "acme inc", ""},
		OwnedDomains: []string{"https://WWW.Acme.com/pricing", "acme.com", " "},
		Competitors: []models.AEOCompetitor{
			{Name: " Globex ", Aliases: []string{" Globex Corp ", "Globex Corp"}, Domain: "HTTP://www.Globex.com"},
			{Name: "   "},
		},
	}

	suite.mockRepo.On("UpsertProfile", mock.AnythingOfType("*models.AEOProfile")).Return(nil)

	saved, err := suite.service.SaveProfile(input)

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), uint(1), saved.ID)
	assert.Equal(suite.T(), "Acme", saved.BrandName)
	assert.Equal(suite.T(), "We sell anvils.", saved.Description)
	// The duplicate alias differs only by case, and the domain arrives as a
	// full URL: both collapse to one normalized entry.
	assert.Equal(suite.T(), []string{"Acme Inc"}, saved.BrandAliases)
	assert.Equal(suite.T(), []string{"acme.com"}, saved.OwnedDomains)
	suite.Require().Len(saved.Competitors, 1)
	assert.Equal(suite.T(), "Globex", saved.Competitors[0].Name)
	assert.Equal(suite.T(), []string{"Globex Corp"}, saved.Competitors[0].Aliases)
	assert.Equal(suite.T(), "globex.com", saved.Competitors[0].Domain)
}

func (suite *AEOServiceTestSuite) TestSaveProfile_RejectsBlankBrandName() {
	saved, err := suite.service.SaveProfile(&models.AEOProfile{BrandName: "   "})

	assert.Nil(suite.T(), saved)
	assert.True(suite.T(), errors.Is(err, ErrAEOInvalidProfile))
}

func (suite *AEOServiceTestSuite) TestSaveProfile_RejectsOverlongBrandName() {
	saved, err := suite.service.SaveProfile(&models.AEOProfile{BrandName: strings.Repeat("a", 121)})

	assert.Nil(suite.T(), saved)
	assert.True(suite.T(), errors.Is(err, ErrAEOInvalidProfile))
}

// ---------------------------------------------------------------- prompts ---

func (suite *AEOServiceTestSuite) TestListPrompts_DecoratesWindowMetrics() {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 30)
	lastRun := time.Date(2026, 8, 30, 6, 0, 0, 0, time.UTC)

	first := models.AEOPrompt{Text: "Which CRM for SMBs?"}
	first.ID = 1
	second := models.AEOPrompt{Text: "Best CRM for support teams?"}
	second.ID = 2

	suite.mockRepo.On("ListPrompts", true, 0, 20, "created_at", "desc").
		Return([]models.AEOPrompt{first, second}, nil)
	suite.mockRepo.On("CountPrompts", true).Return(int64(2), nil)
	suite.mockRepo.On("PromptVisibility", from, to, []uint{1, 2}).
		Return(map[uint]models.AEOPromptVisibility{
			1: {PromptID: 1, Answers: 3, Mentions: 2, LastRunAt: &lastRun},
		}, nil)

	prompts, total, err := suite.service.ListPrompts(from, to, true, 0, 20, "created_at", "desc")

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(2), total)
	suite.Require().Len(prompts, 2)
	assert.Equal(suite.T(), 66.7, prompts[0].Visibility)
	assert.Equal(suite.T(), int64(3), prompts[0].AnswerCount)
	assert.Equal(suite.T(), int64(2), prompts[0].MentionCount)
	assert.Equal(suite.T(), &lastRun, prompts[0].LastRunAt)
	// A prompt with no answers in the window is 0%, not a division by zero.
	assert.Equal(suite.T(), float64(0), prompts[1].Visibility)
	assert.Nil(suite.T(), prompts[1].LastRunAt)
}

func (suite *AEOServiceTestSuite) TestListPrompts_EmptyPageSkipsMetricsQuery() {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 7)

	suite.mockRepo.On("ListPrompts", false, 0, 20, "", "").Return(nil, nil)
	suite.mockRepo.On("CountPrompts", false).Return(int64(0), nil)

	prompts, total, err := suite.service.ListPrompts(from, to, false, 0, 20, "", "")

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), total)
	assert.NotNil(suite.T(), prompts)
	assert.Empty(suite.T(), prompts)
	suite.mockRepo.AssertNotCalled(suite.T(), "PromptVisibility", mock.Anything, mock.Anything, mock.Anything)
}

func (suite *AEOServiceTestSuite) TestCreatePrompts_Success() {
	suite.mockRepo.On("WithTx", mock.Anything).Return(suite.mockRepo)
	suite.mockRepo.On("CountPrompts", true).Return(int64(5), nil)
	suite.mockRepo.On("ExistsByTextInsensitive", "Which CRM for SMBs?", uint(0)).Return(false, nil)
	suite.mockRepo.On("ExistsByTextInsensitive", "Best CRM for support?", uint(0)).Return(false, nil)
	suite.mockRepo.On("CreatePrompt", mock.AnythingOfType("*models.AEOPrompt")).Return(nil).
		Run(func(args mock.Arguments) {
			args.Get(0).(*models.AEOPrompt).ID = 42
		})

	created, err := suite.service.CreatePrompts([]string{"  Which CRM for SMBs?  ", "Best CRM for support?"}, 7)

	assert.NoError(suite.T(), err)
	suite.Require().Len(created, 2)
	assert.Equal(suite.T(), "Which CRM for SMBs?", created[0].Text)
	assert.True(suite.T(), created[0].IsActive)
	suite.Require().NotNil(created[0].CreatedByID)
	assert.Equal(suite.T(), uint(7), *created[0].CreatedByID)
}

func (suite *AEOServiceTestSuite) TestCreatePrompts_AnonymousCreatorLeavesOwnerNil() {
	suite.mockRepo.On("WithTx", mock.Anything).Return(suite.mockRepo)
	suite.mockRepo.On("CountPrompts", true).Return(int64(0), nil)
	suite.mockRepo.On("ExistsByTextInsensitive", "Which CRM?", uint(0)).Return(false, nil)
	suite.mockRepo.On("CreatePrompt", mock.AnythingOfType("*models.AEOPrompt")).Return(nil)

	created, err := suite.service.CreatePrompts([]string{"Which CRM?"}, 0)

	assert.NoError(suite.T(), err)
	suite.Require().Len(created, 1)
	assert.Nil(suite.T(), created[0].CreatedByID)
}

// A batch that repeats a text differing only by case must be rejected before
// anything is written — otherwise the LOWER(text) uniqueness rule would be
// enforced against the database but not within a single request.
func (suite *AEOServiceTestSuite) TestCreatePrompts_RejectsDuplicateWithinBatch() {
	created, err := suite.service.CreatePrompts([]string{"Which CRM?", "which crm?"}, 1)

	assert.Nil(suite.T(), created)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrDuplicatePrompt))
	suite.mockRepo.AssertNotCalled(suite.T(), "CreatePrompt", mock.Anything)
}

func (suite *AEOServiceTestSuite) TestCreatePrompts_RejectsExistingText() {
	suite.mockRepo.On("WithTx", mock.Anything).Return(suite.mockRepo)
	suite.mockRepo.On("CountPrompts", true).Return(int64(1), nil)
	suite.mockRepo.On("ExistsByTextInsensitive", "Which CRM?", uint(0)).Return(true, nil)

	created, err := suite.service.CreatePrompts([]string{"Which CRM?"}, 1)

	assert.Nil(suite.T(), created)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrDuplicatePrompt))
	suite.mockRepo.AssertNotCalled(suite.T(), "CreatePrompt", mock.Anything)
}

func (suite *AEOServiceTestSuite) TestCreatePrompts_EnforcesActivePromptCap() {
	suite.mockRepo.On("WithTx", mock.Anything).Return(suite.mockRepo)
	suite.mockRepo.On("CountPrompts", true).Return(int64(99), nil)

	created, err := suite.service.CreatePrompts([]string{"One more?", "And another?"}, 1)

	assert.Nil(suite.T(), created)
	assert.True(suite.T(), errors.Is(err, ErrAEOPromptLimit))
	suite.mockRepo.AssertNotCalled(suite.T(), "CreatePrompt", mock.Anything)
}

// The cap is a ceiling, not a barrier: filling the last slot exactly is fine.
func (suite *AEOServiceTestSuite) TestCreatePrompts_AllowsFillingTheLastSlot() {
	suite.mockRepo.On("WithTx", mock.Anything).Return(suite.mockRepo)
	suite.mockRepo.On("CountPrompts", true).Return(int64(99), nil)
	suite.mockRepo.On("ExistsByTextInsensitive", "One more?", uint(0)).Return(false, nil)
	suite.mockRepo.On("CreatePrompt", mock.AnythingOfType("*models.AEOPrompt")).Return(nil)

	created, err := suite.service.CreatePrompts([]string{"One more?"}, 1)

	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), created, 1)
}

func (suite *AEOServiceTestSuite) TestCreatePrompts_RejectsBlankAndOverlongText() {
	created, err := suite.service.CreatePrompts([]string{"   "}, 1)
	assert.Nil(suite.T(), created)
	assert.True(suite.T(), errors.Is(err, ErrAEOInvalidPrompt))

	created, err = suite.service.CreatePrompts([]string{strings.Repeat("x", 501)}, 1)
	assert.Nil(suite.T(), created)
	assert.True(suite.T(), errors.Is(err, ErrAEOInvalidPrompt))

	created, err = suite.service.CreatePrompts(nil, 1)
	assert.Nil(suite.T(), created)
	assert.True(suite.T(), errors.Is(err, ErrAEOInvalidPrompt))
}

func (suite *AEOServiceTestSuite) TestUpdatePrompt_Success() {
	existing := models.AEOPrompt{Text: "Old text", IsActive: true}
	existing.ID = 3
	newText := "New text"
	inactive := false

	suite.mockRepo.On("GetPromptByID", uint(3)).Return(&existing, nil)
	suite.mockRepo.On("ExistsByTextInsensitive", "New text", uint(3)).Return(false, nil)
	suite.mockRepo.On("UpdatePrompt", &existing).Return(nil)

	updated, err := suite.service.UpdatePrompt(3, &newText, &inactive)

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "New text", updated.Text)
	assert.False(suite.T(), updated.IsActive)
}

func (suite *AEOServiceTestSuite) TestUpdatePrompt_NotFound() {
	suite.mockRepo.On("GetPromptByID", uint(9)).Return(nil, gorm.ErrRecordNotFound)

	updated, err := suite.service.UpdatePrompt(9, nil, nil)

	assert.Nil(suite.T(), updated)
	assert.True(suite.T(), apperrors.IsNotFound(err))
}

func (suite *AEOServiceTestSuite) TestUpdatePrompt_RejectsDuplicateText() {
	existing := models.AEOPrompt{Text: "Old text", IsActive: true}
	existing.ID = 3
	newText := "Taken"

	suite.mockRepo.On("GetPromptByID", uint(3)).Return(&existing, nil)
	suite.mockRepo.On("ExistsByTextInsensitive", "Taken", uint(3)).Return(true, nil)

	updated, err := suite.service.UpdatePrompt(3, &newText, nil)

	assert.Nil(suite.T(), updated)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrDuplicatePrompt))
	suite.mockRepo.AssertNotCalled(suite.T(), "UpdatePrompt", mock.Anything)
}

// Reactivating a prompt adds one to the active set, so it has to pass the same
// cap a create does; deactivating never can.
func (suite *AEOServiceTestSuite) TestUpdatePrompt_ReactivationHitsCap() {
	existing := models.AEOPrompt{Text: "Dormant", IsActive: false}
	existing.ID = 4
	active := true

	suite.mockRepo.On("GetPromptByID", uint(4)).Return(&existing, nil)
	suite.mockRepo.On("CountPrompts", true).Return(int64(100), nil)

	updated, err := suite.service.UpdatePrompt(4, nil, &active)

	assert.Nil(suite.T(), updated)
	assert.True(suite.T(), errors.Is(err, ErrAEOPromptLimit))
	suite.mockRepo.AssertNotCalled(suite.T(), "UpdatePrompt", mock.Anything)
}

func (suite *AEOServiceTestSuite) TestUpdatePrompt_DeactivationSkipsCapCheck() {
	existing := models.AEOPrompt{Text: "Busy", IsActive: true}
	existing.ID = 5
	inactive := false

	suite.mockRepo.On("GetPromptByID", uint(5)).Return(&existing, nil)
	suite.mockRepo.On("UpdatePrompt", &existing).Return(nil)

	updated, err := suite.service.UpdatePrompt(5, nil, &inactive)

	assert.NoError(suite.T(), err)
	assert.False(suite.T(), updated.IsActive)
	suite.mockRepo.AssertNotCalled(suite.T(), "CountPrompts", mock.Anything)
}

func (suite *AEOServiceTestSuite) TestDeletePrompt_Success() {
	existing := models.AEOPrompt{Text: "Doomed"}
	existing.ID = 6

	suite.mockRepo.On("GetPromptByID", uint(6)).Return(&existing, nil)
	suite.mockRepo.On("DeletePrompt", uint(6)).Return(nil)

	assert.NoError(suite.T(), suite.service.DeletePrompt(6))
}

func (suite *AEOServiceTestSuite) TestDeletePrompt_NotFound() {
	suite.mockRepo.On("GetPromptByID", uint(6)).Return(nil, gorm.ErrRecordNotFound)

	err := suite.service.DeletePrompt(6)

	assert.True(suite.T(), apperrors.IsNotFound(err))
	suite.mockRepo.AssertNotCalled(suite.T(), "DeletePrompt", mock.Anything)
}

// ------------------------------------------------------- prompt generation ---

// scriptedAEOProvider answers Query with a canned reply, which is what prompt
// generation consumes. It records the meta-prompt so the test can assert the
// profile actually reached the model.
type scriptedAEOProvider struct {
	name  string
	reply string
	err   error
	asked string
}

func (p *scriptedAEOProvider) Name() string  { return p.name }
func (p *scriptedAEOProvider) Model() string { return "scripted" }
func (p *scriptedAEOProvider) Query(ctx context.Context, prompt string) (aeo.ProviderAnswer, error) {
	p.asked = prompt
	if p.err != nil {
		return aeo.ProviderAnswer{}, p.err
	}
	return aeo.ProviderAnswer{Text: p.reply}, nil
}

// withGenerator swaps in a scripted Anthropic engine alongside a decoy so the
// test also proves generation does not fall back to another provider.
func (suite *AEOServiceTestSuite) withGenerator(generator *scriptedAEOProvider) AEOService {
	providers := []aeo.Provider{
		&scriptedAEOProvider{name: "openai", reply: `["wrong engine answered"]`},
		generator,
	}
	return NewAEOService(suite.mockRepo, suite.executor, providers, suite.txManager)
}

func (suite *AEOServiceTestSuite) TestGeneratePrompts_JSONReply() {
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	generator := &scriptedAEOProvider{
		name:  "anthropic",
		reply: "Here you go:\n```json\n[\"Which CRM is best for small teams?\", \"What CRM integrates with email?\"]\n```",
	}

	prompts, err := suite.withGenerator(generator).GeneratePrompts(context.Background(), 10)

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), []string{
		"Which CRM is best for small teams?",
		"What CRM integrates with email?",
	}, prompts)
	assert.Contains(suite.T(), generator.asked, "Acme")
	assert.Contains(suite.T(), generator.asked, "Globex")
}

// A model that ignores the JSON instruction still has to be usable, so the
// parser falls back to one suggestion per line.
func (suite *AEOServiceTestSuite) TestGeneratePrompts_LineReplyIsCleanedAndDeduped() {
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	generator := &scriptedAEOProvider{
		name: "anthropic",
		reply: "Sure:\n" +
			"1. Which CRM is best for small teams?\n" +
			"- \"What CRM integrates with email?\"\n" +
			"• which crm is best for small teams?\n" +
			"\n",
	}

	prompts, err := suite.withGenerator(generator).GeneratePrompts(context.Background(), 10)

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), []string{
		"Which CRM is best for small teams?",
		"What CRM integrates with email?",
	}, prompts)
}

func (suite *AEOServiceTestSuite) TestGeneratePrompts_RespectsCount() {
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	generator := &scriptedAEOProvider{
		name:  "anthropic",
		reply: `["Which CRM is best?", "Which CRM is cheapest?", "Which CRM is fastest?"]`,
	}

	prompts, err := suite.withGenerator(generator).GeneratePrompts(context.Background(), 2)

	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), prompts, 2)
}

func (suite *AEOServiceTestSuite) TestGeneratePrompts_MissingProfile() {
	suite.mockRepo.On("GetProfile").Return(nil, gorm.ErrRecordNotFound)
	generator := &scriptedAEOProvider{name: "anthropic", reply: `["Which CRM is best?"]`}

	prompts, err := suite.withGenerator(generator).GeneratePrompts(context.Background(), 10)

	assert.Nil(suite.T(), prompts)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrProfileNotConfigured))
	assert.Empty(suite.T(), generator.asked)
}

// Generation runs on one named engine: configuring only OpenAI must not make
// the endpoint quietly answer from it.
func (suite *AEOServiceTestSuite) TestGeneratePrompts_WithoutGenerationProvider() {
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	svc := NewAEOService(suite.mockRepo, suite.executor,
		[]aeo.Provider{fakeAEOProvider{name: "openai", model: "gpt-4o-mini"}}, suite.txManager)

	prompts, err := svc.GeneratePrompts(context.Background(), 10)

	assert.Nil(suite.T(), prompts)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrGenerationProviderNotConfigured))
}

func (suite *AEOServiceTestSuite) TestGeneratePrompts_ProviderError() {
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	generator := &scriptedAEOProvider{name: "anthropic", err: errors.New("upstream is down")}

	prompts, err := suite.withGenerator(generator).GeneratePrompts(context.Background(), 10)

	assert.Nil(suite.T(), prompts)
	assert.EqualError(suite.T(), err, "upstream is down")
}

func (suite *AEOServiceTestSuite) TestGeneratePrompts_UnusableReply() {
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	generator := &scriptedAEOProvider{name: "anthropic", reply: "Sorry.\n"}

	prompts, err := suite.withGenerator(generator).GeneratePrompts(context.Background(), 10)

	assert.Nil(suite.T(), prompts)
	assert.Error(suite.T(), err)
}

func TestParseAEOGeneratedPrompts(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		limit    int
		expected []string
	}{
		{
			name:     "bare json array",
			raw:      `["Which CRM is best for startups?"]`,
			limit:    5,
			expected: []string{"Which CRM is best for startups?"},
		},
		{
			name:     "numbering is stripped only when followed by a separator",
			raw:      "1) Which CRM is best for startups?\n5 best CRM tools for sales teams?",
			limit:    5,
			expected: []string{"Which CRM is best for startups?", "5 best CRM tools for sales teams?"},
		},
		{
			name:     "over-long suggestions are dropped",
			raw:      strings.Repeat("a", aeoPromptTextMaxLength+1) + "\nWhich CRM is best for startups?",
			limit:    5,
			expected: []string{"Which CRM is best for startups?"},
		},
		{
			name:     "nothing usable",
			raw:      "ok\n\n",
			limit:    5,
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, parseAEOGeneratedPrompts(tt.raw, tt.limit))
		})
	}
}

// ------------------------------------------------------------------- runs ---

// expectStaleSweep arms the housekeeping call StartRun makes before the overlap
// guard. Every StartRun path that gets as far as the guard performs it.
func (suite *AEOServiceTestSuite) expectStaleSweep() {
	suite.mockRepo.On("MarkStaleRunsFailed", mock.AnythingOfType("time.Time")).Return(int64(0), nil)
}

func (suite *AEOServiceTestSuite) TestStartRun_Success() {
	profile := testAEOProfile()
	prompt := models.AEOPrompt{Text: "Which CRM?", IsActive: true}
	prompt.ID = 1

	suite.mockRepo.On("GetProfile").Return(profile, nil)
	suite.expectStaleSweep()
	suite.mockRepo.On("CountRunsByStatus", "running").Return(int64(0), nil)
	suite.mockRepo.On("ListActivePrompts").Return([]models.AEOPrompt{prompt}, nil)
	suite.mockRepo.On("CreateRun", mock.AnythingOfType("*models.AEORun")).Return(nil).
		Run(func(args mock.Arguments) {
			args.Get(0).(*models.AEORun).ID = 11
		})

	run, err := suite.service.StartRun(context.Background(), "manual", nil)

	suite.Require().NoError(err)
	assert.Equal(suite.T(), uint(11), run.ID)
	assert.Equal(suite.T(), "manual", run.Trigger)
	assert.Equal(suite.T(), "running", run.Status)
	// One task per prompt per configured provider.
	assert.Equal(suite.T(), 2, run.TotalQueries)
	assert.False(suite.T(), run.StartedAt.IsZero())

	select {
	case call := <-suite.executor.calls:
		assert.Equal(suite.T(), uint(11), call.run.ID)
		assert.Len(suite.T(), call.prompts, 1)
		assert.Equal(suite.T(), profile, call.profile)
	case <-time.After(2 * time.Second):
		suite.Fail("executor was never handed the run")
	}
}

func (suite *AEOServiceTestSuite) TestStartRun_NoProfile() {
	suite.mockRepo.On("GetProfile").Return(nil, gorm.ErrRecordNotFound)

	run, err := suite.service.StartRun(context.Background(), "manual", nil)

	assert.Nil(suite.T(), run)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrProfileNotConfigured))
	suite.mockRepo.AssertNotCalled(suite.T(), "CreateRun", mock.Anything)
}

func (suite *AEOServiceTestSuite) TestStartRun_NoProvidersConfigured() {
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)

	run, err := suite.withoutProviders().StartRun(context.Background(), "manual", nil)

	assert.Nil(suite.T(), run)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrNoProvidersConfigured))
	suite.mockRepo.AssertNotCalled(suite.T(), "CountRunsByStatus", mock.Anything)
}

func (suite *AEOServiceTestSuite) TestStartRun_RefusesOverlap() {
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	suite.expectStaleSweep()
	suite.mockRepo.On("CountRunsByStatus", "running").Return(int64(1), nil)

	run, err := suite.service.StartRun(context.Background(), "scheduled", nil)

	assert.Nil(suite.T(), run)
	assert.True(suite.T(), errors.Is(err, apperrors.ErrRunInProgress))
	suite.mockRepo.AssertNotCalled(suite.T(), "CreateRun", mock.Anything)
}

func (suite *AEOServiceTestSuite) TestStartRun_NoActivePrompts() {
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	suite.expectStaleSweep()
	suite.mockRepo.On("CountRunsByStatus", "running").Return(int64(0), nil)
	suite.mockRepo.On("ListActivePrompts").Return([]models.AEOPrompt{}, nil)

	run, err := suite.service.StartRun(context.Background(), "manual", nil)

	assert.Nil(suite.T(), run)
	assert.True(suite.T(), apperrors.IsNotFound(err))
	suite.mockRepo.AssertNotCalled(suite.T(), "CreateRun", mock.Anything)
}

// A run stranded in "running" by a crashed process must not block the module
// forever: the guard sweeps anything older than the staleness horizon first, so
// the next run starts normally instead of answering 409 until an operator edits
// the database by hand.
func (suite *AEOServiceTestSuite) TestStartRun_SweepsRunsStrandedByACrash() {
	prompt := models.AEOPrompt{Text: "Which CRM?", IsActive: true}
	prompt.ID = 1

	var cutoff time.Time
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	suite.mockRepo.On("MarkStaleRunsFailed", mock.AnythingOfType("time.Time")).
		Return(int64(1), nil).
		Run(func(args mock.Arguments) { cutoff = args.Get(0).(time.Time) })
	suite.mockRepo.On("CountRunsByStatus", "running").Return(int64(0), nil)
	suite.mockRepo.On("ListActivePrompts").Return([]models.AEOPrompt{prompt}, nil)
	suite.mockRepo.On("CreateRun", mock.AnythingOfType("*models.AEORun")).Return(nil).
		Run(func(args mock.Arguments) { args.Get(0).(*models.AEORun).ID = 3 })

	run, err := suite.service.StartRun(context.Background(), "scheduled", nil)

	suite.Require().NoError(err)
	assert.Equal(suite.T(), uint(3), run.ID)

	// The sweep must never touch a run that could still be alive.
	age := time.Now().UTC().Sub(cutoff)
	assert.InDelta(suite.T(), aeoRunStaleAfter.Seconds(), age.Seconds(), 5,
		"the staleness cutoff must be aeoRunStaleAfter in the past")

	select {
	case <-suite.executor.calls:
	case <-time.After(2 * time.Second):
		suite.Fail("executor was never handed the run")
	}
}

// The sweep is housekeeping. If it fails the guard is still correct, so the run
// request must not turn into a 500.
func (suite *AEOServiceTestSuite) TestStartRun_SweepFailureDoesNotBlockTheRun() {
	prompt := models.AEOPrompt{Text: "Which CRM?", IsActive: true}
	prompt.ID = 1

	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	suite.mockRepo.On("MarkStaleRunsFailed", mock.AnythingOfType("time.Time")).
		Return(int64(0), errors.New("connection refused"))
	suite.mockRepo.On("CountRunsByStatus", "running").Return(int64(0), nil)
	suite.mockRepo.On("ListActivePrompts").Return([]models.AEOPrompt{prompt}, nil)
	suite.mockRepo.On("CreateRun", mock.AnythingOfType("*models.AEORun")).Return(nil)

	run, err := suite.service.StartRun(context.Background(), "manual", nil)

	suite.Require().NoError(err)
	assert.NotNil(suite.T(), run)

	select {
	case <-suite.executor.calls:
	case <-time.After(2 * time.Second):
		suite.Fail("executor was never handed the run")
	}
}

func (suite *AEOServiceTestSuite) TestReconcileRunningRuns() {
	var cutoff time.Time
	suite.mockRepo.On("MarkStaleRunsFailed", mock.AnythingOfType("time.Time")).
		Return(int64(2), nil).
		Run(func(args mock.Arguments) { cutoff = args.Get(0).(time.Time) })

	recovered, err := suite.service.ReconcileRunningRuns()

	suite.Require().NoError(err)
	assert.Equal(suite.T(), int64(2), recovered)
	// At startup nothing can legitimately be running, so the cutoff is "now"
	// rather than a staleness horizon — otherwise a crash would still block
	// runs for hours after the restart that was supposed to clear it.
	assert.WithinDuration(suite.T(), time.Now().UTC(), cutoff, 5*time.Second)
}

func (suite *AEOServiceTestSuite) TestReconcileRunningRuns_PropagatesRepositoryFailure() {
	suite.mockRepo.On("MarkStaleRunsFailed", mock.AnythingOfType("time.Time")).
		Return(int64(0), errors.New("connection refused"))

	recovered, err := suite.service.ReconcileRunningRuns()

	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), int64(0), recovered)
}

// statefulAEORepo answers the overlap guard from real state instead of a canned
// value: CountRunsByStatus reports what CreateRun has actually inserted. Each
// call is individually locked, so the check-then-insert window between them is
// wide open — which is exactly the race the test is trying to provoke.
type statefulAEORepo struct {
	*mocks.AEORepository

	mu      sync.Mutex
	running int64
	created int
}

func (r *statefulAEORepo) CountRunsByStatus(string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running, nil
}

func (r *statefulAEORepo) CreateRun(run *models.AEORun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created++
	r.running++
	run.ID = uint(r.created)
	return nil
}

func (r *statefulAEORepo) MarkStaleRunsFailed(time.Time) (int64, error) { return 0, nil }

func (r *statefulAEORepo) createdRuns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.created
}

// TestStartRun_ConcurrentCallsStartExactlyOneRun covers the non-atomic guard:
// counting running rows and inserting one are two statements, so without a
// critical section two callers — the realistic case being a manual POST landing
// on the scheduled hour — can both read zero and both fan out prompts x
// providers external calls, doubling provider spend.
func (suite *AEOServiceTestSuite) TestStartRun_ConcurrentCallsStartExactlyOneRun() {
	prompt := models.AEOPrompt{Text: "Which CRM?", IsActive: true}
	prompt.ID = 1

	repo := &statefulAEORepo{AEORepository: new(mocks.AEORepository)}
	repo.On("GetProfile").Return(testAEOProfile(), nil)
	repo.On("ListActivePrompts").Return([]models.AEOPrompt{prompt}, nil)

	// A buffered executor so neither goroutine blocks handing off its run.
	executor := &fakeAEOExecutor{calls: make(chan executorCall, 8)}
	svc := NewAEOService(repo, executor, suite.providers, suite.txManager)

	const callers = 8
	var wg sync.WaitGroup
	results := make(chan error, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.StartRun(context.Background(), "manual", nil)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	started, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			started++
		case errors.Is(err, apperrors.ErrRunInProgress):
			refused++
		default:
			suite.Failf("unexpected error", "%v", err)
		}
	}

	assert.Equal(suite.T(), 1, started, "exactly one caller may win the guard")
	assert.Equal(suite.T(), callers-1, refused)
	assert.Equal(suite.T(), 1, repo.createdRuns(), "only one aeo_runs row may be inserted")
}

func (suite *AEOServiceTestSuite) TestStartRun_RejectsUnknownTrigger() {
	run, err := suite.service.StartRun(context.Background(), "cron", nil)

	assert.Nil(suite.T(), run)
	assert.True(suite.T(), errors.Is(err, ErrAEOInvalidTrigger))
	suite.mockRepo.AssertNotCalled(suite.T(), "GetProfile")
}

func (suite *AEOServiceTestSuite) TestListRuns() {
	run := models.AEORun{Trigger: "manual", Status: "completed"}
	run.ID = 2

	suite.mockRepo.On("ListRuns", 0, 20, "", "").Return([]models.AEORun{run}, nil)
	suite.mockRepo.On("CountRuns").Return(int64(1), nil)

	runs, total, err := suite.service.ListRuns(0, 20, "", "")

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(1), total)
	assert.Len(suite.T(), runs, 1)
}

func (suite *AEOServiceTestSuite) TestGetRun_NotFound() {
	suite.mockRepo.On("GetRunByID", uint(8)).Return(nil, gorm.ErrRecordNotFound)

	run, err := suite.service.GetRun(8)

	assert.Nil(suite.T(), run)
	assert.True(suite.T(), apperrors.IsNotFound(err))
}

func (suite *AEOServiceTestSuite) TestGetPromptAnswers_Success() {
	prompt := models.AEOPrompt{Text: "Which CRM?"}
	prompt.ID = 1
	runID := uint(3)
	answer := models.AEOAnswer{RunID: 3, PromptID: 1, Provider: "openai"}

	suite.mockRepo.On("GetPromptByID", uint(1)).Return(&prompt, nil)
	suite.mockRepo.On("ListAnswersByPrompt", uint(1), &runID, 0, 20).Return([]models.AEOAnswer{answer}, nil)
	suite.mockRepo.On("CountAnswersByPrompt", uint(1), &runID).Return(int64(1), nil)

	answers, total, err := suite.service.GetPromptAnswers(1, &runID, 0, 20)

	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(1), total)
	assert.Len(suite.T(), answers, 1)
}

func (suite *AEOServiceTestSuite) TestGetPromptAnswers_UnknownPrompt() {
	suite.mockRepo.On("GetPromptByID", uint(1)).Return(nil, gorm.ErrRecordNotFound)

	answers, total, err := suite.service.GetPromptAnswers(1, nil, 0, 20)

	assert.Nil(suite.T(), answers)
	assert.Zero(suite.T(), total)
	assert.True(suite.T(), apperrors.IsNotFound(err))
}

// -------------------------------------------------------------- dashboard ---

func (suite *AEOServiceTestSuite) TestDashboard_Arithmetic() {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	completed := time.Date(2026, 8, 3, 6, 30, 0, 0, time.UTC)
	run := models.AEORun{Status: "completed", StartedAt: completed.Add(-time.Hour), CompletedAt: &completed}

	facts := []models.AEOAnswerFact{
		{AnswerID: 1, PromptID: 1, Provider: "openai", CreatedAt: from.Add(2 * time.Hour), BrandMentioned: true, CompetitorMentions: map[string]int{"Globex": 2}},
		{AnswerID: 2, PromptID: 1, Provider: "anthropic", CreatedAt: from.Add(3 * time.Hour), CompetitorMentions: map[string]int{"Globex": 1, "Initech": 3}},
		{AnswerID: 3, PromptID: 1, Provider: "openai", CreatedAt: from.AddDate(0, 0, 2).Add(time.Hour), BrandMentioned: true},
		// An errored answer is recorded but never scored: it is not evidence
		// that the brand was absent.
		{AnswerID: 4, PromptID: 1, Provider: "openai", CreatedAt: from.AddDate(0, 0, 2).Add(2 * time.Hour), Errored: true},
	}

	suite.mockRepo.On("ListAnswerFacts", from, to).Return(facts, nil)
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	suite.mockRepo.On("GetLatestRun").Return(&run, nil)

	dashboard, err := suite.service.Dashboard(from, to)
	suite.Require().NoError(err)

	assert.Equal(suite.T(), "2026-08-01", dashboard.From)
	assert.Equal(suite.T(), "2026-08-04", dashboard.To)
	assert.Equal(suite.T(), 3, dashboard.Days)
	assert.Equal(suite.T(), int64(4), dashboard.TotalAnswers)
	assert.Equal(suite.T(), int64(1), dashboard.FailedAnswers)
	assert.Equal(suite.T(), int64(2), dashboard.BrandMentions)
	assert.Equal(suite.T(), 66.7, dashboard.Visibility)
	assert.Equal(suite.T(), &completed, dashboard.LastRunAt)

	suite.Require().Len(dashboard.ByProvider, 2)
	assert.Equal(suite.T(), "anthropic", dashboard.ByProvider[0].Provider)
	assert.Equal(suite.T(), float64(0), dashboard.ByProvider[0].Visibility)
	assert.Equal(suite.T(), "openai", dashboard.ByProvider[1].Provider)
	assert.Equal(suite.T(), int64(2), dashboard.ByProvider[1].Answers)
	assert.Equal(suite.T(), float64(100), dashboard.ByProvider[1].Visibility)

	// One point per day, including the empty middle day.
	suite.Require().Len(dashboard.Timeline, 3)
	assert.Equal(suite.T(), "2026-08-01", dashboard.Timeline[0].Day)
	assert.Equal(suite.T(), float64(50), dashboard.Timeline[0].Overall)
	assert.Equal(suite.T(), map[string]float64{"openai": 100, "anthropic": 0}, dashboard.Timeline[0].ByProvider)
	assert.Equal(suite.T(), "2026-08-02", dashboard.Timeline[1].Day)
	assert.Equal(suite.T(), float64(0), dashboard.Timeline[1].Overall)
	assert.NotNil(suite.T(), dashboard.Timeline[1].ByProvider)
	assert.Empty(suite.T(), dashboard.Timeline[1].ByProvider)
	assert.Equal(suite.T(), float64(100), dashboard.Timeline[2].Overall)

	// Brand first, competitors in profile order; shares sum to 100.
	suite.Require().Len(dashboard.ShareOfVoice, 3)
	assert.Equal(suite.T(), "Acme", dashboard.ShareOfVoice[0].Company)
	assert.True(suite.T(), dashboard.ShareOfVoice[0].IsBrand)
	assert.Equal(suite.T(), int64(2), dashboard.ShareOfVoice[0].Mentions)
	assert.Equal(suite.T(), float64(40), dashboard.ShareOfVoice[0].Share)
	assert.Equal(suite.T(), 66.7, dashboard.ShareOfVoice[0].Visibility)
	assert.Equal(suite.T(), "Globex", dashboard.ShareOfVoice[1].Company)
	assert.False(suite.T(), dashboard.ShareOfVoice[1].IsBrand)
	assert.Equal(suite.T(), float64(40), dashboard.ShareOfVoice[1].Share)
	assert.Equal(suite.T(), "Initech", dashboard.ShareOfVoice[2].Company)
	assert.Equal(suite.T(), float64(20), dashboard.ShareOfVoice[2].Share)

	var shareTotal float64
	for _, entry := range dashboard.ShareOfVoice {
		shareTotal += entry.Share
	}
	assert.InDelta(suite.T(), 100, shareTotal, 0.3)

	suite.Require().Len(dashboard.CompetitorTimeline, 3)
	assert.Equal(suite.T(), map[string]float64{"Acme": 50, "Globex": 100, "Initech": 50}, dashboard.CompetitorTimeline[0].ByCompany)
	assert.Empty(suite.T(), dashboard.CompetitorTimeline[1].ByCompany)
	assert.Equal(suite.T(), map[string]float64{"Acme": 100, "Globex": 0, "Initech": 0}, dashboard.CompetitorTimeline[2].ByCompany)
}

func (suite *AEOServiceTestSuite) TestDashboard_ZeroAnswers() {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 7)

	suite.mockRepo.On("ListAnswerFacts", from, to).Return([]models.AEOAnswerFact{}, nil)
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	suite.mockRepo.On("GetLatestRun").Return(nil, nil)

	dashboard, err := suite.service.Dashboard(from, to)
	suite.Require().NoError(err)

	assert.Equal(suite.T(), float64(0), dashboard.Visibility)
	assert.Equal(suite.T(), int64(0), dashboard.TotalAnswers)
	assert.Empty(suite.T(), dashboard.ByProvider)
	assert.Len(suite.T(), dashboard.Timeline, 7)
	assert.Nil(suite.T(), dashboard.LastRunAt)
	suite.Require().Len(dashboard.ShareOfVoice, 3)
	for _, entry := range dashboard.ShareOfVoice {
		assert.Equal(suite.T(), float64(0), entry.Share)
		assert.Equal(suite.T(), float64(0), entry.Visibility)
	}
}

// An unconfigured profile must not blank the dashboard: the metrics still add
// up, they simply have no brand to attribute a share of voice to.
func (suite *AEOServiceTestSuite) TestDashboard_WithoutProfile() {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 1)

	facts := []models.AEOAnswerFact{
		{AnswerID: 1, Provider: "openai", CreatedAt: from.Add(time.Hour), BrandMentioned: true, CompetitorMentions: map[string]int{"Globex": 1}},
		{AnswerID: 2, Provider: "openai", CreatedAt: from.Add(2 * time.Hour)},
	}

	suite.mockRepo.On("ListAnswerFacts", from, to).Return(facts, nil)
	suite.mockRepo.On("GetProfile").Return(nil, gorm.ErrRecordNotFound)
	suite.mockRepo.On("GetLatestRun").Return(nil, nil)

	dashboard, err := suite.service.Dashboard(from, to)
	suite.Require().NoError(err)

	assert.Equal(suite.T(), int64(1), dashboard.BrandMentions)
	assert.Equal(suite.T(), float64(50), dashboard.Visibility)
	suite.Require().Len(dashboard.ShareOfVoice, 1)
	assert.Equal(suite.T(), "Globex", dashboard.ShareOfVoice[0].Company)
	assert.False(suite.T(), dashboard.ShareOfVoice[0].IsBrand)
}

// A caller asking for a year gets the most recent 90 days rather than a query
// that scans the whole table.
func (suite *AEOServiceTestSuite) TestDashboard_ClampsRangeToNinetyDays() {
	to := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	from := to.AddDate(-1, 0, 0)
	clamped := to.Add(-90 * 24 * time.Hour)

	suite.mockRepo.On("ListAnswerFacts", clamped, to).Return([]models.AEOAnswerFact{}, nil)
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)
	suite.mockRepo.On("GetLatestRun").Return(nil, nil)

	dashboard, err := suite.service.Dashboard(from, to)

	suite.Require().NoError(err)
	assert.Equal(suite.T(), 90, dashboard.Days)
	assert.Equal(suite.T(), clamped.Format("2006-01-02"), dashboard.From)
	assert.Len(suite.T(), dashboard.Timeline, 90)
}

// -------------------------------------------------------------- citations ---

func (suite *AEOServiceTestSuite) TestCitations_Report() {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 30)

	rows := []models.AEOCitationAggRow{
		{Domain: "globex.com", CompetitorName: "Globex", Citations: 3, WithBrandMention: 1},
		{Domain: "acme.com", IsOwned: true, Citations: 6, WithBrandMention: 5},
		{Domain: "news.example.com", Citations: 1, WithBrandMention: 1},
	}

	suite.mockRepo.On("CountAnswersInRange", from, to).Return(int64(20), int64(8), nil)
	suite.mockRepo.On("CountAnswersWithCitations", from, to).Return(int64(7), nil)
	suite.mockRepo.On("CitationDomainStats", from, to).Return(rows, nil)
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)

	report, err := suite.service.Citations(from, to)
	suite.Require().NoError(err)

	assert.Equal(suite.T(), "2026-08-01", report.From)
	assert.Equal(suite.T(), int64(20), report.TotalAnswers)
	assert.Equal(suite.T(), int64(10), report.TotalCitations)
	assert.Equal(suite.T(), int64(7), report.AnswersWithCitations)
	assert.Equal(suite.T(), float64(60), report.OwnedCitationRate)

	// Brand first, then each tracked competitor in profile order — including
	// the one nobody cited, which is the point of the comparison.
	suite.Require().Len(report.ByCompany, 3)
	assert.Equal(suite.T(), "Acme", report.ByCompany[0].Company)
	assert.True(suite.T(), report.ByCompany[0].IsBrand)
	assert.Equal(suite.T(), int64(6), report.ByCompany[0].Citations)
	assert.Equal(suite.T(), float64(60), report.ByCompany[0].CitationRate)
	assert.Equal(suite.T(), 83.3, report.ByCompany[0].BrandMentionRate)
	assert.Equal(suite.T(), "Globex", report.ByCompany[1].Company)
	assert.Equal(suite.T(), float64(30), report.ByCompany[1].CitationRate)
	assert.Equal(suite.T(), 33.3, report.ByCompany[1].BrandMentionRate)
	assert.Equal(suite.T(), "Initech", report.ByCompany[2].Company)
	assert.Equal(suite.T(), int64(0), report.ByCompany[2].Citations)
	assert.Equal(suite.T(), float64(0), report.ByCompany[2].BrandMentionRate)

	// Citations descending, and an unattributed domain still shows up.
	suite.Require().Len(report.TopDomains, 3)
	assert.Equal(suite.T(), "acme.com", report.TopDomains[0].Domain)
	assert.Equal(suite.T(), "Acme", report.TopDomains[0].Company)
	assert.True(suite.T(), report.TopDomains[0].IsOwned)
	assert.Equal(suite.T(), "globex.com", report.TopDomains[1].Domain)
	assert.Equal(suite.T(), "news.example.com", report.TopDomains[2].Domain)
	assert.Equal(suite.T(), "", report.TopDomains[2].Company)
}

func (suite *AEOServiceTestSuite) TestCitations_NoCitations() {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 7)

	suite.mockRepo.On("CountAnswersInRange", from, to).Return(int64(0), int64(0), nil)
	suite.mockRepo.On("CountAnswersWithCitations", from, to).Return(int64(0), nil)
	suite.mockRepo.On("CitationDomainStats", from, to).Return([]models.AEOCitationAggRow{}, nil)
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)

	report, err := suite.service.Citations(from, to)
	suite.Require().NoError(err)

	assert.Equal(suite.T(), int64(0), report.TotalCitations)
	assert.Equal(suite.T(), float64(0), report.OwnedCitationRate)
	assert.Empty(suite.T(), report.TopDomains)
	suite.Require().Len(report.ByCompany, 3)
	for _, stat := range report.ByCompany {
		assert.Equal(suite.T(), float64(0), stat.CitationRate)
		assert.Equal(suite.T(), float64(0), stat.BrandMentionRate)
	}
}

func (suite *AEOServiceTestSuite) TestCitations_TopDomainsCappedAtTwenty() {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 7)

	rows := make([]models.AEOCitationAggRow, 0, 25)
	for i := 0; i < 25; i++ {
		rows = append(rows, models.AEOCitationAggRow{Domain: string(rune('a'+i)) + ".example.com", Citations: int64(i + 1)})
	}

	suite.mockRepo.On("CountAnswersInRange", from, to).Return(int64(50), int64(10), nil)
	suite.mockRepo.On("CountAnswersWithCitations", from, to).Return(int64(25), nil)
	suite.mockRepo.On("CitationDomainStats", from, to).Return(rows, nil)
	suite.mockRepo.On("GetProfile").Return(testAEOProfile(), nil)

	report, err := suite.service.Citations(from, to)

	suite.Require().NoError(err)
	assert.Len(suite.T(), report.TopDomains, 20)
	assert.Equal(suite.T(), int64(25), report.TopDomains[0].Citations)
	assert.Equal(suite.T(), int64(6), report.TopDomains[19].Citations)
}

// -------------------------------------------------------------- providers ---

func (suite *AEOServiceTestSuite) TestProviders_ReportsConfiguredEngines() {
	statuses := suite.service.Providers()

	suite.Require().Len(statuses, 2)
	assert.Equal(suite.T(), "openai", statuses[0].Name)
	assert.Equal(suite.T(), "gpt-4o-mini", statuses[0].Model)
	assert.True(suite.T(), statuses[0].Configured)
	assert.Equal(suite.T(), "anthropic", statuses[1].Name)
}

func (suite *AEOServiceTestSuite) TestProviders_EmptyWhenNothingConfigured() {
	statuses := suite.withoutProviders().Providers()

	assert.NotNil(suite.T(), statuses)
	assert.Empty(suite.T(), statuses)
}

// ---------------------------------------------------------------- helpers ---

func TestAEOServiceSuite(t *testing.T) {
	suite.Run(t, new(AEOServiceTestSuite))
}

func TestAEOPercentRounding(t *testing.T) {
	assert.Equal(t, float64(0), aeoPercent(0, 0))
	assert.Equal(t, float64(0), aeoPercent(5, 0))
	assert.Equal(t, float64(100), aeoPercent(3, 3))
	assert.Equal(t, 66.7, aeoPercent(2, 3))
	assert.Equal(t, 33.3, aeoPercent(1, 3))
	assert.Equal(t, 12.5, aeoPercent(1, 8))
}

func TestNormalizeAEORange(t *testing.T) {
	to := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)

	from, gotTo := normalizeAEORange(to.AddDate(0, 0, -30), to)
	assert.Equal(t, to.AddDate(0, 0, -30), from)
	assert.Equal(t, to, gotTo)
	assert.Equal(t, 30, aeoRangeDays(from, gotTo))

	// Wider than the cap: the end is kept, the oldest days are dropped.
	from, gotTo = normalizeAEORange(to.AddDate(-2, 0, 0), to)
	assert.Equal(t, to.Add(-90*24*time.Hour), from)
	assert.Equal(t, 90, aeoRangeDays(from, gotTo))

	// Degenerate window: widened to a single day so the per-day loop always
	// produces at least one bucket.
	from, gotTo = normalizeAEORange(to, to)
	assert.Equal(t, to.Add(24*time.Hour), gotTo)
	assert.Equal(t, 1, aeoRangeDays(from, gotTo))
}

func TestAEONormalizeProfileDomain(t *testing.T) {
	cases := map[string]string{
		"  ACME.com ":                 "acme.com",
		"www.acme.com":                "acme.com",
		"https://WWW.Acme.com/blog":   "acme.com",
		"http://acme.com:8080/a?b=c":  "acme.com:8080",
		"mailto:sales@acme.com":       "acme.com",
		"":                            "",
		"   ":                         "",
		"https://sub.acme.co.uk/x/y/": "sub.acme.co.uk",
	}
	for input, want := range cases {
		assert.Equal(t, want, aeoNormalizeProfileDomain(input), "input %q", input)
	}
}
