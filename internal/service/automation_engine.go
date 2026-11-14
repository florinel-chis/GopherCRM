package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// The automation engine turns outbox events into runs (Dispatch), continues
// schedule passes (RunSchedules), claims and executes due runs (ExecuteDue)
// and recovers runs whose executor stopped renewing its lease
// (SweepStaleRuns).
//
// Run leases (#237). Several engines (one per API replica) may share one
// MySQL/MariaDB database. Each engine has an instance id; ClaimRun stores it
// in owner_instance together with heartbeat_at, and a heartbeat goroutine
// renews heartbeat_at every automationRunHeartbeatInterval while the run
// executes. Every write after the claim is fenced on the owner: the step
// advance, the release, the first send_email transaction (a locking read of
// the run row), a plain read just before the send, and the second send_email
// transaction. A cancel clears the owner. The stale sweep, at boot and at the
// start of every tick, recovers a running run whose heartbeat is NULL or older
// than automationRunLeaseStaleAfter: back to pending at its position, or
// failed with email_unconfirmed when its last step row is a send_email still
// in sending. A stale executor that wakes up afterwards matches nothing with
// its fenced writes and stops.
//
// Every step other than send_email runs in one transaction that, in order,
// re-reads the target record with a locking read (FOR UPDATE on MySQL, a
// plain read on SQLite), advances the run with a conditional update
// (WHERE id = ? AND status = 'running' AND next_position = ?), applies the
// effect and inserts the run-step row. The conditional advance is what makes
// a cancel, a rule change or an erasure committed meanwhile win: it matches
// nothing, the transaction rolls back and the executor stops. The locking
// read matters on MySQL because a plain read inside a REPEATABLE READ
// transaction would answer from a snapshot taken before that commit.
//
// send_email is the exception: the mail cannot be sent inside a transaction
// (an SMTP relay holds no lock and cannot be rolled back), so the step is two
// transactions around one Mailer.Send: the first commits a run-step row with
// outcome sending (unique on run_id and position, so no second executor can
// start the same send), the second finalizes the row and advances the run.
// A row left in sending is never resent: the next execution, or the stale
// sweep, marks it failed with email_unconfirmed.

// ErrAutomationShuttingDown is returned by the admission once the runtime has
// begun shutting down.
var ErrAutomationShuttingDown = errors.New("automation runtime is shutting down")

// errRunNotAdvanced is the internal signal that a conditional advance (or
// claim) matched nothing: the run moved under the executor, the transaction
// rolls back and the executor stops. It is never retried and never logged
// as a failure.
var errRunNotAdvanced = errors.New("automation run was not advanced: it changed under the executor")

// errEventClaimed is the internal signal that another dispatcher claimed the
// event first.
var errEventClaimed = errors.New("automation event already claimed")

// errScheduleCursorMoved is the internal signal that another worker advanced
// the schedule cursor first.
var errScheduleCursorMoved = errors.New("automation schedule cursor moved")

// Batch sizes and bounds of the worker.
const (
	automationDispatchBatch = 100
	automationClaimBatch    = 100
	automationMaxAttempts   = 3
	automationTxRetries     = 3
)

// Lease timings (#237), package constants rather than settings. The stale
// horizon is six missed heartbeats, as for AEO runs (#229): room for a slow
// database, a 30 s SMTP send and clock skew between replicas, which must stay
// well under 90 s.
const (
	automationRunHeartbeatInterval = 30 * time.Second
	automationRunLeaseStaleAfter   = 3 * time.Minute
	automationSweepBatch           = 100
)

// Outcomes of a stale recovery, as the sweep logs and counts them.
const (
	automationRecoveredPending = "pending"
	automationRecoveredFailed  = "failed"
)

// automationOwner is the lifecycle of the worker's owned work: the admission
// count, the context every owned unit runs on and the drain the runtime waits
// for. Same shape as the AEO run owner: register before the first database
// call, release after the last write, never close the pool while the count is
// non-zero.
type automationOwner struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	active   int
	drained  chan struct{}
	signaled bool
}

func newAutomationOwner() *automationOwner {
	ctx, cancel := context.WithCancel(context.Background())
	return &automationOwner{ctx: ctx, cancel: cancel, drained: make(chan struct{})}
}

// admit registers one unit of owned work; it fails once shutdown has begun.
func (o *automationOwner) admit() (release func(), err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.ctx.Err() != nil {
		return nil, ErrAutomationShuttingDown
	}
	o.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.active--
			o.signalIfDrained()
		})
	}, nil
}

func (o *automationOwner) signalIfDrained() {
	if o.closed && o.active == 0 && !o.signaled {
		o.signaled = true
		close(o.drained)
	}
}

func (o *automationOwner) activeCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.active
}

// shutdown closes admission, cancels the owned context and waits for every
// admitted unit to release, or for ctx to end.
func (o *automationOwner) shutdown(ctx context.Context) error {
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		o.cancel()
	}
	o.signalIfDrained()
	o.mu.Unlock()

	// An already drained owner reports success whatever the caller's
	// context: a repeated Stop after a deadline has passed is a no-op, not a
	// failure. Only an owner that still holds work waits on ctx.
	select {
	case <-o.drained:
		return nil
	default:
	}
	select {
	case <-o.drained:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("automation runtime: %d unit(s) of work still active at the shutdown deadline: %w", o.activeCount(), ctx.Err())
	}
}

// AutomationEngine executes automation rules. It is constructed once by the
// runtime (or directly by a test) and shared by the worker and the dry run.
type AutomationEngine struct {
	repo          repository.AutomationRepository
	notifications repository.NotificationRepository
	tasks         repository.TaskRepository
	users         repository.UserRepository
	mailer        mailer.Mailer
	txManager     *utils.TransactionManager
	now           func() time.Time
	owner         *automationOwner

	// Instance-local seams, nil in production. The regression roots use them
	// to pause a step transaction after its first read, to inject a fault
	// after an effect or after a send, and to act between the two email
	// transactions. They are fields of this engine, never globals.
	afterRecordRead func(runID uint, position int)
	afterEffect     func(runID uint, position int) error
	beforeSend      func(runID uint, position int)
	afterSend       func(runID uint, position int) error

	// beforeStep (#237) fires in the run loop before each step is admitted,
	// outside any transaction; afterStaleRecover fires inside the sweep's
	// recovery transaction right after the conditional recovery update.
	beforeStep func(runID uint, position int)

	afterStaleRecover func(runID uint)

	// instanceID (#237) names this engine in automation_runs.owner_instance:
	// 32 lowercase hex characters from crypto/rand. Never logged.
	instanceID string

	// heartbeatInterval is how often the owner renews heartbeat_at while it
	// executes a run; automationRunHeartbeatInterval in production.
	heartbeatInterval time.Duration

	// recoveryObserver, nil in production, is told about every run the sweep
	// recovered, after the recovery committed (SetRecoveryObserver).
	recoveryObserver func(runID uint, outcome string)
}

