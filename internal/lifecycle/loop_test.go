package lifecycle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitBound is how long a test waits for something that must happen; it is a
// bound on a channel wait, never a sleep used as synchronisation.
const waitBound = 5 * time.Second

// manualTicker is a ticker the test fires by hand.
type manualTicker struct {
	ticks   chan time.Time
	created atomic.Int32
	stopped atomic.Int32
}

func newManualTicker() *manualTicker { return &manualTicker{ticks: make(chan time.Time)} }

func (m *manualTicker) new(time.Duration) (<-chan time.Time, func()) {
	m.created.Add(1)
	return m.ticks, func() { m.stopped.Add(1) }
}

// fire delivers one tick, failing the test when the loop does not take it.
func (m *manualTicker) fire(t *testing.T) {
	t.Helper()
	select {
	case m.ticks <- time.Now():
	case <-time.After(waitBound):
		t.Fatal("the loop did not take the tick")
	}
}

// runRecorder signals every body run on runs.
type runRecorder struct {
	runs  chan struct{}
	count atomic.Int32
}

func newRunRecorder() *runRecorder { return &runRecorder{runs: make(chan struct{}, 16)} }

func (r *runRecorder) body(context.Context) {
	r.count.Add(1)
	r.runs <- struct{}{}
}

func (r *runRecorder) awaitRun(t *testing.T) {
	t.Helper()
	select {
	case <-r.runs:
	case <-time.After(waitBound):
		t.Fatal("the body did not run")
	}
}

func stopWithin(t *testing.T, l *Loop) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitBound)
	defer cancel()
	require.NoError(t, l.Stop(ctx))
}

