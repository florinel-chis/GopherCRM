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
	// defaultCleanupTimeout bounds the terminal status write of a run whose
	// context has already been cancelled. Finalization runs on a fresh context
	// — the cancelled one would abort the very write that records the
	// interruption — but it must not hold shutdown hostage either, so a
	// database that stays blocked past this deadline leaves the row in
	// "running" for the startup reconciliation to recover.
	defaultCleanupTimeout = 5 * time.Second
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

	// CleanupTimeout bounds the finalizing status write once the run context
	// is gone. See defaultCleanupTimeout.
	CleanupTimeout time.Duration
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
	if opts.CleanupTimeout <= 0 {
		opts.CleanupTimeout = defaultCleanupTimeout
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
// be called on a background goroutine with the run context the service owns —
// never with a request context, which would be cancelled the moment the HTTP
// handler returns.
//
// Cancelling ctx interrupts the run: no further query is dispatched, every
// in-flight provider call and answer write is cancelled, and the row is
// finalized on a bounded cleanup context (CleanupTimeout) that is independent
// of ctx. Queries that were cut short or never dispatched count as failed but
// are not recorded as answer rows — a cancelled call is not evidence about the
// brand — so the run ends "failed" when nothing had been recorded and "partial"
// otherwise, with completed_at stamped. Execute then returns ctx.Err() wrapped,
// which the caller logs as an interruption rather than a fault.
//
// A panic anywhere in the batch is recovered and the run is marked failed: this
// goroutine has no caller to propagate to, and a run stuck in "running" would
// block every subsequent run through the overlap guard.
func (e *Engine) Execute(ctx context.Context, run *models.AEORun, prompts []models.AEOPrompt, profile *models.AEOProfile) (err error) {
	if run == nil {
		return fmt.Errorf("aeo: cannot execute a nil run")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	defer func() {
		// The panic value may be provider-controlled, so it is neither
		// returned nor logged.
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("aeo: run %d panicked", run.ID)
			logProvider().WithFields(map[string]any{
				"run_id":           run.ID,
				"failure_category": FailurePanic,
			}).Error("AEO run panicked")
			total := len(prompts) * len(e.providers)
			// runStatus would call a zero-query run "completed"; a panicked run
			// is failed regardless of how far it got.
			e.finalize(run, total, total, RunStatusFailed)
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

	// Every answer write of this run is bound to the run context, so a write
	// blocked in the database is released by the same cancellation that
	// releases the provider calls.
	writes := e.repo.WithContext(ctx)

	var (
		mu       sync.Mutex
		failed   int
		wg       sync.WaitGroup
		slots    = make(chan struct{}, e.opts.Concurrency)
		started  = time.Now()
		recorded = 0
		skipped  = 0
	)

	for _, task := range tasks {
		// Stop dispatching the moment the run is cancelled. A task that never
		// ran is a lost query, counted below. The check comes before the slot
		// acquisition because select picks at random among ready cases: with a
		// free slot and a cancelled context it would otherwise dispatch half the
		// batch of an already-cancelled run.
		if ctx.Err() != nil {
			skipped++
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			skipped++
			continue
		}
		if ctx.Err() != nil {
			// Cancelled while waiting for the slot, and the select still handed
			// it out. Give it back and count the task as lost.
			<-slots
			skipped++
			continue
		}
		wg.Add(1)
		go func(task engineTask) {
			defer wg.Done()
			defer func() { <-slots }()

			taskErr := e.runTask(ctx, writes, run, task, profile)

			mu.Lock()
			recorded++
			if taskErr != nil {
				failed++
			}
			mu.Unlock()
		}(task)
	}
	wg.Wait()

	failed += skipped
	e.finalize(run, len(tasks), failed, "")

	fields := map[string]any{
		"run_id":   run.ID,
		"queries":  len(tasks),
		"recorded": recorded,
		"failed":   failed,
		"status":   run.Status,
		"duration": time.Since(started).String(),
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		fields["skipped"] = skipped
		logProvider().WithFields(fields).Warn("AEO run interrupted")
		return fmt.Errorf("aeo: run %d interrupted: %w", run.ID, ctxErr)
	}
	logProvider().WithFields(fields).Info("AEO run finished")

	return nil
}

// runTask performs one provider call and persists exactly one answer row,
// whether the call succeeded or not. It returns a non-nil error when the query
// failed, which the caller counts towards FailedQueries.
//
// writes is the repository bound to the run context. When the run is cancelled
// while the call is in flight, the query is reported failed but no answer row
// is written: the cancelled context would abort the write anyway, and a row
// saying "request canceled" would only be noise in the transcript.
func (e *Engine) runTask(ctx context.Context, writes repository.AEORepository, run *models.AEORun, task engineTask, profile *models.AEOProfile) (taskErr error) {
	// Last-resort guard. SafeQuery already contains a panicking provider; this
	// covers anything else in the task so one bad answer cannot abort the run.
	// The panic value is never logged: it may be provider-controlled.
	providerName := "provider"
	defer func() {
		if recovered := recover(); recovered != nil {
			taskErr = &ProviderError{Provider: providerName, Category: FailurePanic}
			logProvider().WithFields(map[string]any{
				"run_id":           run.ID,
				"provider":         providerName,
				"failure_category": FailurePanic,
			}).Error("AEO task panicked")
		}
	}()
	providerName = safeProviderName(task.provider.Name())

	queryCtx, cancel := context.WithTimeout(ctx, e.opts.QueryTimeout)
	defer cancel()

	startedAt := time.Now()
	answer, queryErr := SafeQuery(queryCtx, task.provider, task.prompt.Text)
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
		// SafeQuery has already reduced the failure to a bounded category and
		// status; sanitizing again keeps this boundary safe on its own.
		safeErr := SanitizeProviderError(providerName, queryErr)
		taskErr = safeErr
		if ctx.Err() != nil {
			// The run was cancelled under this call. Count it, do not record it.
			logProvider().WithFields(map[string]any{
				"run_id":           run.ID,
				"prompt_id":        task.prompt.ID,
				"provider":         providerName,
				"failure_category": ProviderFailureCategory(safeErr),
			}).Info("AEO provider query interrupted")
			return taskErr
		}
		// A failed call is recorded, never dropped: the citations and
		// visibility views need to know a query was attempted and lost.
		row.Error = safeErr.Error()
		logProvider().WithFields(map[string]any{
			"run_id":           run.ID,
			"prompt_id":        task.prompt.ID,
			"provider":         providerName,
			"http_status":      ProviderHTTPStatus(safeErr),
			"failure_category": ProviderFailureCategory(safeErr),
			"error":            safeErr.Error(),
		}).Warn("AEO provider query failed")
	} else {
		mentions := DetectMentions(answer.Text, profile)
		row.AnswerText = answer.Text
		row.BrandMentioned = mentions.BrandMentioned
		row.FirstMentionPos = mentions.FirstMentionPos
		row.CompetitorMentions = mentions.CompetitorMentions
		citations = extractCitations(answer.Text, answer.Citations, profile, logProvider().WithFields(map[string]any{
			"run_id":    run.ID,
			"prompt_id": task.prompt.ID,
			"provider":  task.provider.Name(),
		}))
	}

	if err := writes.CreateAnswerWithCitations(&row, citations); err != nil {
		fields := map[string]any{
			"run_id":    run.ID,
			"prompt_id": task.prompt.ID,
			"provider":  task.provider.Name(),
			"error":     err.Error(),
		}
		if ctx.Err() != nil {
			// The write was cut short by the run's cancellation. The paid
			// answer is lost; the counters below say so.
			logProvider().WithFields(fields).Warn("AEO answer write interrupted")
		} else {
			logProvider().WithFields(fields).Error("could not persist AEO answer")
		}
		// A write failure is a lost query too — count it, so the run does not
		// report itself as fully successful.
		if taskErr == nil {
			taskErr = err
		}
	}

	return taskErr
}

// finalize writes the terminal status and counters on the run row. status
// overrides the computed classification when non-empty (a panicked run is
// failed whatever its counters say).
//
// The write runs on a fresh context bounded by CleanupTimeout rather than on
// the run context: by the time an interrupted run finalizes, that context is
// already cancelled and would abort the one write that records the
// interruption. A database that stays blocked past the bound leaves the row in
// "running"; the next StartRun sweep or the startup reconciliation recovers it.
func (e *Engine) finalize(run *models.AEORun, total, failed int, status string) {
	completedAt := time.Now()
	run.TotalQueries = total
	run.FailedQueries = failed
	run.CompletedAt = &completedAt
	run.Status = runStatus(total, failed)
	if status != "" {
		run.Status = status
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), e.opts.CleanupTimeout)
	defer cancel()
	if err := e.repo.WithContext(cleanupCtx).UpdateRun(run); err != nil {
		logProvider().WithFields(map[string]any{
			"run_id": run.ID,
			"status": run.Status,
			"error":  err.Error(),
		}).Error("could not finalize AEO run; left for stale-run recovery")
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
