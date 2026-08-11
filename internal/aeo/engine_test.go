package aeo

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// fakeProvider is a scripted answer engine. It is deliberately not a mockery
// mock: internal/mocks imports this package for the Provider and Executor
// mocks, so importing it back here would be an import cycle.
type fakeProvider struct {
	name    string
	model   string
	answer  ProviderAnswer
	err     error
	panics  bool
	delay   time.Duration
	calls   atomic.Int32
	inFlght atomic.Int32
	peak    atomic.Int32
}

func (p *fakeProvider) Name() string  { return p.name }
func (p *fakeProvider) Model() string { return p.model }

func (p *fakeProvider) Query(ctx context.Context, prompt string) (ProviderAnswer, error) {
	p.calls.Add(1)
	current := p.inFlght.Add(1)
	for {
		peak := p.peak.Load()
		if current <= peak || p.peak.CompareAndSwap(peak, current) {
			break
		}
	}
	defer p.inFlght.Add(-1)

	if p.panics {
		panic("provider exploded")
	}
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return ProviderAnswer{}, ctx.Err()
		}
	}
	if p.err != nil {
		return ProviderAnswer{}, p.err
	}
	return p.answer, nil
}

// fakeAEORepo records what the engine writes. Only the handful of methods the
// engine actually calls are implemented; the rest satisfy the interface and
// fail loudly if the engine ever grows a dependency on them.
type fakeAEORepo struct {
	mu sync.Mutex

	answers   []models.AEOAnswer
	citations [][]models.AEOCitation
	runs      []models.AEORun

	createErr error
	updateErr error
}

var _ repository.AEORepository = (*fakeAEORepo)(nil)

func (r *fakeAEORepo) CreateAnswerWithCitations(answer *models.AEOAnswer, citations []models.AEOCitation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	answer.ID = uint(len(r.answers) + 1)
	r.answers = append(r.answers, *answer)
	r.citations = append(r.citations, citations)
	return nil
}

func (r *fakeAEORepo) UpdateRun(run *models.AEORun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return r.updateErr
	}
	r.runs = append(r.runs, *run)
	return nil
}

func (r *fakeAEORepo) snapshot() ([]models.AEOAnswer, [][]models.AEOCitation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	answers := make([]models.AEOAnswer, len(r.answers))
	copy(answers, r.answers)
	citations := make([][]models.AEOCitation, len(r.citations))
	copy(citations, r.citations)
	return answers, citations
}

func (r *fakeAEORepo) unexpected(method string) error {
	return errors.New("fakeAEORepo: unexpected call to " + method)
}