// NewAutomationEngine wires the engine. The mailer may be nil, in which case
// every send_email step fails with mail_failed.
func NewAutomationEngine(repo repository.AutomationRepository, notifications repository.NotificationRepository, tasks repository.TaskRepository, users repository.UserRepository, m mailer.Mailer, txManager *utils.TransactionManager) *AutomationEngine {
	e := &AutomationEngine{
		repo:          repo,
		notifications: notifications,
		tasks:         tasks,
		users:         users,
		mailer:        m,
		txManager:     txManager,
		now:           time.Now,
		owner:         newAutomationOwner(),
	}
	e.instanceID = newAutomationInstanceID()
	e.heartbeatInterval = automationRunHeartbeatInterval
	return e
}

// newAutomationInstanceID returns 32 lowercase hex characters from
// crypto/rand. A failing random source is not survivable for ownership, so it
// panics at construction rather than handing two engines the same id.
func newAutomationInstanceID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(fmt.Sprintf("automation: cannot generate an instance id: %v", err))
	}
	return hex.EncodeToString(raw[:])
}

// SetRecoveryObserver registers a function told about every run the stale
// sweep recovered (outcome "pending" or "failed"), after the recovery
// committed and before anything else touches the run. It is a test seam of
// the production wiring; set it before the engine is started.
func (e *AutomationEngine) SetRecoveryObserver(fn func(runID uint, outcome string)) {
	e.recoveryObserver = fn
}

// SetClock replaces the engine's time source (tests).
func (e *AutomationEngine) SetClock(now func() time.Time) {
	if now != nil {
		e.now = now
	}
}

// Shutdown closes admission, cancels owned work and waits for it to release.
func (e *AutomationEngine) Shutdown(ctx context.Context) error {
	return e.owner.shutdown(ctx)
}

// ActiveWork reports the admitted units of work still registered.
func (e *AutomationEngine) ActiveWork() int {
	return e.owner.activeCount()
}

// Context is the owned context; it ends when Shutdown begins.
func (e *AutomationEngine) Context() context.Context {
	return e.owner.ctx
}

func (e *AutomationEngine) logger() *logrus.Entry {
	if utils.Logger == nil {
		return logrus.NewEntry(logrus.StandardLogger())
	}
	return utils.Logger.WithField("component", "automation")
}

// Tick is one worker pass: the stale sweep, dispatch, schedules, due runs.
// Each phase keeps going after a failure in one item; the errors are joined
// for the log. Sweeping first means a run abandoned by a crashed worker is
// recovered within the stale horizon, without a restart, and claimed by the
// same pass.
func (e *AutomationEngine) Tick(ctx context.Context) error {
	var errs []error
	recovered, failed, err := e.SweepStaleRuns(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	if recovered > 0 || failed > 0 {
		e.logger().WithFields(logrus.Fields{"recovered": recovered, "failed": failed}).
			Warn("Automation runs recovered from stale leases")
	}
	if _, err := e.Dispatch(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := e.RunSchedules(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := e.ExecuteDue(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// --- Dispatch ----------------------------------------------------------------

// Dispatch claims unprocessed events and starts one run per enabled,
// matching rule. It returns how many runs it started.
func (e *AutomationEngine) Dispatch(ctx context.Context) (int, error) {
	events, err := e.repo.ListUnprocessedEvents(automationDispatchBatch)
	if err != nil {
		return 0, err
	}
	started := 0
	var errs []error
	for _, event := range events {
		if ctx.Err() != nil {
			break
		}
		n, err := e.DispatchEvent(event)
		started += n
		if err != nil {
			errs = append(errs, err)
			e.logger().WithFields(logrus.Fields{"event_id": event.ID, "event_type": event.EventType}).
				WithError(utils.LogError(err)).Error("Automation event dispatch failed")
		}
	}
	return started, errors.Join(errs...)
}

// DispatchEvent claims one event with a conditional update and, in the same
// transaction, creates exactly one run per enabled matching rule. An event
// another dispatcher claimed first starts nothing and is not an error. An
// event of origin automation is claimed and starts nothing: that is the loop
// guard.
func (e *AutomationEngine) DispatchEvent(event models.AutomationEvent) (int, error) {
	started := 0
	err := e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		started = 0
		claimed, err := r.ClaimEvent(event.ID, e.now())
		if err != nil {
			return err
		}
		if claimed != 1 {
			return errEventClaimed
		}
		if event.Origin == models.AutomationOriginAutomation {
			return nil
		}
		rules, err := r.ListMatchingRules(event.EntityType, event.EventType)
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if !triggerMatches(rule.Trigger, event) {
				continue
			}
			eventID := event.ID
			run := &models.AutomationRun{
				RuleID:       rule.ID,
				RuleVersion:  rule.Version,
				EntityType:   event.EntityType,
				EntityID:     event.EntityID,
				EventID:      &eventID,
				DedupeKey:    fmt.Sprintf("evt:%d:rule:%d", event.ID, rule.ID),
				Status:       models.AutomationRunPending,
				NextPosition: 1,
			}
			inserted, err := r.CreateRunIgnoringDuplicate(run)
			if err != nil {
				return err
			}
			if inserted {
				started++
			}
		}
		return nil
	}, automationTxRetries)
	if errors.Is(err, errEventClaimed) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if started > 0 {
		e.logger().WithFields(logrus.Fields{
			"event_id": event.ID, "event_type": event.EventType, "entity_type": event.EntityType,
			"entity_id": event.EntityID, "runs": started,
		}).Info("Automation runs started")
	}
	return started, nil
}

// triggerMatches applies the from/to filter of a status or stage change
// trigger and the form_id filter of lead_form_submitted (0 means every form);
// the other event triggers match every event of their type.
func triggerMatches(trigger models.AutomationTrigger, event models.AutomationEvent) bool {
	switch event.EventType {
	case models.AutomationEventLeadStatusChanged, models.AutomationEventDealStageChanged, models.AutomationEventTicketStatusChanged:
		if trigger.From != "" && trigger.From != event.FromValue {
			return false
		}
		if trigger.To != "" && trigger.To != event.ToValue {
			return false
		}
	case models.AutomationEventLeadFormSubmitted:
		if trigger.FormID != 0 && (event.FormID == nil || *event.FormID != trigger.FormID) {
			return false
		}
	}
	return true
}

// --- Schedules ---------------------------------------------------------------

// RunSchedules makes progress on every enabled schedule rule whose pass for
// the current UTC day is due and not finished: one batch of at most
// models.AutomationScheduleBatchSize records per rule per tick.
func (e *AutomationEngine) RunSchedules(ctx context.Context) error {
	rules, err := e.repo.ListScheduleRules()
	if err != nil {
		return err
	}
	now := e.now().UTC()
	today := now.Format("2006-01-02")
	var errs []error
	for i := range rules {
		if ctx.Err() != nil {
			break
		}
		rule := rules[i]
		if now.Hour() < rule.Trigger.HourUTC {
			continue
		}
		if rule.ScheduleCompletedOn != nil && *rule.ScheduleCompletedOn == today {
			continue
		}
		if err := e.runScheduleBatch(rule, now, today); err != nil {
			if errors.Is(err, errScheduleCursorMoved) {
				continue
			}
			errs = append(errs, err)
			e.logger().WithField("rule_id", rule.ID).WithError(utils.LogError(err)).Error("Automation schedule batch failed")
		}
	}
	return errors.Join(errs...)
}

// runScheduleBatch inserts one run per record of the next batch and advances
// the cursor with a conditional update on (schedule_pass_on, schedule_cursor),
// in one transaction. A pass that started on an earlier day restarts from
// cursor 0 for today; an earlier day is never backfilled.
func (e *AutomationEngine) runScheduleBatch(rule models.AutomationRule, now time.Time, today string) error {
	expectedPassOn := rule.SchedulePassOn
	expectedCursor := rule.ScheduleCursor
	afterID := uint(0)
	if expectedPassOn != nil && *expectedPassOn == today {
		afterID = expectedCursor
	}
	query := repository.AutomationScheduleQuery{
		EntityType: rule.EntityType,
		AfterID:    afterID,
		Limit:      models.AutomationScheduleBatchSize,
	}
	if rule.Trigger.Filters != nil {
		query.Status = rule.Trigger.Filters.Status
		query.Stage = rule.Trigger.Filters.Stage
		query.TicketStatus = rule.Trigger.Filters.TicketStatus
	}
	if rule.Trigger.StaleDays > 0 {
		before := now.Add(-time.Duration(rule.Trigger.StaleDays) * 24 * time.Hour)
		query.StaleBefore = &before
	}
	started := 0
	err := e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		started = 0
		ids, err := r.ListScheduleCandidateIDs(query)
		if err != nil {
			return err
		}
		for _, id := range ids {
			run := &models.AutomationRun{
				RuleID:       rule.ID,
				RuleVersion:  rule.Version,
				EntityType:   rule.EntityType,
				EntityID:     id,
				DedupeKey:    fmt.Sprintf("sch:%d:%s:%d", rule.ID, today, id),
				Status:       models.AutomationRunPending,
				NextPosition: 1,
			}
			inserted, err := r.CreateRunIgnoringDuplicate(run)
			if err != nil {
				return err
			}
			if inserted {
				started++
			}
		}
		cursor := afterID
		if len(ids) > 0 {
			cursor = ids[len(ids)-1]
		}
		var completedOn *string
		if len(ids) < models.AutomationScheduleBatchSize {
			day := today
			completedOn = &day
		}
		rows, err := r.AdvanceScheduleCursor(rule.ID, expectedPassOn, expectedCursor, today, cursor, completedOn, now)
		if err != nil {
			return err
		}
		if rows != 1 {
			return errScheduleCursorMoved
		}
		return nil
	}, automationTxRetries)
	if err != nil {
		return err
	}
	e.logger().WithFields(logrus.Fields{"rule_id": rule.ID, "pass": today, "runs": started}).Info("Automation schedule batch handed out")
	return nil
}

