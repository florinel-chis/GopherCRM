package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/sirupsen/logrus"
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

	// webhooks writes the lead.updated / ticket.updated rows of an
	// assign_owner step that changed the record (#408), bound to the step
	// transaction. webhook.Nop unless WithAutomationWebhooks is given.
	webhooks webhook.Emitter

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

	// beforeStaleRecover fires in the sweep after its plain read of the run's
	// last step row and before the recovery transaction begins.
	beforeStaleRecover func(runID uint)

	// afterLeaseLost fires in the heartbeat goroutine once a renewal refused
	// with ErrAutomationRunNotOwned has marked the lease lost and cancelled
	// the run's context, so a root can resume a parked executor knowing it
	// will see the loss.
	afterLeaseLost func(runID uint)

	// beforeClaim fires in ExecuteDue after the batch's plain read and before
	// each run's conditional claim, so a root can let another engine claim,
	// step and release the run in between (the stale-snapshot interleaving).
	beforeClaim func(runID uint)

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

// AutomationEngineOption configures an AutomationEngine at construction.
type AutomationEngineOption func(*AutomationEngine)

// WithAutomationWebhooks makes the assign_owner step emit lead.updated (lead
// run) or ticket.updated (ticket run) through e, bound to the step
// transaction, when the new owner or assignee changed the record's
// WebhookState (#408). A deal run emits nothing, and a dry run runs no effect
// and writes no row. A nil e means webhook.Nop.
func WithAutomationWebhooks(e webhook.Emitter) AutomationEngineOption {
	return func(engine *AutomationEngine) {
		if e == nil {
			e = webhook.Nop
		}
		engine.webhooks = e
	}
}

// NewAutomationEngine wires the engine. The mailer may be nil, in which case
// every send_email step fails with mail_failed. Without WithAutomationWebhooks
// the engine writes no webhook rows.
func NewAutomationEngine(repo repository.AutomationRepository, notifications repository.NotificationRepository, tasks repository.TaskRepository, users repository.UserRepository, m mailer.Mailer, txManager *utils.TransactionManager, opts ...AutomationEngineOption) *AutomationEngine {
	e := &AutomationEngine{
		repo:          repo,
		notifications: notifications,
		tasks:         tasks,
		users:         users,
		mailer:        m,
		txManager:     txManager,
		now:           time.Now,
		owner:         newAutomationOwner(),
		webhooks:      webhook.Nop,
	}
	for _, opt := range opts {
		opt(e)
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

func (e *AutomationEngine) logger() *logrus.Entry {
	if utils.Logger == nil {
		return logrus.NewEntry(logrus.StandardLogger())
	}
	return utils.Logger.WithField("component", "automation")
}
