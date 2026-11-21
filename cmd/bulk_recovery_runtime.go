package main

import (
	"context"

	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// bulkRecoveryBackground owns the stale bulk operation sweep of this process
// (#196). It runs on every replica: at boot, then every
// service.BulkOperationSweepInterval, it fails the pending bulk_operations rows
// older than service.BulkOperationStaleAfter, and deletes the
// oidc_login_attempts rows created more than service.OIDCLoginAttemptRetention
// (one day) before (#453) and the auth_audit_log rows older than
// AUTH_AUDIT_RETENTION_DAYS (#454). Stop is what main calls at
// shutdown, through the combined handle setupDependencies returns
// (combineStops, which stops it together with the other runtimes), under the
// one drain deadline main passes, aeoDrainTimeout (15 s).
type bulkRecoveryBackground struct {
	runtime *service.BulkOperationRecoveryRuntime
}

// newBulkRecoveryBackground builds the sweep over the bulk history repository,
// the single sign-on attempt repository it also prunes, and the sign-in audit
// repository it prunes to authAuditRetentionDays.
func newBulkRecoveryBackground(repo repository.BulkOperationRepository, oidcAttempts repository.OIDCLoginAttemptRepository,
	authAudit repository.AuthAuditRepository, authAuditRetentionDays int) *bulkRecoveryBackground {
	return &bulkRecoveryBackground{runtime: service.NewBulkOperationRecoveryRuntime(repo, utils.Logger,
		service.WithOIDCAttemptPruning(oidcAttempts), service.WithAuthAuditPruning(authAudit, authAuditRetentionDays))}
}

// start arms the sweep on a context derived from ctx. The first sweep runs
// straight away, so rows a previous process left pending are failed on this
// boot without any request.
func (b *bulkRecoveryBackground) start(ctx context.Context) {
	b.runtime.Start(ctx)
}

// Stop cancels the sweep and waits for it under ctx. Repeated calls are safe.
func (b *bulkRecoveryBackground) Stop(ctx context.Context) error {
	return b.runtime.Stop(ctx)
}
