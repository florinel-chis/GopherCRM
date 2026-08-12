package aeo

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

const (
	// defaultConcurrency bounds how many provider calls are in flight at once.
	// Four is deliberately modest: it keeps a run well inside every provider's
	// rate limit without needing a token bucket of our own.
	defaultConcurrency = 4
	// defaultQueryTimeout is the per-query deadline. Answer engines that search
	// the web (Perplexity) are routinely slow, so this is generous.
	defaultQueryTimeout = 60 * time.Second
)

// Executor runs one batch of AEO queries. The service depends on this interface
// rather than on *Engine so it can be mocked.
type Executor interface {
	Execute(ctx context.Context, run *models.AEORun, prompts []models.AEOPrompt, profile *models.AEOProfile) error
}

// ProviderSetExecutor is an Executor that can be rebound to a different set of
// engines. Provider credentials are administrator-editable, so the service
// resolves them when a run starts and hands the executor the set that was
// configured at that moment rather than the one loaded at boot.
//
// It is a separate interface so that an Executor which does not care about the
// provider set — a test double, most obviously — keeps satisfying Executor.
type ProviderSetExecutor interface {
	Executor
	// WithProviders returns an executor equivalent to this one but running
	// against the given engines. The receiver is left untouched.
	WithProviders(providers []Provider) Executor
}

var _ ProviderSetExecutor = (*Engine)(nil)

// EngineOptions tunes the executor. Zero values select the defaults.
type EngineOptions struct {
	Concurrency  int
	QueryTimeout time.Duration
}

// Engine executes a run: every active prompt against every configured provider,
// recording one answer row per pair — including for failures.
type Engine struct {
	repo      repository.AEORepository
	providers []Provider
	opts      EngineOptions
}

// NewEngine builds an executor. Out-of-range options fall back to the defaults.
func NewEngine(repo repository.AEORepository, providers []Provider, opts EngineOptions) *Engine {
	if opts.Concurrency <= 0 {
		opts.Concurrency = defaultConcurrency
	}
	if opts.QueryTimeout <= 0 {
		opts.QueryTimeout = defaultQueryTimeout
	}
	return &Engine{repo: repo, providers: providers, opts: opts}
}

// WithProviders returns a copy of the engine bound to another provider set. The
// repository handle and the tuning options are shared; only the engines differ,
// which is what makes a run pick up a key added since boot.
func (e *Engine) WithProviders(providers []Provider) Executor {
	if e == nil {
		return nil
	}
	clone := *e
	clone.providers = providers
	return &clone
}

// engineTask is one (prompt × provider) pair.
type engineTask struct {
	prompt   models.AEOPrompt
	provider Provider
}

// Execute runs the batch to completion and finalizes the run row. It is meant to
// be called on a background goroutine with a background context — never with a
// request context, which would be cancelled the moment the HTTP handler returns.
//
// A panic anywhere in the batch is recovered and the run is marked failed: this
// goroutine has no caller to propagate to, and a run stuck in "running" would
// block every subsequent run through the overlap guard.
func (e *Engine) Execute(ctx context.Context, run *models.AEORun, prompts []models.AEOPrompt, profile *models.AEOProfile) (err error) {
	if run == nil {
		return fmt.Errorf("aeo: cannot execute a nil run")
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("aeo: run %d panicked: %v", run.ID, recovered)
			logProvider().WithFields(map[string]any{
				"run_id": run.ID,
				"panic":  recovered,
			}).Error("AEO run panicked")
			total := len(prompts) * len(e.providers)
			e.finalize(run, total, total)
			// runStatus would call a zero-query run "completed"; a panicked run
			// is failed regardless of how far it got.
			run.Status = RunStatusFailed
			if updateErr := e.repo.UpdateRun(run); updateErr != nil {
				logProvider().WithFields(map[string]any{
					"run_id": run.ID,
					"error":  updateErr.Error(),
				}).Error("could not finalize panicked AEO run")
			}
		}
	}()

	tasks := make([]engineTask, 0, len(prompts)*len(e.providers))
	for _, prompt := range prompts {
		for _, provider := range e.providers {
			tasks = append(tasks, engineTask{prompt: prompt, provider: provider})
		}
	}

	logProvider().WithFields(map[string]any{
		"run_id":    run.ID,
		"prompts":   len(prompts),
		"providers": len(e.providers),
		"queries":   len(tasks),
	}).Info("AEO run started")

	var (
		mu       sync.Mutex
		failed   int
		wg       sync.WaitGroup
		slots    = make(chan struct{}, e.opts.Concurrency)
		started  = time.Now()
		recorded = 0
	)

	for _, task := range tasks {
		wg.Add(1)
		slots <- struct{}{}
		go func(task engineTask) {
			defer wg.Done()
			defer func() { <-slots }()

			taskErr := e.runTask(ctx, run, task, profile)

			mu.Lock()
			recorded++
			if taskErr != nil {
				failed++
			}
			mu.Unlock()
		}(task)
	}
	wg.Wait()

	e.finalize(run, len(tasks), failed)

	logProvider().WithFields(map[string]any{
		"run_id":   run.ID,
		"queries":  len(tasks),
		"recorded": recorded,
		"failed":   failed,
		"status":   run.Status,
		"duration": time.Since(started).String(),
	}).Info("AEO run finished")

	return nil
}

