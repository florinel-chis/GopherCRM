package aeo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// manualClock is a SchedulerClock whose timers fire when the test says so.
// Every After call hands its channel to the test through ticks, so a test can
// prove the loop armed a timer, fire it, and prove the loop re-armed.
type manualClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
	ticks chan chan time.Time
}

func newManualClock(now time.Time) *manualClock {
	return &manualClock{now: now, ticks: make(chan chan time.Time, 8)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *manualClock) After(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	c.waits = append(c.waits, d)
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.ticks <- ch
	return ch, func() bool { return true }
}

func (c *manualClock) requested() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, len(c.waits))
	copy(out, c.waits)
	return out
}

// armed waits for the loop to arm its next timer and returns the function that
// fires it.
func (c *manualClock) armed(t *testing.T) func() {
	t.Helper()
	select {
	case ch := <-c.ticks:
		return func() { ch <- c.Now() }
	case <-time.After(5 * time.Second):
		t.Fatal("the scheduler did not arm a timer")
		return nil
	}
}

// blockingStarter holds StartRun until released, so a test can observe the
// loop while a scheduled start is in progress.
type blockingStarter struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockingStarter) StartRun(ctx context.Context, trigger string, _ *uint) (*models.AEORun, error) {
	s.entered <- struct{}{}
	<-s.release
	run := &models.AEORun{Trigger: trigger, Status: RunStatusRunning}
	run.ID = 9
	return run, nil
}

func TestSchedulerFiresWhenDueAndRearms(t *testing.T) {
	starter := newFakeRunStarter()
	clock := newManualClock(time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC))
	scheduler := NewScheduler(starter, 6, WithSchedulerClock(clock))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	scheduler.Start(ctx) // a second Start is a no-op, not a second loop

	fire := clock.armed(t)
	require.Equal(t, []time.Duration{time.Hour}, clock.requested(), "the first wait runs to the configured hour")
	assert.Empty(t, starter.recorded(), "nothing runs before the hour")

	clock.advance(time.Hour)
	fire()
	select {
	case <-starter.called:
	case <-time.After(5 * time.Second):
		t.Fatal("the due tick did not start a run")
	}
	assert.Equal(t, []string{TriggerScheduled}, starter.recorded())

	// The loop re-arms for tomorrow's hour; a single Start means exactly one
	// re-arm is pending.
	clock.armed(t)
	require.Len(t, clock.requested(), 2)
	assert.Equal(t, 24*time.Hour, clock.requested()[1])
	select {
	case <-clock.ticks:
		t.Fatal("a second Start armed a second loop")
	default:
	}

	cancel()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	require.NoError(t, scheduler.Wait(waitCtx))
	select {
	case <-scheduler.Done():
	default:
		t.Fatal("Done is not closed after Wait returned")
	}
	assert.Equal(t, []string{TriggerScheduled}, starter.recorded(), "no run after cancellation")
}

// Wait joins the goroutine: while a scheduled start is still inside the service
// it reports the deadline instead of pretending the loop is gone, and returns
// nil once the start has actually finished.
func TestSchedulerWaitJoinsAStartInProgress(t *testing.T) {
	starter := &blockingStarter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	clock := newManualClock(time.Now())
	scheduler := NewScheduler(starter, 6, WithSchedulerClock(clock))

	ctx, cancel := context.WithCancel(context.Background())
	scheduler.Start(ctx)

	clock.armed(t)()
	select {
	case <-starter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the tick did not reach the starter")
	}

	cancel()
	short, shortCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer shortCancel()
	err := scheduler.Wait(short)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the loop is still inside StartRun")
	select {
	case <-scheduler.Done():
		t.Fatal("Done closed while the start was still in progress")
	default:
	}

	close(starter.release)
	long, longCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer longCancel()
	require.NoError(t, scheduler.Wait(long))
}