func TestLoop(t *testing.T) {
	t.Run("start runs the body once at once and is start-once", func(t *testing.T) {
		rec := newRunRecorder()
		ticker := newManualTicker()
		l := NewLoop("test loop", time.Hour, rec.body, WithTicker(ticker.new))
		assert.False(t, l.Running())
		l.Start(context.Background())
		l.Start(context.Background())
		assert.True(t, l.Running())
		rec.awaitRun(t)
		ticker.fire(t)
		rec.awaitRun(t)
		stopWithin(t, l)
		assert.Equal(t, int32(2), rec.count.Load(), "one run at Start and one per tick, never a second goroutine")
		assert.Equal(t, int32(1), ticker.created.Load(), "the second Start built no ticker")
		assert.Equal(t, int32(1), ticker.stopped.Load(), "the ticker is stopped when the goroutine returns")
		assert.False(t, l.Running())
	})

	t.Run("start after stop never runs the body", func(t *testing.T) {
		rec := newRunRecorder()
		ticker := newManualTicker()
		l := NewLoop("test loop", time.Hour, rec.body, WithTicker(ticker.new))
		require.NoError(t, l.Stop(context.Background()), "a loop never started has nothing to join")
		l.Start(context.Background())
		assert.False(t, l.Running())
		assert.Equal(t, int32(0), ticker.created.Load())
		assert.Equal(t, int32(0), rec.count.Load())
	})

	t.Run("a wake before start is buffered and causes one extra run", func(t *testing.T) {
		rec := newRunRecorder()
		l := NewLoop("test loop", time.Hour, rec.body, WithTicker(newManualTicker().new))
		l.Wake()
		l.Wake()
		l.Start(context.Background())
		rec.awaitRun(t)
		rec.awaitRun(t)
		stopWithin(t, l)
		assert.Equal(t, int32(2), rec.count.Load(), "the first run plus one buffered wake")
	})

	t.Run("the body in flight finishes before stop returns nil", func(t *testing.T) {
		entered := make(chan struct{})
		var finished atomic.Bool
		l := NewLoop("test loop", time.Hour, func(ctx context.Context) {
			close(entered)
			<-ctx.Done()
			finished.Store(true)
		}, WithTicker(newManualTicker().new))
		l.Start(context.Background())
		select {
		case <-entered:
		case <-time.After(waitBound):
			t.Fatal("the body did not start")
		}
		stopWithin(t, l)
		assert.True(t, finished.Load(), "Stop returned only after the body did")
	})

	t.Run("the deadline error wraps the context error and names the loop", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		l := NewLoop("test loop", time.Hour, func(context.Context) {
			close(entered)
			<-release
		}, WithTicker(newManualTicker().new))
		l.Start(context.Background())
		select {
		case <-entered:
		case <-time.After(waitBound):
			t.Fatal("the body did not start")
		}
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		err := l.Stop(ctx)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, "test loop did not stop before the deadline: context deadline exceeded", err.Error())

		close(release)
		stopWithin(t, l)
	})

	t.Run("a repeated stop on an ended context returns nil once joined", func(t *testing.T) {
		rec := newRunRecorder()
		l := NewLoop("test loop", time.Hour, rec.body, WithTicker(newManualTicker().new))
		l.Start(context.Background())
		rec.awaitRun(t)
		stopWithin(t, l)
		ended, cancel := context.WithCancel(context.Background())
		cancel()
		for i := 0; i < 100; i++ {
			require.NoError(t, l.Stop(ended))
		}
	})

	t.Run("a cancelled parent ends the loop", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		bodyCtx := make(chan context.Context, 1)
		l := NewLoop("test loop", time.Hour, func(ctx context.Context) { bodyCtx <- ctx }, WithTicker(newManualTicker().new))
		l.Start(parent)
		var ctx context.Context
		select {
		case ctx = <-bodyCtx:
		case <-time.After(waitBound):
			t.Fatal("the body did not run")
		}
		cancel()
		select {
		case <-ctx.Done():
		case <-time.After(waitBound):
			t.Fatal("the body context did not end with the parent")
		}
		stopWithin(t, l)
	})

	t.Run("the done channel ends the loop without cancelling the body", func(t *testing.T) {
		done := make(chan struct{})
		rec := newRunRecorder()
		ticker := newManualTicker()
		l := NewLoop("test loop", time.Hour, rec.body, WithTicker(ticker.new), WithDone(done))
		l.Start(context.Background())
		rec.awaitRun(t)
		close(done)
		// The goroutine leaves on done; a later tick finds nobody to take it.
		select {
		case ticker.ticks <- time.Now():
			// The select may still take one tick racing the close; the
			// re-check after it must then return without a run.
		case <-time.After(100 * time.Millisecond):
		}
		stopWithin(t, l)
		assert.Equal(t, int32(1), rec.count.Load(), "no run after done is closed")
		assert.Equal(t, int32(1), ticker.stopped.Load())
	})

	t.Run("concurrent start and stop leave no goroutine running", func(t *testing.T) {
		for i := 0; i < 50; i++ {
			var runs atomic.Int32
			l := NewLoop("test loop", time.Hour, func(context.Context) { runs.Add(1) }, WithTicker(newManualTicker().new))
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); l.Start(context.Background()) }()
			go func() { defer wg.Done(); _ = l.Stop(context.Background()) }()
			wg.Wait()
			stopWithin(t, l)
			before := runs.Load()
			l.Start(context.Background())
			assert.Equal(t, before, runs.Load())
			assert.False(t, l.Running())
		}
	})

	t.Run("a stop on an ended context still cancels the loop", func(t *testing.T) {
		bodyCtx := make(chan context.Context, 1)
		l := NewLoop("test loop", time.Hour, func(ctx context.Context) { bodyCtx <- ctx; <-ctx.Done() }, WithTicker(newManualTicker().new))
		l.Start(context.Background())
		var ctx context.Context
		select {
		case ctx = <-bodyCtx:
		case <-time.After(waitBound):
			t.Fatal("the body did not run")
		}
		ended, cancel := context.WithCancel(context.Background())
		cancel()
		err := l.Stop(ended)
		if err != nil {
			assert.True(t, errors.Is(err, context.Canceled))
		}
		select {
		case <-ctx.Done():
		case <-time.After(waitBound):
			t.Fatal("the body context was not cancelled")
		}
		stopWithin(t, l)
	})
}
