package aeo

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
)

// fakeRunStarter records scheduler-initiated runs and can be scripted to fail
// or panic.
type fakeRunStarter struct {
	mu       sync.Mutex
	triggers []string
	err      error
	panics   bool
	called   chan struct{}
}

func newFakeRunStarter() *fakeRunStarter {
	return &fakeRunStarter{called: make(chan struct{}, 8)}
}

func (s *fakeRunStarter) StartRun(_ context.Context, trigger string, triggeredByID *uint) (*models.AEORun, error) {
	s.mu.Lock()
	s.triggers = append(s.triggers, trigger)
	err := s.err
	shouldPanic := s.panics
	s.mu.Unlock()

	select {
	case s.called <- struct{}{}:
	default:
	}

	if shouldPanic {
		panic("service exploded")
	}
	if err != nil {
		return nil, err
	}
	if triggeredByID != nil {
		return nil, errors.New("scheduled runs must not carry a user id")
	}

	run := &models.AEORun{Trigger: trigger, Status: RunStatusRunning}
	run.ID = 7
	return run, nil
}

func (s *fakeRunStarter) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.triggers))
	copy(out, s.triggers)
	return out
}

func TestNextRunAt(t *testing.T) {
	// A fixed non-DST location keeps the arithmetic assertions readable.
	utc := time.UTC

	tests := []struct {
		name string
		now  time.Time
		hour int
		want time.Time
	}{
		{
			name: "before the hour, same day",
			now:  time.Date(2026, 8, 11, 3, 30, 0, 0, utc),
			hour: 6,
			want: time.Date(2026, 8, 11, 6, 0, 0, 0, utc),
		},
		{
			name: "after the hour, next day",
			now:  time.Date(2026, 8, 11, 6, 30, 0, 0, utc),
			hour: 6,
			want: time.Date(2026, 8, 12, 6, 0, 0, 0, utc),
		},
		{
			name: "exactly on the hour rolls to the next day",
			now:  time.Date(2026, 8, 11, 6, 0, 0, 0, utc),
			hour: 6,
			want: time.Date(2026, 8, 12, 6, 0, 0, 0, utc),
		},
		{
			name: "one nanosecond before the hour still fires today",
			now:  time.Date(2026, 8, 11, 5, 59, 59, 999999999, utc),
			hour: 6,
			want: time.Date(2026, 8, 11, 6, 0, 0, 0, utc),
		},
		{
			name: "midnight schedule",
			now:  time.Date(2026, 8, 11, 0, 0, 1, 0, utc),
			hour: 0,
			want: time.Date(2026, 8, 12, 0, 0, 0, 0, utc),
		},
		{
			name: "last hour of the day",
			now:  time.Date(2026, 8, 11, 22, 0, 0, 0, utc),
			hour: 23,
			want: time.Date(2026, 8, 11, 23, 0, 0, 0, utc),
		},
		{
			name: "month rollover",
			now:  time.Date(2026, 8, 31, 23, 30, 0, 0, utc),
			hour: 6,
			want: time.Date(2026, 9, 1, 6, 0, 0, 0, utc),
		},
		{
			name: "year rollover",
			now:  time.Date(2026, 12, 31, 23, 30, 0, 0, utc),
			hour: 6,
			want: time.Date(2027, 1, 1, 6, 0, 0, 0, utc),
		},
		{
			name: "leap day",
			now:  time.Date(2028, 2, 28, 23, 30, 0, 0, utc),
			hour: 6,
			want: time.Date(2028, 2, 29, 6, 0, 0, 0, utc),
		},
		{
			name: "negative hour is clamped to midnight",
			now:  time.Date(2026, 8, 11, 3, 0, 0, 0, utc),
			hour: -5,
			want: time.Date(2026, 8, 12, 0, 0, 0, 0, utc),
		},
		{
			name: "hour above 23 is clamped to 23",
			now:  time.Date(2026, 8, 11, 3, 0, 0, 0, utc),
			hour: 99,
			want: time.Date(2026, 8, 11, 23, 0, 0, 0, utc),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NextRunAt(tc.now, tc.hour)
			assert.True(t, got.Equal(tc.want), "want %s, got %s", tc.want, got)
			assert.True(t, got.After(tc.now), "the next run must be strictly in the future")
		})
	}
}