func TestSchedulerHandlesThatNeverArm(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var nilScheduler *Scheduler
	assert.NotPanics(t, func() { nilScheduler.Start(ctx) })
	assert.NoError(t, nilScheduler.Wait(ctx))

	never := NewScheduler(newFakeRunStarter(), 6)
	assert.NoError(t, never.Wait(ctx), "an unstarted scheduler has nothing to join")

	clock := newManualClock(time.Now())
	noStarter := NewScheduler(nil, 6, WithSchedulerClock(clock))
	noStarter.Start(ctx)
	assert.NoError(t, noStarter.Wait(ctx))
	assert.Empty(t, clock.requested(), "a scheduler without a starter arms no timer")

	assert.Nil(t, StartScheduler(ctx, nil, 6))
	handle := StartScheduler(ctx, newFakeRunStarter(), time.Now().Hour())
	require.NotNil(t, handle)
	cancel()
	joinCtx, joinCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer joinCancel()
	assert.NoError(t, handle.Wait(joinCtx))
}

// captureSchedulerLogs routes the package logger into a buffer and returns a
// reader of the JSON lines written.
func captureSchedulerLogs(t *testing.T) func() []map[string]any {
	t.Helper()
	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	sink := logrus.New()
	sink.SetLevel(logrus.DebugLevel)
	sink.SetFormatter(&logrus.JSONFormatter{})
	sink.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	previous := utils.Logger
	utils.Logger = sink
	t.Cleanup(func() { utils.Logger = previous })

	return func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		var entries []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			entry := map[string]any{}
			require.NoError(t, json.Unmarshal([]byte(line), &entry))
			entries = append(entries, entry)
		}
		return entries
	}
}

type writerFunc func(p []byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

// A due tick that finds the module unconfigured — no profile, no engine with a
// key, no active prompt — is an ordinary state, logged once at info level with
// no retry; so is a tick that lands during shutdown. Only an unexpected failure
// is an error-level line.
func TestTriggerScheduledRunIsQuietOnIncompleteConfiguration(t *testing.T) {
	quiet := []struct {
		name string
		err  error
	}{
		{"no providers", apperrors.ErrNoProvidersConfigured},
		{"no profile", apperrors.ErrProfileNotConfigured},
		{"no active prompts", fmt.Errorf("no active AEO prompts: %w", apperrors.ErrNotFound)},
		{"run in progress", apperrors.ErrRunInProgress},
		{"shutting down", ErrShuttingDown},
	}
	for _, tc := range quiet {
		t.Run(tc.name, func(t *testing.T) {
			read := captureSchedulerLogs(t)
			starter := newFakeRunStarter()
			starter.err = tc.err

			triggerScheduledRun(context.Background(), starter)

			entries := read()
			require.Len(t, entries, 1, "one line per tick, no retry loop")
			assert.Equal(t, "info", entries[0]["level"])
			assert.Contains(t, entries[0]["msg"], "AEO scheduled run skipped")
			assert.Equal(t, []string{TriggerScheduled}, starter.recorded())
		})
	}

	t.Run("an unexpected failure is an error", func(t *testing.T) {
		read := captureSchedulerLogs(t)
		starter := newFakeRunStarter()
		starter.err = errors.New("database unreachable")

		triggerScheduledRun(context.Background(), starter)

		entries := read()
		require.Len(t, entries, 1)
		assert.Equal(t, "error", entries[0]["level"])
	})

	t.Run("a started run is reported with its id", func(t *testing.T) {
		read := captureSchedulerLogs(t)

		triggerScheduledRun(context.Background(), newFakeRunStarter())

		entries := read()
		require.Len(t, entries, 1)
		assert.Equal(t, "info", entries[0]["level"])
		assert.Equal(t, "AEO scheduled run started", entries[0]["msg"])
		assert.EqualValues(t, 7, entries[0]["run_id"])
	})
}

func TestErrShuttingDownIsASentinel(t *testing.T) {
	wrapped := fmt.Errorf("start: %w", ErrShuttingDown)
	assert.ErrorIs(t, wrapped, ErrShuttingDown)
	assert.Contains(t, ErrShuttingDown.Error(), "shutting down")
}