// --- Execution ---------------------------------------------------------------

// ExecuteDue claims every pending run and every waiting run whose resume_at
// has passed, one conditional claim each, and executes it.
func (e *AutomationEngine) ExecuteDue(ctx context.Context) error {
	now := e.now()
	runs, err := e.repo.ListClaimableRuns(now, automationClaimBatch)
	if err != nil {
		return err
	}
	for i := range runs {
		if ctx.Err() != nil {
			break
		}
		run := runs[i]
		claimed, err := e.repo.ClaimRun(run.ID, run.Status, e.instanceID, now)
		if err != nil {
			e.logger().WithFields(logrus.Fields{"run_id": run.ID, "operation": "claim"}).
				WithError(utils.LogError(err)).Error("Automation run claim failed")
			continue
		}
		if claimed != 1 {
			continue
		}
		owner := e.instanceID
		stamp := now.UTC()
		run.Status = models.AutomationRunRunning
		run.OwnerInstance = &owner
		run.HeartbeatAt = &stamp
		e.executeRun(ctx, run)
	}
	return nil
}

// stepResult is what one executed step reports back to the run loop.
type stepResult struct {
	terminal     bool
	nextPosition int
}

// executeRun runs the claimed run under its lease: one heartbeat goroutine
// renews heartbeat_at every heartbeatInterval while the steps execute, and is
// joined before executeRun returns, so nothing renews a run whose executor
// has returned. A renewal refused because the run is no longer owned (a
// cancel, a stale recovery by another engine) cancels the run's context: the
// executor starts no further step and writes nothing more for the run.
func (e *AutomationEngine) executeRun(ctx context.Context, run models.AutomationRun) {
	logger := e.logger().WithFields(logrus.Fields{
		"run_id": run.ID, "rule_id": run.RuleID, "entity_type": run.EntityType, "entity_id": run.EntityID,
	})
	runCtx, cancelRun := context.WithCancel(ctx)
	var leaseLost atomic.Bool
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		e.renewLease(runCtx, run.ID, &leaseLost, cancelRun, logger)
	}()
	defer func() {
		cancelRun()
		<-heartbeatDone
	}()
	e.runSteps(runCtx, run, &leaseLost, logger)
}

// renewLease renews the run's heartbeat every heartbeatInterval until ctx
// ends. A renewal refused with ErrAutomationRunNotOwned marks the lease lost
// and cancels the run; any other failure is logged and retried at the next
// interval.
func (e *AutomationEngine) renewLease(ctx context.Context, runID uint, lost *atomic.Bool, cancel context.CancelFunc, logger *logrus.Entry) {
	ticker := time.NewTicker(e.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		err := e.repo.RenewRunLease(runID, e.instanceID, e.now().UTC())
		switch {
		case err == nil:
		case errors.Is(err, repository.ErrAutomationRunNotOwned):
			if ctx.Err() != nil {
				// The run ended while the renewal was in flight.
				return
			}
			lost.Store(true)
			cancel()
			logger.WithFields(logrus.Fields{"operation": "heartbeat", "error_code": "run_not_owned"}).
				Warn("Automation run lease lost; the executor stops")
			return
		case ctx.Err() != nil:
			return
		default:
			logger.WithField("operation", "heartbeat").WithError(utils.LogError(err)).
				Warn("Could not renew the automation run lease")
		}
	}
}

