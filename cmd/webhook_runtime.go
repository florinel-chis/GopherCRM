package main

import (
	"context"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// webhookRuntimeBuilt is a test hook: it receives the runtime setupDependencies
// builds, so the wiring roots can run a pass on demand. Nil in production.
var webhookRuntimeBuilt func(*service.WebhookRuntime)

// webhookBackground owns the outbound webhook delivery runtime (#345). It runs
// on every replica: claims are conditional, so replicas only compete for the
// same rows. Stop is joined through combineStops before the database closes.
type webhookBackground struct {
	runtime *service.WebhookRuntime
}

// newWebhookBackground builds the runtime with the real public-only egress
// guard (no AEO allowlist or local exception applies to it) and the secret
// box of the endpoint secrets.
func newWebhookBackground(repo repository.WebhookRepository, box *utils.SecretBox) *webhookBackground {
	guard := aeo.NewEgressGuard(config.AEOEgressPolicy{})
	runtime := service.NewWebhookRuntime(repo, guard, box)
	if webhookRuntimeBuilt != nil {
		webhookRuntimeBuilt(runtime)
	}
	return &webhookBackground{runtime: runtime}
}

// start runs the first pass at once and then one every
// service.WebhookPollInterval.
func (b *webhookBackground) start(ctx context.Context) {
	b.runtime.Start(ctx)
}

// Stop cancels the loop and joins it, an attempt in flight included.
func (b *webhookBackground) Stop(ctx context.Context) error {
	return b.runtime.Stop(ctx)
}
