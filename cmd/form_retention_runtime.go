package main

import (
	"context"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// formRetentionBackground owns the retention purge of this process. Stop is what
// main calls at shutdown, through the combined handle setupDependencies returns
// (combineStops: automation, then this, then AEO), under the one drain deadline
// main passes, aeoDrainTimeout (15 s). A nil runtime (FORM_RETENTION_DAYS unset)
// has nothing to start or stop.
type formRetentionBackground struct {
	runtime       *service.FormRetentionRuntime
	retentionDays int
}

// newFormRetentionBackground builds the purge from the forms configuration. It
// builds nothing while the retention is 0, so the purge stays off until the
// setting is configured.
func newFormRetentionBackground(cfg config.FormsConfig, repo repository.FormRepository, txManager *utils.TransactionManager) *formRetentionBackground {
	if cfg.RetentionDays <= 0 {
		return &formRetentionBackground{}
	}
	retention := time.Duration(cfg.RetentionDays) * 24 * time.Hour
	purger := service.NewFormSubmissionPurger(repo, txManager, retention)
	return &formRetentionBackground{
		runtime:       service.NewFormRetentionRuntime(purger, service.FormRetentionSweepInterval),
		retentionDays: cfg.RetentionDays,
	}
}

// start announces the purge and arms it on a context derived from ctx. The first
// sweep runs straight away, so an existing backlog is removed on this boot.
func (b *formRetentionBackground) start(ctx context.Context) {
	if b.runtime == nil {
		return
	}
	service.LogFormRetentionEnabled(b.retentionDays)
	b.runtime.Start(ctx)
}

// Stop cancels the purge and waits for it under ctx. It returns nil once no chunk
// is in flight. Repeated calls are safe.
func (b *formRetentionBackground) Stop(ctx context.Context) error {
	if b.runtime == nil {
		return nil
	}
	return b.runtime.Stop(ctx)
}