// runSteps runs the claimed run step by step until it waits, finishes, is
// cancelled under the executor, fails, loses its lease, or the runtime stops
// admitting work.
func (e *AutomationEngine) runSteps(ctx context.Context, run models.AutomationRun, leaseLost *atomic.Bool, logger *logrus.Entry) {
	rule, err := e.repo.GetRuleByID(run.RuleID)
	if err != nil {
		if isNotFound(err) {
			e.cancelRunning(run, models.AutomationCancelRuleDeleted)
			return
		}
		logger.WithError(utils.LogError(err)).Error("Automation rule could not be loaded")
		e.releaseOrFail(run, logger)
		return
	}
	if rule.Version != run.RuleVersion {
		e.cancelRunning(run, models.AutomationCancelRuleChanged)
		return
	}

	for guard := 0; guard <= models.AutomationMaxSteps; guard++ {
		if run.NextPosition > len(rule.Steps) {
			now := e.now().UTC()
			_, _ = e.repo.AdvanceRun(run.ID, run.NextPosition, e.instanceID, repository.AutomationRunAdvance{
				Status: models.AutomationRunCompleted, NextPosition: run.NextPosition, FinishedAt: &now,
			}, now)
			logger.WithField("status", models.AutomationRunCompleted).Info("Automation run finished")
			return
		}
		if e.beforeStep != nil {
			e.beforeStep(run.ID, run.NextPosition)
		}
		if leaseLost.Load() {
			logger.WithFields(logrus.Fields{"operation": "heartbeat_loss", "position": run.NextPosition}).
				Info("Automation run lease lost; no further step")
			return
		}
		if ctx.Err() != nil {
			if !leaseLost.Load() {
				e.releaseRun(run)
			}
			return
		}
		release, err := e.owner.admit()
		if err != nil {
			e.releaseRun(run)
			return
		}
		result, err := e.executeStep(&run, rule, logger)
		release()
		if err != nil {
			if errors.Is(err, errRunNotAdvanced) {
				logger.Info("Automation run changed under the executor; stopping")
				return
			}
			logger.WithField("position", run.NextPosition).WithError(utils.LogError(err)).Error("Automation step failed")
			e.releaseOrFail(run, logger)
			return
		}
		if result.terminal {
			return
		}
		run.NextPosition = result.nextPosition
	}
	logger.Warn("Automation run exceeded the step cap; released")
	e.releaseRun(run)
}

// releaseRun hands the run back to pending at its position, so the next
// tick (on any engine) continues it. Fenced on the owner: a run this engine
// no longer owns is left as it is.
func (e *AutomationEngine) releaseRun(run models.AutomationRun) {
	_, _ = e.repo.ReleaseRun(run.ID, run.NextPosition, e.instanceID, e.now())
}

// releaseOrFail releases a run whose step attempt failed, or fails it once
// the attempts are exhausted.
func (e *AutomationEngine) releaseOrFail(run models.AutomationRun, logger *logrus.Entry) {
	if run.Attempts+1 >= automationMaxAttempts {
		now := e.now().UTC()
		_, _ = e.repo.AdvanceRun(run.ID, run.NextPosition, e.instanceID, repository.AutomationRunAdvance{
			Status: models.AutomationRunFailed, NextPosition: run.NextPosition, FinishedAt: &now, ErrorCode: models.AutomationErrorStepFailed,
		}, now)
		logger.WithField("error_code", models.AutomationErrorStepFailed).Warn("Automation run failed after repeated attempts")
		return
	}
	e.releaseRun(run)
}

// cancelRunning cancels a run the executor holds, conditionally.
func (e *AutomationEngine) cancelRunning(run models.AutomationRun, reason string) {
	now := e.now().UTC()
	_, _ = e.repo.AdvanceRun(run.ID, run.NextPosition, e.instanceID, repository.AutomationRunAdvance{
		Status: models.AutomationRunCancelled, NextPosition: run.NextPosition, FinishedAt: &now, CancelReason: reason,
	}, now)
}

// nextAdvance continues the run at next, or completes it when next lies
// past the last step.
func nextAdvance(stepCount, next int, now time.Time) repository.AutomationRunAdvance {
	if next > stepCount {
		return repository.AutomationRunAdvance{Status: models.AutomationRunCompleted, NextPosition: next, FinishedAt: &now}
	}
	return repository.AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: next}
}

// advance performs the owner-fenced conditional advance and turns a miss into
// errRunNotAdvanced, logged once as a fenced write.
func (e *AutomationEngine) advance(r repository.AutomationRepository, run *models.AutomationRun, position int, adv repository.AutomationRunAdvance, now time.Time) error {
	rows, err := r.AdvanceRun(run.ID, position, e.instanceID, adv, now)
	if err != nil {
		return err
	}
	if rows != 1 {
		e.logger().WithFields(logrus.Fields{"operation": "fenced_advance", "run_id": run.ID, "position": position}).
			Info("Automation run advance matched nothing; the run moved under the executor")
		return errRunNotAdvanced
	}
	return nil
}

