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
// older than service.BulkOperationStaleAfter. Stop is what main calls at
// shutdown, through the combined handle setupDependencies returns
// (combineStops: automation, form retention, this, then AEO), under the one
// drain deadline main passes, aeoDrainTimeout (15 s).
type bulkRecoveryBackground struct {
	runtime *service.BulkOperationRecoveryRuntime
}

// newBulkRecoveryBackground builds the sweep over the bulk history repository.
func newBulkRecoveryBackground(repo repository.BulkOperationRepository) *bulkRecoveryBackground {
	return &bulkRecoveryBackground{runtime: service.NewBulkOperationRecoveryRuntime(repo, utils.Logger)}
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
