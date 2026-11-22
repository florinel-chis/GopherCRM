package service

import (
	"context"
	"time"

	"github.com/florinel-chis/gophercrm/internal/lifecycle"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// Stale bulk operation recovery (#196).
//
// A bulk status batch writes its history row pending, and completed inside the
// batch transaction, so a row that is still pending belongs to a batch that did
// not commit: none of its records changed. The failed write that follows a
// rolled-back batch is a separate, bounded statement that can time out (the
// connection or the row held elsewhere). The sweep below gives every such row
// its terminal status: a pending row older than BulkOperationStaleAfter becomes
// failed.
//
// Rows already pending when this was deployed are the one exception: their
// batch recorded completed after its commit, so it may have committed. They are
// swept to failed all the same; the outcome is unknown, and that is documented,
// not stored.

const (
	// BulkOperationStaleAfter is how old a pending bulk_operations row must be
	// before the sweep fails it. A batch holds at most MaxBulkStatusItems
	// records and finishes in seconds, so an hour is far past any live batch.
	BulkOperationStaleAfter = time.Hour
	// BulkOperationSweepInterval is how often every replica sweeps.
	BulkOperationSweepInterval = 15 * time.Minute
)

// SweepStaleBulkOperations fails every live pending bulk operation created
// before now - BulkOperationStaleAfter and returns how many rows it changed.
func SweepStaleBulkOperations(ctx context.Context, repo repository.BulkOperationRepository, now time.Time) (int64, error) {
	return repo.MarkStalePendingFailed(ctx, now.Add(-BulkOperationStaleAfter))
}

// BulkOperationRecoveryOption configures NewBulkOperationRecoveryRuntime.
type BulkOperationRecoveryOption func(*BulkOperationRecoveryRuntime)

// WithBulkRecoveryClock replaces the wall clock the cutoff is computed from.
func WithBulkRecoveryClock(now func() time.Time) BulkOperationRecoveryOption {
	return func(r *BulkOperationRecoveryRuntime) {
		if now != nil {
			r.now = now
		}
	}
}

// WithBulkRecoveryTicker replaces the interval ticker. newTicker returns the
// channel the loop waits on and the function that releases it.
func WithBulkRecoveryTicker(newTicker func(interval time.Duration) (<-chan time.Time, func())) BulkOperationRecoveryOption {
	return func(r *BulkOperationRecoveryRuntime) {
		if newTicker != nil {
			r.newTicker = newTicker
		}
	}
}

// WithOIDCAttemptPruning makes every Sweep also prune the
// oidc_login_attempts rows older than OIDCLoginAttemptRetention (#453),
// independently of the bulk operation result.
func WithOIDCAttemptPruning(repo repository.OIDCLoginAttemptRepository) BulkOperationRecoveryOption {
	return func(r *BulkOperationRecoveryRuntime) {
		r.oidcAttempts = repo
	}
}

// WithAuthAuditPruning makes every Sweep also prune the auth_audit_log rows
// older than retentionDays days (#454), independently of the bulk operation
// result.
func WithAuthAuditPruning(repo repository.AuthAuditRepository, retentionDays int) BulkOperationRecoveryOption {
	return func(r *BulkOperationRecoveryRuntime) {
		r.authAudit = repo
		r.authAuditRetentionDays = retentionDays
	}
}

// WithBulkRecoveryInterval replaces BulkOperationSweepInterval; a
// non-positive value keeps it.
func WithBulkRecoveryInterval(interval time.Duration) BulkOperationRecoveryOption {
	return func(r *BulkOperationRecoveryRuntime) {
		if interval > 0 {
			r.interval = interval
		}
	}
}

// BulkOperationRecoveryRuntime owns the goroutine that sweeps once at Start and
// then on every interval tick. It is safe on every replica: two sweeps only
// compete for the same rows, and each row changes once. main drains it before
// closing the database: Stop cancels the sweep and joins the goroutine under
// the caller's deadline. A runtime runs once: Start after Stop does nothing.
type BulkOperationRecoveryRuntime struct {
	repo      repository.BulkOperationRepository
	logger    *logrus.Logger
	now       func() time.Time
	newTicker func(time.Duration) (<-chan time.Time, func())
	interval  time.Duration
	// oidcAttempts, when set, is pruned on every sweep (#453).
	oidcAttempts repository.OIDCLoginAttemptRepository
	// authAudit, when set, is pruned on every sweep to
	// authAuditRetentionDays (#454).
	authAudit              repository.AuthAuditRepository
	authAuditRetentionDays int

	loop *lifecycle.Loop
}

// NewBulkOperationRecoveryRuntime builds the runtime over repo. A nil logger
// means utils.Logger.
func NewBulkOperationRecoveryRuntime(repo repository.BulkOperationRepository, logger *logrus.Logger, opts ...BulkOperationRecoveryOption) *BulkOperationRecoveryRuntime {
	r := &BulkOperationRecoveryRuntime{
		repo:     repo,
		logger:   logger,
		now:      time.Now,
		interval: BulkOperationSweepInterval,
		newTicker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
	}
	for _, opt := range opts {
		opt(r)
	}
	r.loop = lifecycle.NewLoop("bulk operation recovery runtime", r.interval,
		func(ctx context.Context) { _, _ = r.Sweep(ctx) }, lifecycle.WithTicker(r.newTicker))
	return r
}

// Interval is the time between two scheduled sweeps.
func (r *BulkOperationRecoveryRuntime) Interval() time.Duration { return r.interval }

// Sweep runs one pass with the runtime's clock and logs its count. With
// WithOIDCAttemptPruning it then prunes the old sign-in attempts, and with
// WithAuthAuditPruning the old audit rows; each prune is logged on its own and
// never changes the returned count or error.
func (r *BulkOperationRecoveryRuntime) Sweep(ctx context.Context) (int64, error) {
	now := r.now()
	failed, err := SweepStaleBulkOperations(ctx, r.repo, now)
	entry := r.log().WithField("failed", failed)
	switch {
	case err == nil && failed > 0:
		entry.Info("bulk operation recovery sweep failed stale pending rows")
	case err == nil:
		entry.Debug("bulk operation recovery sweep completed")
	case ctx.Err() != nil:
		entry.Info("bulk operation recovery sweep interrupted")
	default:
		entry.WithError(utils.LogError(err)).Warn("bulk operation recovery sweep failed")
	}
	r.pruneOIDCAttempts(ctx, now)
	r.pruneAuthAudit(ctx, now)
	return failed, err
}

// pruneAuthAudit deletes the audit rows older than the retention, when the
// option is set.
func (r *BulkOperationRecoveryRuntime) pruneAuthAudit(ctx context.Context, now time.Time) {
	if r.authAudit == nil {
		return
	}
	pruned, err := PruneAuthAuditLog(ctx, r.authAudit, now, r.authAuditRetentionDays)
	entry := r.log().WithField("pruned", pruned)
	switch {
	case err == nil && pruned > 0:
		entry.Info("pruned sign-in audit rows")
	case err == nil:
	case ctx.Err() != nil:
		entry.Info("sign-in audit prune interrupted")
	default:
		entry.WithError(utils.LogError(err)).Warn("sign-in audit prune failed")
	}
}

// pruneOIDCAttempts deletes the sign-in attempts created more than
// OIDCLoginAttemptRetention before now, when the option is set.
func (r *BulkOperationRecoveryRuntime) pruneOIDCAttempts(ctx context.Context, now time.Time) {
	if r.oidcAttempts == nil {
		return
	}
	pruned, err := PruneOIDCLoginAttempts(ctx, r.oidcAttempts, now)
	entry := r.log().WithField("pruned", pruned)
	switch {
	case err == nil && pruned > 0:
		entry.Info("pruned single sign-on attempts")
	case err == nil:
	case ctx.Err() != nil:
		entry.Info("single sign-on attempt prune interrupted")
	default:
		entry.WithError(utils.LogError(err)).Warn("single sign-on attempt prune failed")
	}
}

func (r *BulkOperationRecoveryRuntime) log() *logrus.Logger {
	if r.logger != nil {
		return r.logger
	}
	return utils.Logger
}

// Start sweeps once immediately on a goroutine derived from ctx, then on every
// tick. Starting twice, or after Stop, is a no-op.
func (r *BulkOperationRecoveryRuntime) Start(ctx context.Context) { r.loop.Start(ctx) }

// Stop cancels the sweep and waits for the goroutine to return, or for ctx to
// end. A statement in flight finishes or is abandoned before Stop returns nil,
// and after that the runtime writes nothing. A deadline that passes first is an
// error. Once the goroutine has joined, a repeated Stop returns nil even on a
// context that has already ended.
func (r *BulkOperationRecoveryRuntime) Stop(ctx context.Context) error { return r.loop.Stop(ctx) }
