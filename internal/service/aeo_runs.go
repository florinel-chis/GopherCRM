package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"

	"github.com/sirupsen/logrus"
)

// ------------------------------------------------------------------- runs ---

// StartRun starts a full run of every active prompt. A body trigger of
// "scheduled" is accepted for compatibility and stores no schedule slot; the
// scheduler itself goes through StartScheduledRun.
func (s *aeoService) StartRun(ctx context.Context, trigger string, triggeredByID *uint) (*models.AEORun, error) {
	return s.startRun(ctx, trigger, triggeredByID, nil, selectActiveAEOPrompts)
}

// StartScheduledRun starts the daily run of slot, the instant the scheduler
// computed with aeo.NextRunAt (not the instant it woke up). The slot key is
// slot.UTC() in models.AEOScheduleSlotFormat; its unique index lets exactly one
// replica start it, including after that run has finished. The others get
// apperrors.ErrAEOScheduleSlotTaken and insert nothing.
func (s *aeoService) StartScheduledRun(ctx context.Context, slot time.Time) (*models.AEORun, error) {
	key := slot.UTC().Format(models.AEOScheduleSlotFormat)
	return s.startRun(ctx, aeoTriggerScheduled, nil, &key, selectActiveAEOPrompts)
}

// selectActiveAEOPrompts is the prompt selection of a full run.
func selectActiveAEOPrompts(repo repository.AEORepository) ([]models.AEOPrompt, error) {
	prompts, err := repo.ListActivePrompts()
	if err != nil {
		return nil, err
	}
	if len(prompts) == 0 {
		return nil, fmt.Errorf("no active AEO prompts: %w", apperrors.ErrNotFound)
	}
	return prompts, nil
}

// StartPromptRun runs a single prompt against every configured engine — the
// "try just this one" button on the prompt detail. The prompt does not have to
// be active: running an inactive prompt on demand is how a draft gets tested
// before it joins the daily run. Everything else — the claim, the abandoned-run
// sweep, background execution — matches a full manual run.
func (s *aeoService) StartPromptRun(ctx context.Context, promptID uint, triggeredByID *uint) (*models.AEORun, error) {
	return s.startRun(ctx, aeoTriggerManual, triggeredByID, nil, func(repo repository.AEORepository) ([]models.AEOPrompt, error) {
		prompt, err := repo.GetPromptByID(promptID)
		if err != nil {
			if isNotFound(err) {
				return nil, fmt.Errorf("prompt %d not found: %w", promptID, apperrors.ErrNotFound)
			}
			return nil, err
		}
		return []models.AEOPrompt{*prompt}, nil
	})
}

