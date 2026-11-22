package lifecycle

import (
	"context"
	"fmt"
	"sync"
)

// Owner is the lifecycle of owned background work: the admission count, the
// context every owned unit runs on and the drain a shutdown waits for. A unit
// is admitted before its first side effect and released after its last one;
// Shutdown closes admission, cancels the context and waits for the count to
// reach zero. Admission and the count share the owner's own mutex, so a unit
// is either counted before the close or refused after it.
type Owner struct {
	shuttingDown error
	deadlineErr  func(active int, err error) error

	mu sync.Mutex
	// ctx is cancelled by the first Shutdown.
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	active int
	// drained is closed exactly once, when admission is closed and the last
	// admitted unit has released.
	drained  chan struct{}
	signaled bool
}

// NewOwner builds an open owner. Admit returns shuttingDown once the owner is
// closed; Shutdown returns deadlineErr(active units, ctx.Err()) when its
// context ends before the drain.
func NewOwner(shuttingDown error, deadlineErr func(active int, err error) error) *Owner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Owner{shuttingDown: shuttingDown, deadlineErr: deadlineErr, ctx: ctx, cancel: cancel, drained: make(chan struct{})}
}

// Admit registers one unit of owned work. Once the owner is closed, or its
// context has ended, it returns the owner's shutting-down error. The returned
// release is idempotent.
func (o *Owner) Admit() (release func(), err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.ctx.Err() != nil {
		return nil, o.shuttingDown
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

// signalIfDrained closes drained when nothing can be, or is, running. Callers
// hold o.mu.
func (o *Owner) signalIfDrained() {
	if o.closed && o.active == 0 && !o.signaled {
		o.signaled = true
		close(o.drained)
	}
}

// Context is the owned context; it ends when Shutdown begins.
func (o *Owner) Context() context.Context { return o.ctx }

// Active reports the admitted units still registered.
func (o *Owner) Active() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.active
}

// Shutdown closes admission, cancels the owned context and waits for every
// admitted unit to release, or for ctx to end. A drained owner returns nil
// whatever the caller's context, so a repeated Shutdown after the drain is a
// no-op. It is safe to call repeatedly and concurrently: later calls only wait.
func (o *Owner) Shutdown(ctx context.Context) error {
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		o.cancel()
	}
	o.signalIfDrained()
	o.mu.Unlock()

	select {
	case <-o.drained:
		return nil
	default:
	}
	select {
	case <-o.drained:
		return nil
	case <-ctx.Done():
		if o.deadlineErr != nil {
			return o.deadlineErr(o.Active(), ctx.Err())
		}
		return fmt.Errorf("%d unit(s) of work still active at the shutdown deadline: %w", o.Active(), ctx.Err())
	}
}