func (r *fakeAEORepo) GetProfile() (*models.AEOProfile, error) {
	return nil, r.unexpected("GetProfile")
}
func (r *fakeAEORepo) UpsertProfile(*models.AEOProfile) error { return r.unexpected("UpsertProfile") }
func (r *fakeAEORepo) CreatePrompt(*models.AEOPrompt) error   { return r.unexpected("CreatePrompt") }
func (r *fakeAEORepo) GetPromptByID(uint) (*models.AEOPrompt, error) {
	return nil, r.unexpected("GetPromptByID")
}
func (r *fakeAEORepo) UpdatePrompt(*models.AEOPrompt) error { return r.unexpected("UpdatePrompt") }
func (r *fakeAEORepo) DeletePrompt(uint) error              { return r.unexpected("DeletePrompt") }
func (r *fakeAEORepo) ListPrompts(bool, int, int, string, string) ([]models.AEOPrompt, error) {
	return nil, r.unexpected("ListPrompts")
}
func (r *fakeAEORepo) CountPrompts(bool) (int64, error) { return 0, r.unexpected("CountPrompts") }
func (r *fakeAEORepo) ListActivePrompts() ([]models.AEOPrompt, error) {
	return nil, r.unexpected("ListActivePrompts")
}
func (r *fakeAEORepo) ExistsByTextInsensitive(string, uint) (bool, error) {
	return false, r.unexpected("ExistsByTextInsensitive")
}
func (r *fakeAEORepo) CreateRun(*models.AEORun) error { return r.unexpected("CreateRun") }
func (r *fakeAEORepo) GetRunByID(uint) (*models.AEORun, error) {
	return nil, r.unexpected("GetRunByID")
}
func (r *fakeAEORepo) ListRuns(int, int, string, string) ([]models.AEORun, error) {
	return nil, r.unexpected("ListRuns")
}
func (r *fakeAEORepo) CountRuns() (int64, error) { return 0, r.unexpected("CountRuns") }
func (r *fakeAEORepo) GetLatestRun() (*models.AEORun, error) {
	return nil, r.unexpected("GetLatestRun")
}
func (r *fakeAEORepo) CountRunsByStatus(string) (int64, error) {
	return 0, r.unexpected("CountRunsByStatus")
}
func (r *fakeAEORepo) MarkStaleRunsFailed(time.Time) (int64, error) {
	return 0, r.unexpected("MarkStaleRunsFailed")
}
func (r *fakeAEORepo) ListAnswersByPrompt(uint, *uint, int, int) ([]models.AEOAnswer, error) {
	return nil, r.unexpected("ListAnswersByPrompt")
}
func (r *fakeAEORepo) CountAnswersByPrompt(uint, *uint) (int64, error) {
	return 0, r.unexpected("CountAnswersByPrompt")
}
func (r *fakeAEORepo) ListAnswersByRun(uint) ([]models.AEOAnswer, error) {
	return nil, r.unexpected("ListAnswersByRun")
}
func (r *fakeAEORepo) ListAnswerFacts(time.Time, time.Time) ([]models.AEOAnswerFact, error) {
	return nil, r.unexpected("ListAnswerFacts")
}
func (r *fakeAEORepo) PromptVisibility(time.Time, time.Time, []uint) (map[uint]models.AEOPromptVisibility, error) {
	return nil, r.unexpected("PromptVisibility")
}
func (r *fakeAEORepo) CitationDomainStats(time.Time, time.Time) ([]models.AEOCitationAggRow, error) {
	return nil, r.unexpected("CitationDomainStats")
}
func (r *fakeAEORepo) CountAnswersInRange(time.Time, time.Time) (int64, int64, error) {
	return 0, 0, r.unexpected("CountAnswersInRange")
}
func (r *fakeAEORepo) CountAnswersWithCitations(time.Time, time.Time) (int64, error) {
	return 0, r.unexpected("CountAnswersWithCitations")
}
func (r *fakeAEORepo) WithTx(*gorm.DB) repository.AEORepository { return r }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func enginePrompts(texts ...string) []models.AEOPrompt {
	prompts := make([]models.AEOPrompt, 0, len(texts))
	for i, text := range texts {
		prompt := models.AEOPrompt{Text: text, IsActive: true}
		prompt.ID = uint(i + 1)
		prompts = append(prompts, prompt)
	}
	return prompts
}

func newTestRun() *models.AEORun {
	run := &models.AEORun{
		Trigger:   TriggerManual,
		Status:    RunStatusRunning,
		StartedAt: time.Now(),
	}
	run.ID = 42
	return run
}

func TestNewEngineAppliesDefaults(t *testing.T) {
	engine := NewEngine(&fakeAEORepo{}, nil, EngineOptions{})
	assert.Equal(t, defaultConcurrency, engine.opts.Concurrency)
	assert.Equal(t, defaultQueryTimeout, engine.opts.QueryTimeout)

	engine = NewEngine(&fakeAEORepo{}, nil, EngineOptions{Concurrency: -3, QueryTimeout: -time.Second})
	assert.Equal(t, defaultConcurrency, engine.opts.Concurrency)
	assert.Equal(t, defaultQueryTimeout, engine.opts.QueryTimeout)

	engine = NewEngine(&fakeAEORepo{}, nil, EngineOptions{Concurrency: 2, QueryTimeout: 5 * time.Second})
	assert.Equal(t, 2, engine.opts.Concurrency)
	assert.Equal(t, 5*time.Second, engine.opts.QueryTimeout)
}