// startRun admits, records and launches one run.
//
// The caller's ctx is deliberately not used for anything the run depends on. A
// manual run is asked for by an HTTP request that returns 202 as soon as the
// row exists; cancelling that request must not abort a run already writing
// answers, and the scheduler's context is the process lifetime anyway. Every
// read and write below, and the executor itself, run on the lifecycle context
// the service owns, so shutdown is the one thing that cancels them.
//
// scheduleSlot is set only by StartScheduledRun. Log lines here carry the
// trigger, run_id, counts and error codes; never the slot, the instance id,
// the brand or a prompt.
func (s *aeoService) startRun(_ context.Context, trigger string, triggeredByID *uint, scheduleSlot *string, selectPrompts func(repo repository.AEORepository) ([]models.AEOPrompt, error)) (*models.AEORun, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_run"), "AEOService", "StartRun")

	trigger = strings.ToLower(strings.TrimSpace(trigger))
	if trigger == "" {
		trigger = aeoTriggerManual
	}
	if trigger != aeoTriggerManual && trigger != aeoTriggerScheduled {
		return nil, ErrAEOInvalidTrigger
	}
	logger = logger.WithField("trigger", trigger)

	// Admission comes before the first database call: from here until the
	// executor releases it, this start is owned work that shutdown joins (or
	// times out on) instead of closing the database underneath. A start that
	// fails anywhere below releases on return; a start that launches hands the
	// release to the executor goroutine.
	release, err := s.lifecycle.Admit()
	if err != nil {
		logger.Info("Refusing to start an AEO run: the runtime is shutting down")
		return nil, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()

	runCtx := s.lifecycle.Context()
	repo := s.repo.WithContext(runCtx)

	// Order matters: the caller gets the most actionable reason first. A missing
	// profile means mention detection has nothing to look for, so it outranks
	// "no providers", which outranks the overlap guard.
	profile, err := repo.GetProfile()
	if err != nil {
		if isNotFound(err) {
			return nil, apperrors.ErrProfileNotConfigured
		}
		if runCtx.Err() != nil {
			return nil, ErrAEOShuttingDown
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	// Credentials are administrator-editable, so the engine set is resolved
	// here rather than reused from boot: a key stored since the process
	// started is in force from this run on.
	providers := s.currentProviders()
	if len(providers) == 0 {
		return nil, apperrors.ErrNoProvidersConfigured
	}
	if s.executor == nil {
		return nil, apperrors.ErrNoProvidersConfigured
	}
	executor := s.currentExecutor(providers)

	// startMu keeps this process's sweep and claim together; across replicas
	// the claim itself is the guard.
	s.startMu.Lock()
	defer s.startMu.Unlock()

	// A start that queued on the mutex behind a slow one may find the runtime
	// gone by the time it gets in; nothing below should touch the database then.
	if runCtx.Err() != nil {
		logger.Info("Refusing to start an AEO run: the runtime is shutting down")
		return nil, ErrAEOShuttingDown
	}

	// Fail the runs whose owner is gone before claiming: a run stranded by a
	// crash unblocks the next start once its heartbeat is older than
	// aeoRunHeartbeatStaleAfter (or it is older than aeoRunStaleAfter). A live
	// run on another replica keeps renewing its heartbeat and is left alone.
	if _, err := s.failAbandonedRuns(repo, logger); err != nil {
		// A failed sweep is not a reason to refuse the run: the claim below is
		// still correct, just less forgiving.
		logger.WithError(utils.LogError(err)).Warn("Could not sweep abandoned AEO runs")
	}

	prompts, err := selectPrompts(repo)
	if err != nil {
		if runCtx.Err() != nil && !apperrors.IsNotFound(err) {
			return nil, ErrAEOShuttingDown
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	owner := s.instanceID
	run := &models.AEORun{
		Trigger:       trigger,
		Status:        aeoRunStatusRunning,
		StartedAt:     s.clock(),
		TotalQueries:  len(prompts) * len(providers),
		TriggeredByID: triggeredByID,
		OwnerInstance: &owner,
		ScheduleSlot:  scheduleSlot,
	}
	if err := repo.ClaimRun(run); err != nil {
		switch {
		case errors.Is(err, apperrors.ErrRunInProgress):
			logger.WithField("error_code", "run_in_progress").
				Info("Refusing to start an AEO run: another run holds the running lock")
			return nil, apperrors.ErrRunInProgress
		case errors.Is(err, apperrors.ErrAEOScheduleSlotTaken):
			logger.WithField("error_code", "schedule_slot_taken").
				Info("Skipping the AEO scheduled run: its slot was already claimed")
			return nil, apperrors.ErrAEOScheduleSlotTaken
		case runCtx.Err() != nil:
			// A claim cut short by the cancellation may still have landed.
			// Nothing renews its heartbeat, so the next sweep on any replica
			// fails it once the heartbeat passes aeoRunHeartbeatStaleAfter.
			return nil, ErrAEOShuttingDown
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	if runCtx.Err() != nil {
		// Shutdown won the race with the insert. The row exists and nothing
		// will execute it, so it is failed here, on a bounded fresh context.
		s.failInterruptedRun(run, logger)
		return nil, ErrAEOShuttingDown
	}

	// The caller gets a copy taken before the executor starts mutating the
	// run it owns.
	accepted := *run

	// The run outlives the HTTP request that asked for it: it executes on the
	// lifecycle context, which only shutdown cancels, and the executor
	// goroutine now owns the admission taken above.
	handedOff = true
	go s.runInBackground(runCtx, executor, run, prompts, profile, release, logger)

	logger.WithFields(map[string]interface{}{
		"run_id":        accepted.ID,
		"total_queries": accepted.TotalQueries,
	}).Info("AEO run started")
	return &accepted, nil
}

// failAbandonedRuns fails the running rows whose heartbeat is older than
// aeoRunHeartbeatStaleAfter (or NULL, a row written before ownership existed)
// or that started more than aeoRunStaleAfter ago, and logs the count.
func (s *aeoService) failAbandonedRuns(repo repository.AEORepository, logger *logrus.Entry) (int64, error) {
	now := s.clock()
	recovered, err := repo.FailAbandonedRuns(now.Add(-aeoRunStaleAfter), now.Add(-aeoRunHeartbeatStaleAfter))
	if err != nil {
		return 0, err
	}
	if recovered > 0 {
		logger.WithField("recovered_runs", recovered).
			Warn("Failed AEO runs whose owner stopped renewing its heartbeat")
	}
	return recovered, nil
}

// ReconcileRunningRuns fails the abandoned runs and returns how many it
// recovered. It is called at startup, before the scheduler is armed, by every
// replica.
//
// A running row is not proof of a dead process any more: another replica
// sharing the database may be executing it, renewing its heartbeat every
// aeoRunHeartbeatInterval. Only a row whose heartbeat is older than
// aeoRunHeartbeatStaleAfter, a legacy row with no heartbeat, or a row started
// more than aeoRunStaleAfter ago is failed. A restarted process therefore
// recovers its own crashed run up to aeoRunHeartbeatStaleAfter after the
// crash, at boot or at the next start attempt (there is no periodic sweep).
func (s *aeoService) ReconcileRunningRuns() (int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_run"), "AEOService", "ReconcileRunningRuns")

	s.startMu.Lock()
	defer s.startMu.Unlock()

	recovered, err := s.failAbandonedRuns(s.repo, logger)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return 0, err
	}
	return recovered, nil
}

func (s *aeoService) ListRuns(offset, limit int, sortBy, sortOrder string) ([]models.AEORun, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_run"), "AEOService", "ListRuns")

	runs, err := s.repo.ListRuns(offset, limit, sortBy, sortOrder)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	if runs == nil {
		runs = []models.AEORun{}
	}

	total, err := s.repo.CountRuns()
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	return runs, total, nil
}

func (s *aeoService) GetRun(id uint) (*models.AEORun, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("aeo_run_id", id), "AEOService", "GetRun")

	run, err := s.repo.GetRunByID(id)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("aeo run %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	return run, nil
}

// lastRunAt reports when the most recent run finished, falling back to when it
// started for a run that is still going.
func (s *aeoService) lastRunAt() (*time.Time, error) {
	run, err := s.repo.GetLatestRun()
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if run == nil {
		return nil, nil
	}
	if run.CompletedAt != nil {
		return run.CompletedAt, nil
	}
	startedAt := run.StartedAt
	return &startedAt, nil
}
