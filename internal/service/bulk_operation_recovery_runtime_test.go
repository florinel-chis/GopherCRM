package service

// The lifecycle of the stale bulk operation sweep (#196) and the content of
// its log lines. The clock and the ticker are injected, so no root waits for
// real time to pass.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/glebarez/sqlite"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
		_, err := svc.BulkSetLeadStatus(context.Background(), env.sales.ID, models.PermissionScopeOwn, []uint{lead.ID, 999999}, models.LeadStatusContacted)
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

// recordingPruneRepo records the single sign-on attempt prunes a sweep asks
// for (#453). Only PruneCreatedBefore is implemented. results are returned in
// order, then 0; err is returned by every call.
type recordingPruneRepo struct {
	repository.OIDCLoginAttemptRepository
	mu      sync.Mutex
	befores []time.Time
	limits  []int
	results []int64
	err     error
}

func (r *recordingPruneRepo) PruneCreatedBefore(_ context.Context, before time.Time, limit int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.befores = append(r.befores, before)
	r.limits = append(r.limits, limit)
	var deleted int64
	if len(r.results) > 0 {
		deleted, r.results = r.results[0], r.results[1:]
	}
	return deleted, r.err
}

func (r *recordingPruneRepo) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.befores)
}

func TestBulkOperationRecoveryOIDCAttemptPruning(t *testing.T) {
	now := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)

	t.Run("every sweep prunes attempts older than a day, chunk by chunk, and logs the count", func(t *testing.T) {
		logger, out := capturingLogger()
		logger.SetLevel(logrus.DebugLevel)
		bulk := newRecordingSweepRepo()
		bulk.result = 2
		prune := &recordingPruneRepo{results: []int64{500, 3}}
		rt := NewBulkOperationRecoveryRuntime(bulk, logger, WithBulkRecoveryClock(func() time.Time { return now }),
			WithOIDCAttemptPruning(prune))
		failed, err := rt.Sweep(context.Background())
		require.NoError(t, err)
		assert.EqualValues(t, 2, failed)
		require.Equal(t, 2, prune.calls(), "a full chunk is followed by another")
		for i := range prune.befores {
			assert.Equal(t, now.Add(-24*time.Hour), prune.befores[i], "the cutoff is a day before the runtime clock")
			assert.Equal(t, 500, prune.limits[i])
		}
		assert.Equal(t, 24*time.Hour, OIDCLoginAttemptRetention)

		var pruned map[string]interface{}
		for _, line := range logLines(t, out.String()) {
			assert.NotEqual(t, "oidc_sign_in", line["component"], "the sweep does not log as the sign-in")
			if line["msg"] == "pruned single sign-on attempts" {
				pruned = line
			}
		}
		require.NotNil(t, pruned, "a non-zero prune is logged")
		assert.Equal(t, "info", pruned["level"])
		assert.EqualValues(t, 503, pruned["pruned"])
		assertOnlyFields(t, pruned, "pruned")
	})

	t.Run("a prune error leaves the sweep result unchanged", func(t *testing.T) {
		logger, out := capturingLogger()
		bulk := newRecordingSweepRepo()
		bulk.result = 4
		prune := &recordingPruneRepo{err: errors.New("prune refused")}
		rt := NewBulkOperationRecoveryRuntime(bulk, logger, WithBulkRecoveryClock(func() time.Time { return now }),
			WithOIDCAttemptPruning(prune))
		failed, err := rt.Sweep(context.Background())
		require.NoError(t, err, "the bulk sweep's error is not the prune's")
		assert.EqualValues(t, 4, failed)
		assert.Equal(t, 1, prune.calls())
		assert.Contains(t, out.String(), "single sign-on attempt prune failed")

		bulk.err = errors.New("bulk refused")
		_, err = rt.Sweep(context.Background())
		require.Error(t, err)
		assert.Equal(t, "bulk refused", err.Error(), "a bulk failure stays the bulk failure")
		assert.Equal(t, 2, prune.calls(), "the prune runs whatever the bulk result")
	})

	t.Run("without the option there is no prune call", func(t *testing.T) {
		bulk := newRecordingSweepRepo()
		bulk.result = 1
		prune := &recordingPruneRepo{results: []int64{7}}
		rt := NewBulkOperationRecoveryRuntime(bulk, quietLogger(), WithBulkRecoveryClock(func() time.Time { return now }))
		failed, err := rt.Sweep(context.Background())
		require.NoError(t, err)
		assert.EqualValues(t, 1, failed)
		assert.Zero(t, prune.calls())
		assert.Nil(t, rt.oidcAttempts)
	})

	t.Run("stop cancels a prune waiting for the database", func(t *testing.T) {
		held := openHeldPruneDatabase(t, &models.OIDCLoginAttempt{})
		logger, out := capturingLogger()
		logger.SetLevel(logrus.DebugLevel)
		rt := NewBulkOperationRecoveryRuntime(newRecordingSweepRepo(), logger,
			WithBulkRecoveryClock(func() time.Time { return now }),
			WithBulkRecoveryTicker(newManualTicker().factory),
			WithOIDCAttemptPruning(repository.NewOIDCLoginAttemptRepository(held.db)))
		stopCancelsWaitingPrune(t, rt, held, out, "single sign-on attempt prune interrupted")
	})
}