func TestEngineExecuteRunStatuses(t *testing.T) {
	tests := []struct {
		name          string
		providers     []Provider
		prompts       []models.AEOPrompt
		wantStatus    string
		wantTotal     int
		wantFailed    int
		wantAnswerLen int
	}{
		{
			name: "all queries succeed",
			providers: []Provider{
				&fakeProvider{name: "alpha", model: "a1", answer: ProviderAnswer{Text: "Acme wins."}},
				&fakeProvider{name: "beta", model: "b1", answer: ProviderAnswer{Text: "Acme wins."}},
			},
			prompts:       enginePrompts("q1", "q2"),
			wantStatus:    RunStatusCompleted,
			wantTotal:     4,
			wantFailed:    0,
			wantAnswerLen: 4,
		},
		{
			name: "some queries fail",
			providers: []Provider{
				&fakeProvider{name: "alpha", model: "a1", answer: ProviderAnswer{Text: "Acme wins."}},
				&fakeProvider{name: "beta", model: "b1", err: errors.New("beta: upstream exploded")},
			},
			prompts:       enginePrompts("q1", "q2"),
			wantStatus:    RunStatusPartial,
			wantTotal:     4,
			wantFailed:    2,
			wantAnswerLen: 4,
		},
		{
			name: "every query fails",
			providers: []Provider{
				&fakeProvider{name: "alpha", model: "a1", err: errors.New("alpha: down")},
			},
			prompts:       enginePrompts("q1", "q2"),
			wantStatus:    RunStatusFailed,
			wantTotal:     2,
			wantFailed:    2,
			wantAnswerLen: 2,
		},
		{
			name:          "no prompts is a completed no-op",
			providers:     []Provider{&fakeProvider{name: "alpha", model: "a1"}},
			prompts:       nil,
			wantStatus:    RunStatusCompleted,
			wantTotal:     0,
			wantFailed:    0,
			wantAnswerLen: 0,
		},
		{
			name:          "no providers is a completed no-op",
			providers:     nil,
			prompts:       enginePrompts("q1"),
			wantStatus:    RunStatusCompleted,
			wantTotal:     0,
			wantFailed:    0,
			wantAnswerLen: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeAEORepo{}
			run := newTestRun()

			engine := NewEngine(repo, tc.providers, EngineOptions{})
			require.NoError(t, engine.Execute(context.Background(), run, tc.prompts, testProfile()))

			assert.Equal(t, tc.wantStatus, run.Status)
			assert.Equal(t, tc.wantTotal, run.TotalQueries)
			assert.Equal(t, tc.wantFailed, run.FailedQueries)
			require.NotNil(t, run.CompletedAt)

			answers, _ := repo.snapshot()
			assert.Len(t, answers, tc.wantAnswerLen)

			require.Len(t, repo.runs, 1, "the run row is finalized exactly once")
			assert.Equal(t, tc.wantStatus, repo.runs[0].Status)
		})
	}
}

func TestEngineRecordsSuccessfulAnswerDetail(t *testing.T) {
	repo := &fakeAEORepo{}
	provider := &fakeProvider{
		name:  ProviderPerplexity,
		model: "sonar",
		answer: ProviderAnswer{
			Text:      "Acme is strong; Globex is cheaper. See https://globex.com/pricing.",
			Citations: []string{"https://acme.com/compare"},
		},
	}
	run := newTestRun()
	prompts := enginePrompts("Which CRM?")

	engine := NewEngine(repo, []Provider{provider}, EngineOptions{})
	require.NoError(t, engine.Execute(context.Background(), run, prompts, testProfile()))

	answers, citations := repo.snapshot()
	require.Len(t, answers, 1)
	answer := answers[0]

	assert.Equal(t, run.ID, answer.RunID)
	assert.Equal(t, prompts[0].ID, answer.PromptID)
	assert.Equal(t, ProviderPerplexity, answer.Provider)
	assert.Equal(t, "sonar", answer.Model)
	assert.Equal(t, 1, answer.Attempt, "v1 issues exactly one attempt per prompt and provider")
	assert.Equal(t, provider.answer.Text, answer.AnswerText)
	assert.True(t, answer.BrandMentioned)
	assert.Equal(t, 0, answer.FirstMentionPos)
	// Two hits: the prose mention and the host label inside the cited URL. A
	// company named in a link it owns is still named in the answer, so this is
	// counted rather than filtered.
	assert.Equal(t, map[string]int{"Globex": 2}, answer.CompetitorMentions)
	assert.Empty(t, answer.Error)
	assert.GreaterOrEqual(t, answer.LatencyMs, 0)

	require.Len(t, citations, 1)
	require.Len(t, citations[0], 2)
	assert.Equal(t, "https://acme.com/compare", citations[0][0].URL)
	assert.True(t, citations[0][0].IsOwned)
	assert.Equal(t, "https://globex.com/pricing", citations[0][1].URL)
	assert.Equal(t, "Globex", citations[0][1].CompetitorName)
}

