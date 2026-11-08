package service

// The lifecycle of the stale bulk operation sweep (#196) and the content of
// its log lines. The clock and the ticker are injected, so no root waits for
// real time to pass.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingSweepRepo records every sweep the runtime asks for. Only
// MarkStalePendingFailed is implemented; any other call panics on the nil
// embedded interface. A non-nil block makes each sweep wait for it whatever
// its context says, so a sweep can be held in flight.
type recordingSweepRepo struct {
	repository.BulkOperationRepository
	mu      sync.Mutex
	cutoffs []time.Time
	calls   chan time.Time
	block   chan struct{}
	result  int64
	err     error
}

func newRecordingSweepRepo() *recordingSweepRepo {
	return &recordingSweepRepo{calls: make(chan time.Time, 64)}
}

func (r *recordingSweepRepo) MarkStalePendingFailed(ctx context.Context, cutoff time.Time) (int64, error) {
	r.mu.Lock()
	r.cutoffs = append(r.cutoffs, cutoff)
	block := r.block
	r.mu.Unlock()
	r.calls <- cutoff
	if block != nil {
		<-block
	}
	return r.result, r.err
}

func (r *recordingSweepRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cutoffs)
}

func (r *recordingSweepRepo) next(t *testing.T) time.Time {
	t.Helper()
	select {
	case cutoff := <-r.calls:
		return cutoff
	case <-time.After(5 * time.Second):
		t.Fatal("the runtime never swept")
		return time.Time{}
	}
}

// manualTicker is the injected ticker: the root delivers each tick.
type manualTicker struct {
	ticks    chan time.Time
	interval atomic.Int64
	stopped  atomic.Bool
}

func newManualTicker() *manualTicker { return &manualTicker{ticks: make(chan time.Time)} }

func (m *manualTicker) factory(interval time.Duration) (<-chan time.Time, func()) {
	m.interval.Store(int64(interval))
	return m.ticks, func() { m.stopped.Store(true) }
}

// tick delivers one tick; it returns once the loop has received it.
func (m *manualTicker) tick(t *testing.T, at time.Time) {
	t.Helper()
	select {
	case m.ticks <- at:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop never took the tick")
	}
}

// refused reports that no goroutine receives a tick any more.
func (m *manualTicker) refused() bool {
	select {
	case m.ticks <- time.Now():
		return false
	case <-time.After(100 * time.Millisecond):
		return true
	}
}

// awaitNoExtraGoroutines polls from the test goroutine itself until the
// process holds no more goroutines than baseline.
func awaitNoExtraGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			t.Fatalf("a stopped runtime holds no goroutine: %d running, %d before Start", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func quietLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetLevel(logrus.PanicLevel)
	return logger
}