// recordingAuditPruneRepo records the prune calls of the sign-in audit log.
type recordingAuditPruneRepo struct {
	repository.AuthAuditRepository
	inner recordingPruneRepo
}

func (r *recordingAuditPruneRepo) PruneCreatedBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	return r.inner.PruneCreatedBefore(ctx, before, limit)
}

// heldPruneDatabase is a file-backed SQLite database with model migrated and
// a pool of one connection, which the root holds until release (#485). A
// prune started meanwhile waits in the pool for that connection.
type heldPruneDatabase struct {
	db      *gorm.DB
	sqlDB   *sql.DB
	waits   int64
	release func()
}

func openHeldPruneDatabase(t *testing.T, model interface{}) *heldPruneDatabase {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "prune.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), automationGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(model))

	held, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	var once sync.Once
	return &heldPruneDatabase{
		db:      db,
		sqlDB:   sqlDB,
		waits:   sqlDB.Stats().WaitCount,
		release: func() { once.Do(func() { _ = held.Close() }) },
	}
}

// awaitPruneWaiting returns once a statement waits for the held connection.
// The bulk step is a fake, so the waiter is the prune.
func (h *heldPruneDatabase) awaitPruneWaiting(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return h.sqlDB.Stats().WaitCount > h.waits },
		5*time.Second, 5*time.Millisecond, "the prune never waited for the held connection")
}

