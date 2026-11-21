package service

import (
	"context"
	"fmt"
	"sync"

	"github.com/florinel-chis/gophercrm/internal/utils"
)

// PasswordResetQueueSize is how many password reset requests the queue holds
// before Submit refuses new ones.
const PasswordResetQueueSize = 100

// PasswordResetIssuer issues one password reset: the account lookup, the token
// transaction and the mail. AuthService satisfies it.
type PasswordResetIssuer interface {
	IssuePasswordReset(email string) (uint, error)
}

// PasswordResetQueueOption configures a PasswordResetQueue.
type PasswordResetQueueOption func(*PasswordResetQueue)

// withPasswordResetQueueCapacity replaces PasswordResetQueueSize (tests).
func withPasswordResetQueueCapacity(n int) PasswordResetQueueOption {
	return func(q *PasswordResetQueue) { q.capacity = n }
}

// passwordResetItem is one queued request: the email and the submitting
// request's context without its cancellation, so the audit row keeps the
// request id and client facts after the request has ended.
type passwordResetItem struct {
	ctx   context.Context
	email string
}

// PasswordResetQueue runs POST /auth/password-reset after the answer: the
// handler submits the email and answers at once, and the queue issues the
// reset (IssuePasswordReset) and writes the reset_request audit row.
//
// It has exactly ONE worker goroutine, so requests are issued one at a time in
// submission order; the cmd wiring tests and the browser tests rely on that
// order. It holds at most PasswordResetQueueSize waiting requests; Submit on a
// full or stopped queue drops the request, logs one warning and records an
// unavailable reset_request row with no user id. Stop lets the request in
// flight finish, drops the waiting ones and joins the worker. No log line of
// the queue carries the email, the token or the link.
type PasswordResetQueue struct {
	issuer   PasswordResetIssuer
	audit    AuthAuditService
	capacity int

	mu      sync.Mutex
	items   chan passwordResetItem
	started bool
	stopped bool
	quit    chan struct{}
	exited  chan struct{}
}

// NewPasswordResetQueue builds a queue over issuer; audit may be nil, then no
// audit row is written.
func NewPasswordResetQueue(issuer PasswordResetIssuer, audit AuthAuditService, opts ...PasswordResetQueueOption) *PasswordResetQueue {
	q := &PasswordResetQueue{issuer: issuer, audit: audit, capacity: PasswordResetQueueSize}
	for _, opt := range opts {
		opt(q)
	}
	if q.capacity < 1 {
		q.capacity = 1
	}
	q.items = make(chan passwordResetItem, q.capacity)
	q.quit = make(chan struct{})
	return q
}

// Start runs the worker. Repeated calls, and a call after Stop, do nothing.
// The worker is not tied to ctx: it ends through Stop, so a request accepted
// before the shutdown is either issued or counted as dropped.
func (q *PasswordResetQueue) Start(_ context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.started || q.stopped {
		return
	}
	q.started = true
	q.exited = make(chan struct{})
	go q.loop(q.exited)
}

// Submit queues email for the worker and returns true. It never blocks and
// touches no database while the queue has room. A full or stopped queue
// returns false, logs a warning and records an unavailable reset_request row.
func (q *PasswordResetQueue) Submit(ctx context.Context, email string) bool {
	item := passwordResetItem{ctx: context.WithoutCancel(ctx), email: email}
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		q.refuse(item.ctx, "reset_queue_stopped", "Password reset request dropped: the queue is stopped")
		return false
	}
	select {
	case q.items <- item:
		q.mu.Unlock()
		return true
	default:
	}
	q.mu.Unlock()
	q.refuse(item.ctx, "reset_queue_full", "Password reset request dropped: the queue is full")
	return false
}

func (q *PasswordResetQueue) refuse(ctx context.Context, code, message string) {
	utils.Logger.WithField("operation", "PasswordResetQueue.Submit").WithField("error_code", code).Warn(message)
	if q.audit != nil {
		q.audit.RecordPasswordResetRequest(ctx, 0, authRefusal(AuthAuditReasonUnavailable, 0, errPasswordResetNotIssued))
	}
}

func (q *PasswordResetQueue) loop(exited chan<- struct{}) {
	defer close(exited)
	for {
		// Stop wins over a waiting item: after quit closes, nothing new starts.
		select {
		case <-q.quit:
			return
		default:
		}
		select {
		case <-q.quit:
			return
		case item := <-q.items:
			q.run(item)
		}
	}
}

// run issues one request; a panic is recovered and logged, and the worker
// carries on with the next item.
func (q *PasswordResetQueue) run(item passwordResetItem) {
	defer func() {
		if r := recover(); r != nil {
			utils.Logger.WithField("operation", "PasswordResetQueue.run").WithField("error_code", "reset_worker_panic").
				Error("Password reset worker recovered from a panic")
		}
	}()
	userID, err := q.issuer.IssuePasswordReset(item.email)
	if q.audit != nil {
		q.audit.RecordPasswordResetRequest(item.ctx, userID, err)
	}
}

// Stop refuses new requests, lets the request in flight finish, drops the
// waiting ones (logging their count) and waits for the worker under ctx; it
// returns ctx's error when the deadline passes first. Repeated calls are safe.
func (q *PasswordResetQueue) Stop(ctx context.Context) error {
	q.mu.Lock()
	if !q.stopped {
		q.stopped = true
		close(q.quit)
	}
	exited := q.exited
	q.mu.Unlock()

	if exited != nil {
		select {
		case <-exited:
		case <-ctx.Done():
			return fmt.Errorf("password reset queue did not stop before the deadline: %w", ctx.Err())
		}
	}
	// The worker has returned (or never ran), so nothing reads q.items any more.
	dropped := 0
drain:
	for {
		select {
		case <-q.items:
			dropped++
		default:
			break drain
		}
	}
	if dropped > 0 {
		utils.Logger.WithField("operation", "PasswordResetQueue.Stop").WithField("dropped", dropped).
			Warn("Password reset requests dropped at shutdown")
	}
	return nil
}