func TestEngineRecordsFailedQueryAsAnAnswerRow(t *testing.T) {
	repo := &fakeAEORepo{}
	provider := &fakeProvider{name: "alpha", model: "a1", err: errors.New("alpha: 503 unavailable")}
	run := newTestRun()

	engine := NewEngine(repo, []Provider{provider}, EngineOptions{})
	require.NoError(t, engine.Execute(context.Background(), run, enginePrompts("q1"), testProfile()))

	answers, citations := repo.snapshot()
	require.Len(t, answers, 1, "a failed provider call is recorded, never dropped")
	assert.Equal(t, "alpha: 503 unavailable", answers[0].Error)
	assert.Empty(t, answers[0].AnswerText)
	assert.False(t, answers[0].BrandMentioned)
	assert.Equal(t, -1, answers[0].FirstMentionPos)
	assert.Equal(t, map[string]int{}, answers[0].CompetitorMentions)
	assert.Empty(t, citations[0])

	assert.Equal(t, RunStatusFailed, run.Status)
	assert.Equal(t, 1, run.FailedQueries)
}

func TestEngineCountsPersistenceFailuresAsFailedQueries(t *testing.T) {
	repo := &fakeAEORepo{createErr: errors.New("database is gone")}
	provider := &fakeProvider{name: "alpha", model: "a1", answer: ProviderAnswer{Text: "Acme."}}
	run := newTestRun()

	engine := NewEngine(repo, []Provider{provider}, EngineOptions{})
	require.NoError(t, engine.Execute(context.Background(), run, enginePrompts("q1", "q2"), testProfile()))

	assert.Equal(t, RunStatusFailed, run.Status,
		"a query whose answer could not be stored is a lost query")
	assert.Equal(t, 2, run.FailedQueries)
}

func TestEngineSurvivesAPanickingProvider(t *testing.T) {
	repo := &fakeAEORepo{}
	providers := []Provider{
		&fakeProvider{name: "sane", model: "s1", answer: ProviderAnswer{Text: "Acme."}},
		&fakeProvider{name: "boom", model: "b1", panics: true},
	}
	run := newTestRun()

	engine := NewEngine(repo, providers, EngineOptions{})
	require.NoError(t, engine.Execute(context.Background(), run, enginePrompts("q1"), testProfile()))

	assert.Equal(t, RunStatusPartial, run.Status)
	assert.Equal(t, 2, run.TotalQueries)
	assert.Equal(t, 1, run.FailedQueries)

	answers, _ := repo.snapshot()
	require.Len(t, answers, 2, "a panicking provider still records its lost query")

	byProvider := map[string]models.AEOAnswer{}
	for _, answer := range answers {
		byProvider[answer.Provider] = answer
	}
	assert.Empty(t, byProvider["sane"].Error)
	assert.Contains(t, byProvider["boom"].Error, "panicked")
	assert.Equal(t, -1, byProvider["boom"].FirstMentionPos)
}

