package service

import (
	"context"
	"errors"
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
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fakeAEOConfigSource stands in for the resolution main.go wires: it returns
// whatever configuration the test currently wants and counts how often it was
// asked, which is how the tests prove the service re-resolves rather than
// caching a boot-time snapshot. The keys are obviously fake.
type fakeAEOConfigSource struct {
	mu     sync.Mutex
	cfg    config.AEOConfig
	called int
}

func (s *fakeAEOConfigSource) source() config.AEOConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.called++
	return s.cfg
}

func (s *fakeAEOConfigSource) set(cfg config.AEOConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

func (s *fakeAEOConfigSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.called
}

// rebindableExecutor is an Executor that can be rebound to another provider
// set, like the real engine. It reports the set it was executed with.
type rebindableExecutor struct {
	providers []aeo.Provider
	calls     chan []aeo.Provider
}

func newRebindableExecutor() *rebindableExecutor {
	return &rebindableExecutor{calls: make(chan []aeo.Provider, 1)}
}

func (e *rebindableExecutor) Execute(ctx context.Context, run *models.AEORun, prompts []models.AEOPrompt, profile *models.AEOProfile) error {
	e.calls <- e.providers
	return nil
}

func (e *rebindableExecutor) WithProviders(providers []aeo.Provider) aeo.Executor {
	return &rebindableExecutor{providers: providers, calls: e.calls}
}

// aeoConfigSourceFixture is the minimum a run needs: a mocked repository and a
// transaction manager over an empty in-memory database.
type aeoConfigSourceFixture struct {
	repo      *mocks.AEORepository
	txManager *utils.TransactionManager
	source    *fakeAEOConfigSource
}

func newAEOConfigSourceFixture(t *testing.T) *aeoConfigSourceFixture {
	t.Helper()
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	return &aeoConfigSourceFixture{
		repo:      new(mocks.AEORepository),
		txManager: utils.NewTransactionManager(db),
		source:    &fakeAEOConfigSource{},
	}
}

// expectRunCreation arms every repository call a successful StartRun makes.
func (f *aeoConfigSourceFixture) expectRunCreation(runID uint, prompts ...models.AEOPrompt) {
	f.repo.On("GetProfile").Return(testAEOProfile(), nil)
	f.repo.On("MarkStaleRunsFailed", mock.AnythingOfType("time.Time")).Return(int64(0), nil)
	f.repo.On("CountRunsByStatus", "running").Return(int64(0), nil)
	f.repo.On("ListActivePrompts").Return(prompts, nil)
	f.repo.On("CreateRun", mock.AnythingOfType("*models.AEORun")).Return(nil).
		Run(func(args mock.Arguments) {
			args.Get(0).(*models.AEORun).ID = runID
		})
}

func aeoPromptFixture(id uint) models.AEOPrompt {
	prompt := models.AEOPrompt{Text: "Which CRM should a small team pick?", IsActive: true}
	prompt.ID = id
	return prompt
}

// The status roster is rebuilt on every read, so an administrator who stores a
// key sees the engine flip to configured without the process restarting.
func TestAEOService_ProviderStatusesAreResolvedOnEveryRead(t *testing.T) {
	fixture := newAEOConfigSourceFixture(t)
	service := NewAEOService(fixture.repo, newFakeAEOExecutor(), nil, fixture.txManager,
		WithAEOConfigSource(fixture.source.source))

	statuses := service.Providers()
	require.NotEmpty(t, statuses)
	assert.Equal(t, 1, fixture.source.callCount())
	assert.False(t, providerStatusConfigured(statuses, aeo.ProviderGemini))

	fixture.source.set(config.AEOConfig{GeminiAPIKey: "not-a-real-gemini-key", GeminiModel: "gemini-test"})

	statuses = service.Providers()
	assert.Equal(t, 2, fixture.source.callCount())
	assert.True(t, providerStatusConfigured(statuses, aeo.ProviderGemini))
	assert.False(t, providerStatusConfigured(statuses, aeo.ProviderOpenAI))

	// The roster always names every engine, configured or not.
	assert.Len(t, statuses, 6)
}

func providerStatusConfigured(statuses []models.AEOProviderStatus, name string) bool {
	for _, status := range statuses {
		if status.Name == name {
			return status.Configured
		}
	}
	return false
}

// A service that started with no credentials must start running once a key is
// stored, without being reconstructed.
func TestAEOService_RunCreationResolvesProvidersFreshly(t *testing.T) {
	fixture := newAEOConfigSourceFixture(t)
	executor := newRebindableExecutor()
	service := NewAEOService(fixture.repo, executor, nil, fixture.txManager,
		WithAEOConfigSource(fixture.source.source))

	fixture.repo.On("GetProfile").Return(testAEOProfile(), nil)

	run, err := service.StartRun(context.Background(), "manual", nil)
	assert.Nil(t, run)
	assert.True(t, errors.Is(err, apperrors.ErrNoProvidersConfigured), "expected ErrNoProvidersConfigured, got %v", err)
	fixture.repo.AssertNotCalled(t, "CreateRun", mock.Anything)
	callsBefore := fixture.source.callCount()
	assert.Positive(t, callsBefore)

	// The administrator stores two keys while the process keeps running.
	fixture.source.set(config.AEOConfig{
		GeminiAPIKey:   "not-a-real-gemini-key",
		MoonshotAPIKey: "not-a-real-moonshot-key",
	})

	fixture.expectRunCreation(21, aeoPromptFixture(1), aeoPromptFixture(2))

	run, err = service.StartRun(context.Background(), "manual", nil)
	require.NoError(t, err)
	assert.Greater(t, fixture.source.callCount(), callsBefore, "run creation did not re-read the configuration")
	// Two prompts against the two engines configured at run creation.
	assert.Equal(t, 4, run.TotalQueries)

	select {
	case providers := <-executor.calls:
		require.Len(t, providers, 2)
		assert.Equal(t, aeo.ProviderGemini, providers[0].Name())
		assert.Equal(t, aeo.ProviderKimi, providers[1].Name())
	case <-time.After(2 * time.Second):
		t.Fatal("executor was never handed the run")
	}
}

// A scheduled run creates its run through the same path, so it resolves the
// configuration the same way.
func TestAEOService_ScheduledRunCreationResolvesProvidersFreshly(t *testing.T) {
	fixture := newAEOConfigSourceFixture(t)
	executor := newRebindableExecutor()
	fixture.source.set(config.AEOConfig{GeminiAPIKey: "not-a-real-gemini-key"})
	service := NewAEOService(fixture.repo, executor, nil, fixture.txManager,
		WithAEOConfigSource(fixture.source.source))

	fixture.expectRunCreation(22, aeoPromptFixture(1))

	run, err := service.StartRun(context.Background(), "scheduled", nil)
	require.NoError(t, err)
	assert.Equal(t, "scheduled", run.Trigger)
	assert.Equal(t, 1, run.TotalQueries)
	assert.Positive(t, fixture.source.callCount())

	select {
	case providers := <-executor.calls:
		require.Len(t, providers, 1)
		assert.Equal(t, aeo.ProviderGemini, providers[0].Name())
	case <-time.After(2 * time.Second):
		t.Fatal("executor was never handed the run")
	}
}

// Resolving per run changes which engines answer, not the guards around a run:
// a run already in progress still wins.
func TestAEOService_ConfigSourceKeepsTheOverlapGuard(t *testing.T) {
	fixture := newAEOConfigSourceFixture(t)
	fixture.source.set(config.AEOConfig{GeminiAPIKey: "not-a-real-gemini-key"})
	service := NewAEOService(fixture.repo, newRebindableExecutor(), nil, fixture.txManager,
		WithAEOConfigSource(fixture.source.source))

	fixture.repo.On("GetProfile").Return(testAEOProfile(), nil)
	fixture.repo.On("MarkStaleRunsFailed", mock.AnythingOfType("time.Time")).Return(int64(0), nil)
	fixture.repo.On("CountRunsByStatus", "running").Return(int64(1), nil)

	run, err := service.StartRun(context.Background(), "manual", nil)

	assert.Nil(t, run)
	assert.True(t, errors.Is(err, apperrors.ErrRunInProgress), "expected ErrRunInProgress, got %v", err)
	fixture.repo.AssertNotCalled(t, "CreateRun", mock.Anything)
}

// Clearing every stored key puts the module back into the 503 state on the next
// run: resolution has no memory of the engines that used to be configured.
func TestAEOService_ClearedKeysStopTheModuleWithoutARestart(t *testing.T) {
	fixture := newAEOConfigSourceFixture(t)
	fixture.source.set(config.AEOConfig{GeminiAPIKey: "not-a-real-gemini-key"})
	service := NewAEOService(fixture.repo, newRebindableExecutor(), nil, fixture.txManager,
		WithAEOConfigSource(fixture.source.source))

	fixture.repo.On("GetProfile").Return(testAEOProfile(), nil)

	fixture.source.set(config.AEOConfig{})

	run, err := service.StartRun(context.Background(), "manual", nil)

	assert.Nil(t, run)
	assert.True(t, errors.Is(err, apperrors.ErrNoProvidersConfigured), "expected ErrNoProvidersConfigured, got %v", err)
	assert.False(t, providerStatusConfigured(service.Providers(), aeo.ProviderGemini))
}

// Without the option nothing changes: the service keeps the engines and the
// roster it was constructed with, and consults no source at all.
func TestAEOService_WithoutAConfigSourceUsesTheConstructedProviders(t *testing.T) {
	fixture := newAEOConfigSourceFixture(t)
	executor := newRebindableExecutor()
	providers := []aeo.Provider{fakeAEOProvider{name: "openai", model: "test-model"}}
	statuses := []models.AEOProviderStatus{{Name: "openai", Model: "test-model", Configured: true}}
	service := NewAEOService(fixture.repo, executor, providers, fixture.txManager,
		WithAEOProviderStatuses(statuses))

	assert.Equal(t, statuses, service.Providers())
	assert.Equal(t, 0, fixture.source.callCount())

	fixture.expectRunCreation(23, aeoPromptFixture(1))

	run, err := service.StartRun(context.Background(), "manual", nil)
	require.NoError(t, err)
	assert.Equal(t, 1, run.TotalQueries)

	select {
	case executed := <-executor.calls:
		// The executor was not rebound, so it still holds the set it was
		// built with — nil here, since this test never gave it one.
		assert.Nil(t, executed)
	case <-time.After(2 * time.Second):
		t.Fatal("executor was never handed the run")
	}
}