// runTask performs one provider call and persists exactly one answer row,
// whether the call succeeded or not. It returns a non-nil error when the query
// failed, which the caller counts towards FailedQueries.
func (e *Engine) runTask(ctx context.Context, run *models.AEORun, task engineTask, profile *models.AEOProfile) (taskErr error) {
	// Last-resort guard. safeQuery already contains a panicking provider; this
	// covers anything else in the task so one bad answer cannot abort the run.
	defer func() {
		if recovered := recover(); recovered != nil {
			taskErr = fmt.Errorf("%s: panicked: %v", task.provider.Name(), recovered)
			logProvider().WithFields(map[string]any{
				"run_id":   run.ID,
				"provider": task.provider.Name(),
				"panic":    recovered,
			}).Error("AEO task panicked")
		}
	}()

	queryCtx, cancel := context.WithTimeout(ctx, e.opts.QueryTimeout)
	defer cancel()

	startedAt := time.Now()
	answer, queryErr := safeQuery(queryCtx, task.provider, task.prompt.Text)
	latency := int(time.Since(startedAt).Milliseconds())

	row := models.AEOAnswer{
		RunID:              run.ID,
		PromptID:           task.prompt.ID,
		Provider:           task.provider.Name(),
		Model:              task.provider.Model(),
		Attempt:            1,
		FirstMentionPos:    -1,
		LatencyMs:          latency,
		CompetitorMentions: map[string]int{},
	}

	var citations []models.AEOCitation
	if queryErr != nil {
		// A failed call is recorded, never dropped: the citations and
		// visibility views need to know a query was attempted and lost.
		row.Error = queryErr.Error()
		taskErr = queryErr
		logProvider().WithFields(map[string]any{
			"run_id":      run.ID,
			"prompt_id":   task.prompt.ID,
			"provider":    task.provider.Name(),
			"http_status": ProviderHTTPStatus(queryErr),
			"error":       queryErr.Error(),
		}).Warn("AEO provider query failed")
	} else {
		mentions := DetectMentions(answer.Text, profile)
		row.AnswerText = answer.Text
		row.BrandMentioned = mentions.BrandMentioned
		row.FirstMentionPos = mentions.FirstMentionPos
		row.CompetitorMentions = mentions.CompetitorMentions
		citations = ExtractCitations(answer.Text, answer.Citations, profile)
	}

	if err := e.repo.CreateAnswerWithCitations(&row, citations); err != nil {
		logProvider().WithFields(map[string]any{
			"run_id":    run.ID,
			"prompt_id": task.prompt.ID,
			"provider":  task.provider.Name(),
			"error":     err.Error(),
		}).Error("could not persist AEO answer")
		// A write failure is a lost query too — count it, so the run does not
		// report itself as fully successful.
		if taskErr == nil {
			taskErr = err
		}
	}

	return taskErr
}

// safeQuery calls the provider and converts a panic into an ordinary error, so
// the task still writes its answer row (with the error recorded) instead of
// silently vanishing from the run.
func safeQuery(ctx context.Context, provider Provider, prompt string) (answer ProviderAnswer, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			answer = ProviderAnswer{}
			err = fmt.Errorf("%s: panicked: %v", provider.Name(), recovered)
			logProvider().WithFields(map[string]any{
				"provider": provider.Name(),
				"panic":    recovered,
			}).Error("AEO provider panicked")
		}
	}()
	return provider.Query(ctx, prompt)
}

// finalize writes the terminal status and counters on the run row.
func (e *Engine) finalize(run *models.AEORun, total, failed int) {
	completedAt := time.Now()
	run.TotalQueries = total
	run.FailedQueries = failed
	run.CompletedAt = &completedAt
	run.Status = runStatus(total, failed)

	if err := e.repo.UpdateRun(run); err != nil {
		logProvider().WithFields(map[string]any{
			"run_id": run.ID,
			"error":  err.Error(),
		}).Error("could not finalize AEO run")
	}
}

// runStatus classifies a finished run. A run with nothing to do is completed,
// not failed.
func runStatus(total, failed int) string {
	switch {
	case failed == 0:
		return RunStatusCompleted
	case failed >= total:
		return RunStatusFailed
	default:
		return RunStatusPartial
	}
}