func TestBulkOperationRecoveryRuntimeLifecycle(t *testing.T) {
	t.Run("sweeps on Start and on every tick, then Stop joins and nothing runs after", func(t *testing.T) {
		baseline := runtime.NumGoroutine()
		repo := newRecordingSweepRepo()
		ticker := newManualTicker()
		clock := &automationClock{now: time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC)}
		rt := NewBulkOperationRecoveryRuntime(repo, quietLogger(),
			WithBulkRecoveryClock(clock.Now), WithBulkRecoveryTicker(ticker.factory))
		assert.Equal(t, BulkOperationSweepInterval, rt.Interval())

		rt.Start(context.Background())
		assert.Equal(t, clock.Now().Add(-BulkOperationStaleAfter), repo.next(t), "the boot sweep uses now - BulkOperationStaleAfter")
		assert.Equal(t, int64(BulkOperationSweepInterval), ticker.interval.Load(), "the ticker runs every 15 minutes")

		for i := 1; i <= 3; i++ {
			clock.Advance(BulkOperationSweepInterval)
			ticker.tick(t, clock.Now())
			assert.Equal(t, clock.Now().Add(-BulkOperationStaleAfter), repo.next(t), "tick %d sweeps with the current clock", i)
		}
		rt.Start(context.Background())
		assert.Equal(t, 4, repo.count(), "a second Start is a no-op")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, rt.Stop(ctx))
		assert.True(t, ticker.stopped.Load(), "the ticker is released")
		assert.True(t, ticker.refused(), "no goroutine waits for a tick after Stop")
		assert.Equal(t, 4, repo.count(), "nothing is swept after a nil Stop")

		ended, endedCancel := context.WithCancel(context.Background())
		endedCancel()
		assert.NoError(t, rt.Stop(ended), "a repeated Stop returns nil even on an ended context")
		rt.Start(context.Background())
		assert.Equal(t, 4, repo.count(), "Start after Stop does nothing")
		awaitNoExtraGoroutines(t, baseline)
	})

	t.Run("Stop waits for a sweep in flight under the caller's deadline", func(t *testing.T) {
		baseline := runtime.NumGoroutine()
		repo := newRecordingSweepRepo()
		repo.block = make(chan struct{})
		release := sync.OnceFunc(func() { close(repo.block) })
		t.Cleanup(release)
		ticker := newManualTicker()
		rt := NewBulkOperationRecoveryRuntime(repo, quietLogger(), WithBulkRecoveryTicker(ticker.factory))

		rt.Start(context.Background())
		repo.next(t)

		short, cancelShort := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancelShort()
		err := rt.Stop(short)
		require.Error(t, err, "Stop does not return nil while the sweep runs")
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.ErrorContains(t, err, "bulk operation recovery runtime did not stop")

		release()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, rt.Stop(ctx), "Stop returns nil once the sweep has ended")
		assert.True(t, ticker.refused())
		assert.Equal(t, 1, repo.count(), "no further sweep starts after the drain")
		awaitNoExtraGoroutines(t, baseline)
	})

	t.Run("Stop before Start returns nil and the runtime never starts", func(t *testing.T) {
		repo := newRecordingSweepRepo()
		rt := NewBulkOperationRecoveryRuntime(repo, quietLogger(), WithBulkRecoveryTicker(newManualTicker().factory))
		require.NoError(t, rt.Stop(context.Background()))
		rt.Start(context.Background())
		assert.Zero(t, repo.count())
	})

	t.Run("on a real database the boot sweep fails a stale row, and a stopped runtime writes nothing", func(t *testing.T) {
		env := newAutomationEnv(t, openAutomationSQLite(t))
		bulk := repository.NewBulkOperationRepository(env.db)
		seed := func() uint {
			row := &models.BulkOperation{UserID: env.admin.ID, ResourceType: "leads", Type: models.BulkAction,
				Status: models.StatusPending, TotalItems: 1}
			require.NoError(t, bulk.Create(context.Background(), row))
			return row.ID
		}
		status := func(id uint) models.BulkOperationStatus {
			var row models.BulkOperation
			require.NoError(t, env.db.First(&row, id).Error)
			return row.Status
		}
		first := seed()
		ticker := newManualTicker()
		rt := NewBulkOperationRecoveryRuntime(bulk, quietLogger(), WithBulkRecoveryTicker(ticker.factory),
			WithBulkRecoveryClock(func() time.Time { return time.Now().Add(BulkOperationStaleAfter + time.Minute) }))
		rt.Start(context.Background())
		deadline := time.Now().Add(5 * time.Second)
		for status(first) != models.StatusFailed {
			require.True(t, time.Now().Before(deadline), "the boot sweep fails the stale row")
			time.Sleep(10 * time.Millisecond)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, rt.Stop(ctx))
		second := seed()
		assert.True(t, ticker.refused())
		time.Sleep(50 * time.Millisecond)
		assert.Equal(t, models.StatusPending, status(second), "nothing is written after Stop returned nil")
	})
}

// logLines parses the JSON lines of a capture.
func logLines(t *testing.T, raw string) []map[string]interface{} {
	t.Helper()
	var lines []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		lines = append(lines, entry)
	}
	return lines
}

