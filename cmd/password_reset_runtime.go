package main

import (
	"context"

	"github.com/florinel-chis/gophercrm/internal/service"
)

// passwordResetBackground owns the password reset queue of this process: the
// handler of POST /auth/password-reset submits the email and answers at once,
// and the queue's one worker issues and mails the link and writes the
// reset_request audit row, in submission order. Stop is what main calls at
// shutdown, through the combined handle setupDependencies returns
// (combineStops, which stops it together with the other runtimes, before the
// database handle closes), under the one drain deadline main passes,
// aeoDrainTimeout (15 s).
type passwordResetBackground struct {
	queue *service.PasswordResetQueue
}

// newPasswordResetBackground builds the queue over the auth service, which
// issues each reset, and the sign-in audit writer.
func newPasswordResetBackground(issuer service.PasswordResetIssuer, audit service.AuthAuditService) *passwordResetBackground {
	return &passwordResetBackground{queue: service.NewPasswordResetQueue(issuer, audit)}
}

// start runs the worker.
func (b *passwordResetBackground) start(ctx context.Context) {
	b.queue.Start(ctx)
}

// Stop refuses new requests, lets the one in flight finish, drops the waiting
// ones and waits for the worker under ctx. Repeated calls are safe.
func (b *passwordResetBackground) Stop(ctx context.Context) error {
	return b.queue.Stop(ctx)
}