// lockRecord re-reads the run's record with the transaction-bound locking
// read. A record that is gone (deleted or erased) is nil.
func (e *AutomationEngine) lockRecord(r repository.AutomationRepository, run *models.AutomationRun) (*automationRecord, error) {
	switch run.EntityType {
	case models.AutomationEntityLead:
		lead, err := r.GetLeadForUpdate(run.EntityID)
		if err != nil {
			if isNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		return recordFromLead(lead), nil
	case models.AutomationEntityDeal:
		deal, err := r.GetDealForUpdate(run.EntityID)
		if err != nil {
			if isNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		return recordFromDeal(deal), nil
	case models.AutomationEntityTicket:
		// The ticket row is locked before the run advance: the order every
		// ticket writer (update, bulk status, delete) uses, so they serialise
		// on the ticket row instead of deadlocking (#242).
		ticket, err := r.GetTicketForUpdate(run.EntityID)
		if err != nil {
			if isNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		return recordFromTicket(ticket), nil
	}
	return nil, fmt.Errorf("unknown automation entity type %q", run.EntityType)
}

// loadRecord is the plain read of the dry run and of the API.
func (e *AutomationEngine) loadRecord(entityType string, entityID uint) (*automationRecord, error) {
	switch entityType {
	case models.AutomationEntityLead:
		lead, err := e.repo.GetLead(entityID)
		if err != nil {
			return nil, err
		}
		return recordFromLead(lead), nil
	case models.AutomationEntityDeal:
		deal, err := e.repo.GetDeal(entityID)
		if err != nil {
			return nil, err
		}
		return recordFromDeal(deal), nil
	case models.AutomationEntityTicket:
		ticket, err := e.repo.GetTicket(entityID)
		if err != nil {
			return nil, err
		}
		return recordFromTicket(ticket), nil
	}
	return nil, fmt.Errorf("unknown automation entity type %q: %w", entityType, apperrors.ErrValidation)
}

// resolveAssignTarget is resolveRecipient for an assign_owner step: the
// configured user, live, active and staff, and on a ticket run (#242) also a
// support or admin user, as the ticket API requires of an assignee.
func resolveAssignTarget(users repository.UserRepository, cfg models.AutomationStepConfig, rec *automationRecord) (*models.User, bool, error) {
	assignCfg := cfg
	assignCfg.Recipient = models.AutomationRecipientUser
	target, available, err := resolveRecipient(users, assignCfg, rec)
	if err != nil || !available {
		return nil, false, err
	}
	if rec.entityType == models.AutomationEntityTicket && !isTicketAssigneeRole(target.Role) {
		return nil, false, nil
	}
	return target, true, nil
}

// resolveRecipient returns the staff user a step addresses: the record's
// current owner or the configured user. A user that does not exist, is
// erased or is inactive is unavailable.
func resolveRecipient(users repository.UserRepository, cfg models.AutomationStepConfig, rec *automationRecord) (*models.User, bool, error) {
	userID := cfg.UserID
	if cfg.Recipient != models.AutomationRecipientUser {
		userID = rec.ownerID
	}
	if userID == 0 {
		return nil, false, nil
	}
	user, err := users.GetByID(userID)
	if err != nil {
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !user.IsActive || user.Role == models.RoleCustomer {
		return nil, false, nil
	}
	return user, true, nil
}

// loadOwner loads the record's owner for placeholder rendering; a missing
// owner renders blank.
func loadOwner(users repository.UserRepository, rec *automationRecord) *models.User {
	if rec.ownerID == 0 {
		return nil
	}
	owner, err := users.GetByID(rec.ownerID)
	if err != nil {
		return nil
	}
	return owner
}

// stepPlan is the decided outcome of one step inside its transaction: the
// advance to write, the effect to apply after it, and the log row.
type stepPlan struct {
	advance   repository.AutomationRunAdvance
	effect    func() error
	outcome   string
	errorCode string
	details   map[string]interface{}
}

// planStep decides what a single-transaction step does, against the locked
// record. The effect closure runs after the conditional advance.
func (e *AutomationEngine) planStep(tx *gorm.DB, r repository.AutomationRepository, run *models.AutomationRun, rule *models.AutomationRule, step models.AutomationStep, rec *automationRecord, now time.Time) (*stepPlan, error) {
	pos := step.Position
	stepCount := len(rule.Steps)
	cfg := step.Config
	users := e.users.WithTx(tx)
	plan := &stepPlan{outcome: models.AutomationOutcomeSucceeded, details: map[string]interface{}{}}

	switch step.StepType {
	case models.AutomationStepWait:
		resumeAt := now.Add(time.Duration(cfg.DurationMinutes) * time.Minute)
		plan.advance = repository.AutomationRunAdvance{Status: models.AutomationRunWaiting, NextPosition: pos + 1, ResumeAt: &resumeAt}
		plan.outcome = models.AutomationOutcomeWaiting
		plan.details["resume_at"] = resumeAt.Format(time.RFC3339)
	case models.AutomationStepCondition:
		if rec.entityType == models.AutomationEntityLead && isAttributionField(cfg.Field) {
			// Read here, inside the step transaction and after the lead's
			// locking read, so the condition sees the submission as it is now.
			attribution, err := e.triggeringAttribution(r, run)
			if err != nil {
				return nil, err
			}
			rec.attribution = attribution
		}
		ok, err := evaluateCondition(cfg, rec)
		if err != nil {
			return nil, err
		}
		plan.details["result"] = ok
		switch {
		case ok:
			plan.details["branch"] = "next"
			plan.advance = nextAdvance(stepCount, pos+1, now)
		case cfg.Else == models.AutomationElseJump:
			plan.details["branch"] = models.AutomationElseJump
			plan.details["next_position"] = cfg.ElsePosition
			plan.advance = nextAdvance(stepCount, cfg.ElsePosition, now)
		default:
			plan.details["branch"] = models.AutomationElseStop
			plan.advance = repository.AutomationRunAdvance{Status: models.AutomationRunCompleted, NextPosition: pos + 1, FinishedAt: &now}
		}
	case models.AutomationStepCreateTask:
		plan.advance = nextAdvance(stepCount, pos+1, now)
		assignee, available, err := resolveRecipient(users, cfg, rec)
		if err != nil {
			return nil, err
		}
		if !available {
			plan.outcome, plan.errorCode = models.AutomationOutcomeSkipped, models.AutomationErrorRecipientUnavailable
			break
		}
		plan.details["assignee_id"] = assignee.ID
		task := buildAutomationTask(cfg, rec, assignee.ID, now)
		plan.details["due_date"] = task.DueDate.Format(time.RFC3339)
		if task.LeadID != nil {
			plan.details["lead_id"] = *task.LeadID
		}
		if task.CustomerID != nil {
			plan.details["customer_id"] = *task.CustomerID
		}
		if task.DealID != nil {
			plan.details["deal_id"] = *task.DealID
		}
		plan.effect = func() error {
			if err := e.tasks.WithTx(tx).Create(task); err != nil {
				return err
			}
			plan.details["task_id"] = task.ID
			return nil
		}
	case models.AutomationStepAssignOwner:
		plan.advance = nextAdvance(stepCount, pos+1, now)
		target, available, err := resolveAssignTarget(users, cfg, rec)
		if err != nil {
			return nil, err
		}
		if !available {
			plan.outcome, plan.errorCode = models.AutomationOutcomeSkipped, models.AutomationErrorRecipientUnavailable
			break
		}
		plan.details["from_owner_id"] = rec.ownerID
		plan.details["to_owner_id"] = target.ID
		plan.effect = func() error {
			var err error
			switch rec.entityType {
			case models.AutomationEntityLead:
				if err = r.SetLeadOwner(rec.id, target.ID, now); err != nil {
					return err
				}
				err = automationLeadUpdated(r, rec.id, rec.lead.Status, rec.lead.Status, models.AutomationOriginAutomation, nil)
			case models.AutomationEntityDeal:
				if err = r.SetDealOwner(rec.id, target.ID, now); err != nil {
					return err
				}
				err = automationDealUpdated(r, rec.id, rec.deal.Stage, rec.deal.Stage, models.AutomationOriginAutomation, nil)
			case models.AutomationEntityTicket:
				// Only assigned_to_id and updated_at are written; the
				// ticket_updated event carries origin automation, which the
				// dispatcher claims without starting a run (#242).
				if err = r.SetTicketAssignee(rec.id, target.ID, now); err != nil {
					return err
				}
				err = automationTicketUpdated(r, rec.id, rec.ticket.Status, rec.ticket.Status, models.AutomationOriginAutomation, nil)
			}
			return err
		}
	case models.AutomationStepNotify:
		plan.advance = nextAdvance(stepCount, pos+1, now)
		recipient, available, err := resolveRecipient(users, cfg, rec)
		if err != nil {
			return nil, err
		}
		if !available {
			plan.outcome, plan.errorCode = models.AutomationOutcomeSkipped, models.AutomationErrorRecipientUnavailable
			break
		}
		plan.details["recipient_id"] = recipient.ID
		owner := loadOwner(users, rec)
		runID := run.ID
		notification := &models.Notification{
			UserID:     recipient.ID,
			Title:      truncateRunes(renderPlaceholders(cfg.Title, rec, owner), models.NotificationTitleMaxLength),
			Body:       renderPlaceholders(cfg.Body, rec, owner),
			EntityType: rec.entityType,
			EntityID:   rec.id,
			RunID:      &runID,
		}
		plan.effect = func() error {
			if err := e.notifications.WithTx(tx).Create(notification); err != nil {
				return err
			}
			plan.details["notification_id"] = notification.ID
			return nil
		}
	default:
		return nil, fmt.Errorf("unknown automation step type %q: %w", step.StepType, apperrors.ErrValidation)
	}
	return plan, nil
}

// triggeringAttribution returns the attribution of the form submission named
// by the run's triggering event (#220). It is nil — every attribution field
// reads empty — for a schedule run, an event without a submission, a
// submission that is gone, or one whose lead_id is not the run's lead.
func (e *AutomationEngine) triggeringAttribution(r repository.AutomationRepository, run *models.AutomationRun) (*formAttribution, error) {
	if run.EventID == nil || run.EntityType != models.AutomationEntityLead {
		return nil, nil
	}
	submission, err := r.GetEventFormSubmission(*run.EventID)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if submission == nil || submission.LeadID == nil || *submission.LeadID != run.EntityID {
		return nil, nil
	}
	return attributionFromSubmission(submission), nil
}

// buildAutomationTask renders the task of a create_task step: assigned to
// the resolved user, due due_in_days after execution (UTC), linked to the
// lead of a lead run, to the deal of a deal run plus the deal's customer,
// else its lead, never both of those two, and to the customer of a ticket
// run's ticket (#242). The deal link is a business link the
// rule wrote, not the rule's actor reading the deal, so it is set whoever the
// assignee is and whatever the stage. The text is rendered through the task allowlist only: a task survives the
// erasure of its lead, so no name, address, phone or company is ever copied
// into it.
func buildAutomationTask(cfg models.AutomationStepConfig, rec *automationRecord, assigneeID uint, now time.Time) *models.Task {
	due := now.UTC().Add(time.Duration(cfg.DueInDays) * 24 * time.Hour)
	priority := models.TaskPriority(cfg.Priority)
	if priority == "" {
		priority = models.TaskPriorityMedium
	}
	task := &models.Task{
		Title:        truncateRunes(renderTaskPlaceholders(cfg.Title, rec), models.AutomationStepTextMaxLength),
		Description:  renderTaskPlaceholders(cfg.Description, rec),
		Status:       models.TaskStatusPending,
		Priority:     priority,
		DueDate:      &due,
		AssignedToID: assigneeID,
	}
	switch rec.entityType {
	case models.AutomationEntityLead:
		leadID := rec.id
		task.LeadID = &leadID
	case models.AutomationEntityDeal:
		dealID := rec.id
		task.DealID = &dealID
		if rec.deal.CustomerID != nil {
			customerID := *rec.deal.CustomerID
			task.CustomerID = &customerID
		} else if rec.deal.LeadID != nil {
			leadID := *rec.deal.LeadID
			task.LeadID = &leadID
		}
	case models.AutomationEntityTicket:
		// Tasks have no ticket link (#242): the task is linked to the
		// ticket's customer only.
		if rec.ticket.CustomerID != 0 {
			customerID := rec.ticket.CustomerID
			task.CustomerID = &customerID
		}
	}
	return task
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

// executeStep executes the run's next step and reports where the run stands.
func (e *AutomationEngine) executeStep(run *models.AutomationRun, rule *models.AutomationRule, logger *logrus.Entry) (stepResult, error) {
	pos := run.NextPosition
	step := rule.Steps[pos-1]
	logger = logger.WithFields(logrus.Fields{"position": pos, "step_type": step.StepType})
	if step.StepType == models.AutomationStepSendEmail {
		return e.executeEmailStep(run, rule, step, logger)
	}

	var result stepResult
	var outcome, errorCode string
	err := e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		rec, err := e.lockRecord(r, run)
		if err != nil {
			return err
		}
		if e.afterRecordRead != nil {
			e.afterRecordRead(run.ID, pos)
		}
		now := e.now().UTC()
		if rec == nil {
			result = stepResult{terminal: true}
			outcome = models.AutomationRunCancelled
			return e.advance(r, run, pos, repository.AutomationRunAdvance{
				Status: models.AutomationRunCancelled, NextPosition: pos, FinishedAt: &now, CancelReason: models.AutomationCancelRecordDeleted,
			}, now)
		}
		plan, err := e.planStep(tx, r, run, rule, step, rec, now)
		if err != nil {
			return err
		}
		if err := e.advance(r, run, pos, plan.advance, now); err != nil {
			return err
		}
		if plan.effect != nil {
			if err := plan.effect(); err != nil {
				return err
			}
		}
		if e.afterEffect != nil {
			if err := e.afterEffect(run.ID, pos); err != nil {
				return err
			}
		}
		if err := r.CreateRunStep(&models.AutomationRunStep{
			RunID: run.ID, Position: pos, StepType: step.StepType,
			Outcome: plan.outcome, ErrorCode: plan.errorCode, Details: plan.details, ExecutedAt: now,
		}); err != nil {
			return err
		}
		outcome, errorCode = plan.outcome, plan.errorCode
		result = stepResult{terminal: plan.advance.Status != models.AutomationRunRunning, nextPosition: plan.advance.NextPosition}
		return nil
	}, automationTxRetries)
	if err != nil {
		return stepResult{}, err
	}
	entry := logger.WithField("outcome", outcome)
	if errorCode != "" {
		entry = entry.WithField("error_code", errorCode)
	}
	entry.Info("Automation step executed")
	return result, nil
}

// executeEmailStep is the two-transaction email step described at the top of
// the file.
func (e *AutomationEngine) executeEmailStep(run *models.AutomationRun, rule *models.AutomationRule, step models.AutomationStep, logger *logrus.Entry) (stepResult, error) {
	pos := step.Position
	stepCount := len(rule.Steps)

	// A row already at this position belongs to an earlier attempt. One in
	// sending may or may not have reached the relay: it is finalized as
	// unconfirmed and the run fails, so the mail is never sent twice.
	existing, err := e.repo.GetRunStep(run.ID, pos)
	switch {
	case err == nil && existing.Outcome == models.AutomationOutcomeSending:
		if err := e.failUnconfirmedEmail(run, existing); err != nil {
			return stepResult{}, err
		}
		logger.WithField("error_code", models.AutomationErrorEmailUnconfirmed).Warn("Automation email left unconfirmed; run failed")
		return stepResult{terminal: true}, nil
	case err == nil:
		return stepResult{}, errRunNotAdvanced
	case !isNotFound(err):
		return stepResult{}, err
	}

	var (
		result      stepResult
		done        bool
		doneOutcome string
		to          string
		subject     string
		body        string
		stepRowID   uint
		recipient   uint
	)
	err = e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		done = false
		rec, err := e.lockRecord(r, run)
		if err != nil {
			return err
		}
		if e.afterRecordRead != nil {
			e.afterRecordRead(run.ID, pos)
		}
		now := e.now().UTC()
		if rec == nil {
			done, doneOutcome = true, models.AutomationRunCancelled
			result = stepResult{terminal: true}
			return e.advance(r, run, pos, repository.AutomationRunAdvance{
				Status: models.AutomationRunCancelled, NextPosition: pos, FinishedAt: &now, CancelReason: models.AutomationCancelRecordDeleted,
			}, now)
		}
		// The conditional check of this half: the run row under lock, still
		// running at this position under this engine's own lease, else the
		// run moved (cancelled, recovered or re-claimed by another engine)
		// and nothing is recorded or sent.
		current, err := r.GetRunByIDForUpdate(run.ID)
		if err != nil {
			if isNotFound(err) {
				return errRunNotAdvanced
			}
			return err
		}
		if !e.holdsRun(current, pos) {
			logger.WithField("operation", "email_claim").Info("Automation email refused: the run is no longer held by this executor")
			return errRunNotAdvanced
		}
		users := e.users.WithTx(tx)
		user, available, err := resolveRecipient(users, step.Config, rec)
		if err != nil {
			return err
		}
		if !available {
			done, doneOutcome = true, models.AutomationOutcomeSkipped
			adv := nextAdvance(stepCount, pos+1, now)
			if err := e.advance(r, run, pos, adv, now); err != nil {
				return err
			}
			if err := r.CreateRunStep(&models.AutomationRunStep{
				RunID: run.ID, Position: pos, StepType: step.StepType,
				Outcome: models.AutomationOutcomeSkipped, ErrorCode: models.AutomationErrorRecipientUnavailable,
				Details: map[string]interface{}{}, ExecutedAt: now,
			}); err != nil {
				return err
			}
			result = stepResult{terminal: adv.Status != models.AutomationRunRunning, nextPosition: pos + 1}
			return nil
		}
		owner := loadOwner(users, rec)
		to = strings.TrimSpace(user.Email)
		subject = renderPlaceholders(step.Config.Subject, rec, owner)
		body = renderPlaceholders(step.Config.Body, rec, owner)
		recipient = user.ID
		row := &models.AutomationRunStep{
			RunID: run.ID, Position: pos, StepType: step.StepType,
			Outcome: models.AutomationOutcomeSending, Details: map[string]interface{}{"recipient_id": user.ID}, ExecutedAt: now,
		}
		if err := r.CreateRunStep(row); err != nil {
			return err
		}
		stepRowID = row.ID
		return nil
	}, automationTxRetries)
	if err != nil {
		return stepResult{}, err
	}
	if done {
		logger.WithField("outcome", doneOutcome).Info("Automation email step resolved without a send")
		return result, nil
	}

	if e.beforeSend != nil {
		e.beforeSend(run.ID, pos)
	}
	// The pre-send check (#237): a plain read of the run just before the
	// relay is called. A run that another engine's sweep failed as
	// email_unconfirmed, that was cancelled or re-claimed while this executor
	// was paused is not sent and not stamped. The window between this read
	// and Send stays open; the row then means "outcome unknown" and nothing
	// resends it.
	current, err := e.repo.GetRunByID(run.ID)
	if err != nil {
		if isNotFound(err) {
			return stepResult{}, errRunNotAdvanced
		}
		return stepResult{}, err
	}
	if !e.holdsRun(current, pos) {
		logger.WithField("operation", "pre_send_check").Warn("Automation email not sent: the run is no longer held by this executor")
		return stepResult{}, errRunNotAdvanced
	}
	var sendErr error
	if e.mailer == nil {
		sendErr = errors.New("no mailer is configured")
	} else {
		sendErr = e.mailer.Send(to, subject, body)
	}
	if sendErr != nil {
		// The transport error may quote the address; only the fact is logged.
		logger.WithField("error_code", models.AutomationErrorMailFailed).Warn("Automation email delivery failed")
	}
	if e.afterSend != nil {
		if err := e.afterSend(run.ID, pos); err != nil {
			return stepResult{}, err
		}
	}

	advanced, stampedRow := false, false
	err = e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		now := e.now().UTC()
		outcome, errorCode := models.AutomationOutcomeSucceeded, ""
		if sendErr != nil {
			outcome, errorCode = models.AutomationOutcomeFailed, models.AutomationErrorMailFailed
		}
		// The run row first, then the step row: the order of the first half
		// and of the stale sweep, so they queue instead of deadlocking.
		adv := nextAdvance(stepCount, pos+1, now)
		rows, err := r.AdvanceRun(run.ID, pos, e.instanceID, adv, now)
		if err != nil {
			return err
		}
		// Stamped only while the row is still sending: a row the stale sweep
		// already failed as email_unconfirmed keeps that outcome.
		stamped, err := r.FinishSendingStep(stepRowID, outcome, errorCode, map[string]interface{}{"recipient_id": recipient})
		if err != nil {
			return err
		}
		advanced, stampedRow = rows == 1, stamped == 1
		if rows != 1 {
			// Cancelled or recovered between the two halves: the run stays as
			// the cancel or the sweep left it.
			result = stepResult{terminal: true}
			return nil
		}
		result = stepResult{terminal: adv.Status != models.AutomationRunRunning, nextPosition: pos + 1}
		return nil
	}, automationTxRetries)
	if err != nil {
		return stepResult{}, err
	}
	if !stampedRow {
		logger.WithField("operation", "email_stamp").Warn("Automation email outcome not recorded: the step row was already finalized")
	}
	if !advanced {
		logger.WithField("operation", "fenced_advance").Info("Automation run moved between the two email halves; stopping")
		return result, nil
	}
	entry := logger.WithField("outcome", models.AutomationOutcomeSucceeded)
	if sendErr != nil {
		entry = logger.WithFields(logrus.Fields{"outcome": models.AutomationOutcomeFailed, "error_code": models.AutomationErrorMailFailed})
	}
	entry.Info("Automation step executed")
	return result, nil
}