// stopCancelsWaitingPrune starts rt while the connection is held, waits until
// the prune waits for it, and asserts Stop cancels the prune under a 5 s
// deadline and logs interrupted at info, with no warning at all.
func stopCancelsWaitingPrune(t *testing.T, rt *BulkOperationRecoveryRuntime, held *heldPruneDatabase, out *lockedBuffer, interrupted string) {
	t.Helper()
	// Registered after the database's own cleanup, so it runs first: the
	// connection goes back and the goroutine joins before the database and
	// the logger are torn down, also when an assertion below failed.
	t.Cleanup(func() {
		held.release()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	rt.Start(context.Background())
	held.awaitPruneWaiting(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, rt.Stop(ctx), "Stop cancels the prune waiting for the database")

	var found int
	for _, line := range logLines(t, out.String()) {
		assert.NotEqual(t, "warning", line["level"], "no warning: %v", line)
		if line["msg"] == interrupted {
			found++
			assert.Equal(t, "info", line["level"])
		}
	}
	assert.Equal(t, 1, found, "one %q line", interrupted)
}

func TestBulkOperationRecoveryAuthAuditPruning(t *testing.T) {
	now := time.Date(2026, time.November, 24, 12, 0, 0, 0, time.UTC)

	t.Run("every sweep prunes audit rows older than the retention, chunk by chunk, and logs the count", func(t *testing.T) {
		logger, out := capturingLogger()
		logger.SetLevel(logrus.DebugLevel)
		bulk := newRecordingSweepRepo()
		bulk.result = 2
		audit := &recordingAuditPruneRepo{inner: recordingPruneRepo{results: []int64{500, 4}}}
		oidc := &recordingPruneRepo{}
		rt := NewBulkOperationRecoveryRuntime(bulk, logger, WithBulkRecoveryClock(func() time.Time { return now }),
			WithOIDCAttemptPruning(oidc), WithAuthAuditPruning(audit, 30))
		failed, err := rt.Sweep(context.Background())
		require.NoError(t, err)
		assert.EqualValues(t, 2, failed)
		assert.Equal(t, 1, oidc.calls(), "the attempt prune still runs")
		require.Equal(t, 2, audit.inner.calls(), "a full chunk is followed by another")
		for i := range audit.inner.befores {
			assert.Equal(t, now.Add(-30*24*time.Hour), audit.inner.befores[i], "the cutoff is the retention before the runtime clock")
			assert.Equal(t, 500, audit.inner.limits[i])
		}

		var pruned map[string]interface{}
		for _, line := range logLines(t, out.String()) {
			if line["msg"] == "pruned sign-in audit rows" {
				pruned = line
			}
		}
		require.NotNil(t, pruned, "a non-zero prune is logged")
		assert.Equal(t, "info", pruned["level"])
		assert.EqualValues(t, 504, pruned["pruned"])
		assertOnlyFields(t, pruned, "pruned")
	})

	t.Run("a non-positive retention prunes at the 90-day default", func(t *testing.T) {
		bulk := newRecordingSweepRepo()
		audit := &recordingAuditPruneRepo{}
		rt := NewBulkOperationRecoveryRuntime(bulk, quietLogger(), WithBulkRecoveryClock(func() time.Time { return now }),
			WithAuthAuditPruning(audit, 0))
		_, err := rt.Sweep(context.Background())
		require.NoError(t, err)
		require.Equal(t, 1, audit.inner.calls())
		assert.Equal(t, now.Add(-90*24*time.Hour), audit.inner.befores[0])
	})

	t.Run("a prune error leaves the sweep result unchanged", func(t *testing.T) {
		logger, out := capturingLogger()
		bulk := newRecordingSweepRepo()
		bulk.result = 4
		audit := &recordingAuditPruneRepo{inner: recordingPruneRepo{err: errors.New("prune refused")}}
		rt := NewBulkOperationRecoveryRuntime(bulk, logger, WithBulkRecoveryClock(func() time.Time { return now }),
			WithAuthAuditPruning(audit, 90))
		failed, err := rt.Sweep(context.Background())
		require.NoError(t, err, "the bulk sweep's error is not the prune's")
		assert.EqualValues(t, 4, failed)
		assert.Contains(t, out.String(), "sign-in audit prune failed")

		bulk.err = errors.New("bulk refused")
		_, err = rt.Sweep(context.Background())
		require.Error(t, err)
		assert.Equal(t, "bulk refused", err.Error())
		assert.Equal(t, 2, audit.inner.calls(), "the prune runs whatever the bulk result")
	})

	t.Run("without the option there is no prune call", func(t *testing.T) {
		bulk := newRecordingSweepRepo()
		audit := &recordingAuditPruneRepo{}
		rt := NewBulkOperationRecoveryRuntime(bulk, quietLogger(), WithBulkRecoveryClock(func() time.Time { return now }),
			WithOIDCAttemptPruning(&recordingPruneRepo{}))
		_, err := rt.Sweep(context.Background())
		require.NoError(t, err)
		assert.Zero(t, audit.inner.calls())
	})
	t.Run("stop cancels a prune waiting for the database", func(t *testing.T) {
		held := openHeldPruneDatabase(t, &models.AuthAuditLog{})
		logger, out := capturingLogger()
		logger.SetLevel(logrus.DebugLevel)
		rt := NewBulkOperationRecoveryRuntime(newRecordingSweepRepo(), logger,
			WithBulkRecoveryClock(func() time.Time { return now }),
			WithBulkRecoveryTicker(newManualTicker().factory),
			WithAuthAuditPruning(repository.NewAuthAuditRepository(held.db), 90))
		stopCancelsWaitingPrune(t, rt, held, out, "sign-in audit prune interrupted")
	})
}
