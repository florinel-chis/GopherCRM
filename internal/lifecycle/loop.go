// Package lifecycle holds the start/stop and admission types the background
// runtimes share: Loop, a goroutine that runs a body at Start and then on
// every tick or Wake until Stop, and Owner, the admission count and context
// of owned work that a shutdown closes and drains. It depends on the standard
// library only.
package lifecycle

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Loop runs body once at Start and then on every tick or Wake, on one
// goroutine, until Stop, the parent context or the done channel ends it. A
// loop runs once: a second Start, or a Start after Stop, does nothing, so a
// drained process cannot begin working again.
type Loop struct {
	name      string
	interval  time.Duration
	body      func(context.Context)
	newTicker func(time.Duration) (<-chan time.Time, func())
	done      <-chan struct{}
	// wake is buffered from construction, so a Wake before Start causes one
	// extra run after the first.
	wake chan struct{}

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	// exited is closed by the goroutine when it returns; nil until Start. A
	// closed or nil channel makes a repeated Stop a no-op whatever its context.
	exited chan struct{}
}

// LoopOption customizes a Loop.
type LoopOption func(*Loop)

// WithTicker replaces the interval ticker. newTicker is called once, at
// Start, and returns the tick channel and the function that stops it.
func WithTicker(newTicker func(time.Duration) (<-chan time.Time, func())) LoopOption {
	return func(l *Loop) {
		if newTicker != nil {
			l.newTicker = newTicker
		}
	}
}

// WithDone adds a channel whose close ends the loop like a cancelled parent
// context does. The body's context is not derived from it.
func WithDone(done <-chan struct{}) LoopOption {
	return func(l *Loop) { l.done = done }
}

// NewLoop builds a loop named name (the subject of its deadline error) that
// runs body every interval.
func NewLoop(name string, interval time.Duration, body func(context.Context), opts ...LoopOption) *Loop {
	l := &Loop{
		name:     name,
		interval: interval,
		body:     body,
		newTicker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
		wake: make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Start runs the body once immediately on a goroutine whose context is
// derived from ctx, so either the parent's cancel or Stop ends it. Starting
// twice, or after Stop, is a no-op.
func (l *Loop) Start(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started || l.stopped {
		return
	}
	l.started = true
	loopCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	exited := make(chan struct{})
	l.exited = exited
	ticks, stopTicker := l.newTicker(l.interval)
	go l.run(loopCtx, ticks, stopTicker, exited)
}

func (l *Loop) run(ctx context.Context, ticks <-chan time.Time, stopTicker func(), exited chan<- struct{}) {
	defer close(exited)
	defer stopTicker()
	l.body(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.done:
			return
		case <-ticks:
		case <-l.wake:
		}
		if ctx.Err() != nil || l.doneClosed() {
			return
		}
		l.body(ctx)
	}
}

func (l *Loop) doneClosed() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// Wake asks for a run now. A Wake before Start is buffered and causes one
// extra run after the first; after the goroutine has ended it does nothing.
func (l *Loop) Wake() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Running reports whether the loop has been started and not yet stopped.
func (l *Loop) Running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.started && !l.stopped
}

// Stop cancels the loop and waits for the goroutine to return, or for ctx to
// end. A body in flight finishes before Stop returns nil. A deadline that
// passes first is an error wrapping ctx.Err(), and the body may then still be
// running. Once the goroutine has joined, a repeated Stop returns nil even on
// a context that has already ended; a loop never started has nothing to join.
func (l *Loop) Stop(ctx context.Context) error {
	l.mu.Lock()
	l.stopped = true
	cancel := l.cancel
	exited := l.exited
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if exited == nil {
		return nil
	}
	select {
	case <-exited:
		return nil
	default:
	}
	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s did not stop before the deadline: %w", l.name, ctx.Err())
	}
}