func TestEngineAppliesPerQueryTimeout(t *testing.T) {
	repo := &fakeAEORepo{}
	provider := &fakeProvider{name: "slow", model: "s1", delay: 2 * time.Second}
	run := newTestRun()

	engine := NewEngine(repo, []Provider{provider}, EngineOptions{QueryTimeout: 20 * time.Millisecond})

	started := time.Now()
	require.NoError(t, engine.Execute(context.Background(), run, enginePrompts("q1"), testProfile()))
	assert.Less(t, time.Since(started), time.Second, "the per-query deadline must cut the call short")

	answers, _ := repo.snapshot()
	require.Len(t, answers, 1)
	assert.Contains(t, answers[0].Error, context.DeadlineExceeded.Error())
	assert.Equal(t, RunStatusFailed, run.Status)
}

func TestEngineHonoursACancelledParentContext(t *testing.T) {
	repo := &fakeAEORepo{}
	provider := &fakeProvider{name: "slow", model: "s1", delay: 2 * time.Second}
	run := newTestRun()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	engine := NewEngine(repo, []Provider{provider}, EngineOptions{})
	require.NoError(t, engine.Execute(ctx, run, enginePrompts("q1"), testProfile()))

	answers, _ := repo.snapshot()
	require.Len(t, answers, 1)
	assert.Contains(t, answers[0].Error, context.Canceled.Error())
}

func TestEngineBoundsConcurrency(t *testing.T) {
	repo := &fakeAEORepo{}
	provider := &fakeProvider{
		name:   "alpha",
		model:  "a1",
		delay:  15 * time.Millisecond,
		answer: ProviderAnswer{Text: "Acme."},
	}
	run := newTestRun()

	prompts := enginePrompts("q1", "q2", "q3", "q4", "q5", "q6", "q7", "q8", "q9", "q10")

	engine := NewEngine(repo, []Provider{provider}, EngineOptions{Concurrency: 3})
	require.NoError(t, engine.Execute(context.Background(), run, prompts, testProfile()))

	assert.EqualValues(t, len(prompts), provider.calls.Load())
	assert.LessOrEqual(t, provider.peak.Load(), int32(3), "the worker pool must bound in-flight queries")
	assert.Equal(t, RunStatusCompleted, run.Status)
}

func TestEngineDefaultConcurrencyIsFour(t *testing.T) {
	repo := &fakeAEORepo{}
	provider := &fakeProvider{
		name:   "alpha",
		model:  "a1",
		delay:  15 * time.Millisecond,
		answer: ProviderAnswer{Text: "Acme."},
	}
	run := newTestRun()

	prompts := enginePrompts("q1", "q2", "q3", "q4", "q5", "q6", "q7", "q8")

	engine := NewEngine(repo, []Provider{provider}, EngineOptions{})
	require.NoError(t, engine.Execute(context.Background(), run, prompts, testProfile()))

	assert.LessOrEqual(t, provider.peak.Load(), int32(defaultConcurrency))
}

func TestEngineRejectsNilRun(t *testing.T) {
	engine := NewEngine(&fakeAEORepo{}, nil, EngineOptions{})
	assert.Error(t, engine.Execute(context.Background(), nil, nil, nil))
}

func TestEngineFinalizesEvenWhenTheRunUpdateFails(t *testing.T) {
	repo := &fakeAEORepo{updateErr: errors.New("update failed")}
	provider := &fakeProvider{name: "alpha", model: "a1", answer: ProviderAnswer{Text: "Acme."}}
	run := newTestRun()

	engine := NewEngine(repo, []Provider{provider}, EngineOptions{})
	require.NoError(t, engine.Execute(context.Background(), run, enginePrompts("q1"), testProfile()),
		"a failed status write is logged, not propagated: there is no caller to handle it")
	assert.Equal(t, RunStatusCompleted, run.Status)
}

func TestEngineSatisfiesExecutor(t *testing.T) {
	var _ Executor = NewEngine(&fakeAEORepo{}, nil, EngineOptions{})
}

func TestRunStatus(t *testing.T) {
	tests := []struct {
		total, failed int
		want          string
	}{
		{total: 0, failed: 0, want: RunStatusCompleted},
		{total: 5, failed: 0, want: RunStatusCompleted},
		{total: 5, failed: 1, want: RunStatusPartial},
		{total: 5, failed: 4, want: RunStatusPartial},
		{total: 5, failed: 5, want: RunStatusFailed},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, runStatus(tc.total, tc.failed))
	}
}