// holdsRun reports whether the run row is still running at position under
// this engine's own lease.
func (e *AutomationEngine) holdsRun(run *models.AutomationRun, position int) bool {
	return run.Status == models.AutomationRunRunning && run.NextPosition == position &&
		run.OwnerInstance != nil && *run.OwnerInstance == e.instanceID
}

// failUnconfirmedEmail fails the run with email_unconfirmed through the
// owner-fenced advance and, in the same transaction, stamps the sending row
// failed with the same code (only while it is still sending). An advance that
// matches nothing rolls both back and stops the executor.
func (e *AutomationEngine) failUnconfirmedEmail(run *models.AutomationRun, row *models.AutomationRunStep) error {
	return e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		now := e.now().UTC()
		if err := e.advance(r, run, run.NextPosition, repository.AutomationRunAdvance{
			Status: models.AutomationRunFailed, NextPosition: run.NextPosition, FinishedAt: &now, ErrorCode: models.AutomationErrorEmailUnconfirmed,
		}, now); err != nil {
			return err
		}
		_, err := r.FinishSendingStep(row.ID, models.AutomationOutcomeFailed, models.AutomationErrorEmailUnconfirmed, row.Details)
		return err
	}, automationTxRetries)
}

// --- Stale sweep -------------------------------------------------------------