func assertOnlyFields(t *testing.T, entry map[string]interface{}, allowed ...string) {
	t.Helper()
	permitted := map[string]bool{"level": true, "msg": true, "time": true}
	for _, key := range allowed {
		permitted[key] = true
	}
	for key := range entry {
		assert.True(t, permitted[key], "unexpected field %q in %v", key, entry)
	}
}

func TestBulkOperationRecoveryLogsOmitPersonalData(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))
	lead := env.createLead(env.sales, models.LeadStatusNew)
	canaries := []string{
		automationCanaryFirstName, automationCanaryLastName, automationCanaryPhone, automationCanaryNotes,
		lead.Email, env.admin.Email, env.sales.Email, env.admin.LastName, env.sales.LastName,
	}
	assertNoPersonalData := func(t *testing.T, raw string) {
		t.Helper()
		for _, canary := range canaries {
			assert.NotContains(t, raw, canary)
		}
	}
	capture := func() (*logrus.Logger, *lockedBuffer) {
		logger, out := capturingLogger()
		logger.SetLevel(logrus.DebugLevel)
		return logger, out
	}

	t.Run("an outcome-write warning carries operation_id and the error only", func(t *testing.T) {
		logger, out := capture()
		svc := NewBulkStatusService(
			faultyOperationRepo{BulkOperationRepository: repository.NewBulkOperationRepository(env.db), failUpdate: true},
			repository.NewBulkStatusRepository(env.db), env.txManager, logger, WithBulkAutomationEvents(env.repo))
		_, err := svc.BulkSetLeadStatus(context.Background(), env.sales.ID, models.RoleSales, []uint{lead.ID, 999999}, models.LeadStatusContacted)
		require.Error(t, err)

		lines := logLines(t, out.String())
		require.Len(t, lines, 1, "exactly the outcome warning")
		assert.Equal(t, "Failed to record bulk status operation outcome", lines[0]["msg"])
		assert.Equal(t, "warning", lines[0]["level"])
		assert.Contains(t, lines[0], "operation_id")
		assert.Contains(t, lines[0], "error")
		assertOnlyFields(t, lines[0], "operation_id", "error")
		assertNoPersonalData(t, out.String())
	})

	t.Run("a sweep line carries the count only", func(t *testing.T) {
		logger, out := capture()
		row := &models.BulkOperation{UserID: env.sales.ID, ResourceType: "leads", Type: models.BulkAction,
			Status: models.StatusPending, TotalItems: 1}
		require.NoError(t, repository.NewBulkOperationRepository(env.db).Create(context.Background(), row))
		rt := NewBulkOperationRecoveryRuntime(repository.NewBulkOperationRepository(env.db), logger,
			WithBulkRecoveryClock(func() time.Time { return time.Now().Add(BulkOperationStaleAfter + time.Minute) }))
		swept, err := rt.Sweep(context.Background())
		require.NoError(t, err)
		require.Positive(t, swept)
		_, err = rt.Sweep(context.Background())
		require.NoError(t, err)

		lines := logLines(t, out.String())
		require.Len(t, lines, 2)
		assert.Equal(t, "info", lines[0]["level"])
		assert.EqualValues(t, swept, lines[0]["failed"])
		assert.EqualValues(t, 0, lines[1]["failed"])
		for _, line := range lines {
			assertOnlyFields(t, line, "failed")
		}
		assertNoPersonalData(t, out.String())
	})

	t.Run("a failed sweep logs the count and the error through utils.LogError", func(t *testing.T) {
		logger, out := capture()
		repo := newRecordingSweepRepo()
		repo.err = fmt.Errorf("sweep refused for '%s': %w", env.sales.Email, errors.New("driver failure"))
		rt := NewBulkOperationRecoveryRuntime(repo, logger)
		_, err := rt.Sweep(context.Background())
		require.Error(t, err)

		lines := logLines(t, out.String())
		require.Len(t, lines, 1)
		assert.Equal(t, "warning", lines[0]["level"])
		assert.Equal(t, "bulk operation recovery sweep failed", lines[0]["msg"])
		assert.Contains(t, lines[0], "error")
		assertOnlyFields(t, lines[0], "failed", "error")
		assertNoPersonalData(t, out.String())
	})
}