func TestNextRunAtAcrossDSTTransitions(t *testing.T) {
	// Europe/Bucharest springs forward at 03:00 on the last Sunday in March and
	// falls back at 04:00 on the last Sunday in October. Building the next
	// occurrence with wall-clock fields (rather than adding 24 hours) is what
	// keeps a 06:00 schedule at 06:00 across both.
	location, err := time.LoadLocation("Europe/Bucharest")
	require.NoError(t, err)

	tests := []struct {
		name string
		now  time.Time
	}{
		{name: "day before spring forward", now: time.Date(2026, 3, 28, 12, 0, 0, 0, location)},
		{name: "day before fall back", now: time.Date(2026, 10, 24, 12, 0, 0, 0, location)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next := NextRunAt(tc.now, 6)
			assert.Equal(t, 6, next.Hour(), "the run stays at the configured wall-clock hour")
			assert.Equal(t, 0, next.Minute())
			assert.Equal(t, tc.now.Day()+1, next.Day())
			assert.True(t, next.After(tc.now))
		})
	}
}

func TestTriggerScheduledRun(t *testing.T) {
	tests := []struct {
		name    string
		starter func() *fakeRunStarter
	}{
		{
			name:    "successful run",
			starter: newFakeRunStarter,
		},
		{
			name: "overlap is swallowed",
			starter: func() *fakeRunStarter {
				s := newFakeRunStarter()
				s.err = apperrors.ErrRunInProgress
				return s
			},
		},
		{
			name: "an arbitrary failure is swallowed",
			starter: func() *fakeRunStarter {
				s := newFakeRunStarter()
				s.err = errors.New("database unreachable")
				return s
			},
		},
		{
			name: "a panic inside the service is contained",
			starter: func() *fakeRunStarter {
				s := newFakeRunStarter()
				s.panics = true
				return s
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			starter := tc.starter()
			assert.NotPanics(t, func() {
				triggerScheduledRun(context.Background(), starter)
			})
			assert.Equal(t, []string{TriggerScheduled}, starter.recorded())
		})
	}
}

func TestStartSchedulerFiresAtTheConfiguredHourAndStops(t *testing.T) {
	starter := newFakeRunStarter()

	// Schedule for the current hour when it has not elapsed yet, otherwise the
	// test would wait a whole day. Using the hour of "one hour from now" would
	// still be a long wait, so drive the loop through NextRunAt indirectly by
	// asking for the hour that is about to start.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartScheduler(ctx, starter, time.Now().Hour())

	// The next occurrence of the current hour is tomorrow, so nothing should
	// fire; the scheduler must nevertheless shut down promptly on cancel.
	select {
	case <-starter.called:
		t.Fatal("the scheduler fired before its scheduled hour")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	// Give the goroutine a moment to observe the cancellation; a leaked
	// goroutine would keep the test binary's timer alive.
	time.Sleep(20 * time.Millisecond)
	assert.Empty(t, starter.recorded())
}

func TestStartSchedulerIgnoresANilStarter(t *testing.T) {
	assert.NotPanics(t, func() {
		StartScheduler(context.Background(), nil, 6)
	})
}

func TestSchedulerLoopStopsOnContextCancellation(t *testing.T) {
	starter := newFakeRunStarter()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		schedulerLoop(ctx, starter, 6)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the scheduler goroutine did not stop when its context was cancelled")
	}
}

func TestAEOSchedulerName(t *testing.T) {
	assert.Equal(t, "aeo-scheduler", AEOSchedulerName)
}