// SweepStaleRuns recovers the running runs whose lease went stale: the
// heartbeat is NULL (a run claimed before #237) or older than
// automationRunLeaseStaleAfter. It runs at boot, before the worker starts,
// and at the start of every tick, on every engine. Selection is a plain read;
// each recovery is a conditional update by primary key that re-checks the
// status, the observed owner and the staleness, so a run whose owner renewed
// its lease, that another engine recovered or re-claimed, or that finished
// meanwhile is left alone. A run whose last step row is a send_email still in
// sending is failed with email_unconfirmed together with that row (the mail
// may have gone out and is never sent again); every other stale run goes back
// to pending at its next_position with attempts unchanged. It returns how
// many runs went back to pending and how many failed.
func (e *AutomationEngine) SweepStaleRuns(ctx context.Context) (recovered int, failed int, err error) {
	now := e.now().UTC()
	cutoff := now.Add(-automationRunLeaseStaleAfter)
	runs, err := e.repo.ListStaleRunningRuns(cutoff, automationSweepBatch)
	if err != nil {
		return 0, 0, err
	}
	var errs []error
	for i := range runs {
		if ctx.Err() != nil {
			break
		}
		outcome, err := e.recoverStaleRun(runs[i], cutoff, now)
		if err != nil {
			errs = append(errs, err)
			e.logger().WithFields(logrus.Fields{"operation": "sweep", "run_id": runs[i].ID}).
				WithError(utils.LogError(err)).Error("Automation stale run recovery failed")
			continue
		}
		switch outcome {
		case automationRecoveredPending:
			recovered++
		case automationRecoveredFailed:
			failed++
		}
	}
	return recovered, failed, errors.Join(errs...)
}

// recoverStaleRun recovers one stale run in one transaction: the run row
// first, then the sending step row, the lock order of the email step. It
// returns the outcome, or "" when the run was spared because it moved since
// the sweep read it.
func (e *AutomationEngine) recoverStaleRun(run models.AutomationRun, cutoff, now time.Time) (string, error) {
	logger := e.logger().WithFields(logrus.Fields{
		"operation": "takeover", "run_id": run.ID, "rule_id": run.RuleID,
		"entity_type": run.EntityType, "entity_id": run.EntityID, "position": run.NextPosition,
	})
	last, err := e.repo.LastRunStep(run.ID)
	if err != nil {
		return "", err
	}
	unconfirmed := last != nil && last.StepType == models.AutomationStepSendEmail && last.Outcome == models.AutomationOutcomeSending
	failCode := ""
	if unconfirmed {
		failCode = models.AutomationErrorEmailUnconfirmed
	}
	err = e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		rows, err := r.RecoverStaleRun(run.ID, run.OwnerInstance, cutoff, failCode, now)
		if err != nil {
			return err
		}
		if rows != 1 {
			return errRunNotAdvanced
		}
		if e.afterStaleRecover != nil {
			e.afterStaleRecover(run.ID)
		}
		if !unconfirmed {
			return nil
		}
		stamped, err := r.FinishSendingStep(last.ID, models.AutomationOutcomeFailed, models.AutomationErrorEmailUnconfirmed, last.Details)
		if err != nil {
			return err
		}
		if stamped != 1 {
			// The executor stamped the row after the sweep read it: the next
			// sweep decides on what the row says now.
			return errRunNotAdvanced
		}
		return nil
	}, automationTxRetries)
	if errors.Is(err, errRunNotAdvanced) {
		logger.Info("Automation run spared by the sweep: it moved since it was read")
		return "", nil
	}
	if err != nil {
		return "", err
	}
	outcome := automationRecoveredPending
	entry := logger.WithField("outcome", outcome)
	if unconfirmed {
		outcome = automationRecoveredFailed
		entry = logger.WithFields(logrus.Fields{"outcome": outcome, "error_code": models.AutomationErrorEmailUnconfirmed})
	}
	entry.Warn("Automation run recovered from a stale lease")
	if e.recoveryObserver != nil {
		e.recoveryObserver(run.ID, outcome)
	}
	return outcome, nil
}
