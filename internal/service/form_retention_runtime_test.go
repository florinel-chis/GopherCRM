package service

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const formRetentionRuntimeWait = 10 * time.Second

// stubFormSweeper reports each call on a channel and answers with the configured
// error.
type stubFormSweeper struct {
	calls chan struct{}
	mu    sync.Mutex
	err   error
}

func newStubFormSweeper() *stubFormSweeper {
	return &stubFormSweeper{calls: make(chan struct{}, 64)}
}

func (s *stubFormSweeper) Sweep(context.Context) (FormPurgeResult, error) {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	s.calls <- struct{}{}
	return FormPurgeResult{}, err
}

func (s *stubFormSweeper) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *stubFormSweeper) await(t *testing.T) {
	t.Helper()
	select {
	case <-s.calls:
	case <-time.After(formRetentionRuntimeWait):
		t.Fatal("the runtime did not sweep")
	}
}

func (s *stubFormSweeper) assertQuiet(t *testing.T) {
	t.Helper()
	select {
	case <-s.calls:
		t.Fatal("the runtime swept without a reason")
	case <-time.After(100 * time.Millisecond):
	}
}

func stopFormRetention(t *testing.T, runtime *FormRetentionRuntime) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), formRetentionRuntimeWait)
	defer cancel()
	return runtime.Stop(ctx)
}

func TestFormRetentionRuntimeLifecycle(t *testing.T) {
	assert.Equal(t, 24*time.Hour, FormRetentionSweepInterval)
	_, hook := capturePrivacyLogs(t)

	t.Run("start sweeps once, wake sweeps again, starting twice is a no-op", func(t *testing.T) {
		sweeper := newStubFormSweeper()
		runtime := NewFormRetentionRuntime(sweeper, time.Hour)
		assert.Equal(t, time.Hour, runtime.Interval())

		runtime.Start(context.Background())
		runtime.Start(context.Background())
		sweeper.await(t)
		sweeper.assertQuiet(t)

		runtime.Wake()
		sweeper.await(t)
		sweeper.assertQuiet(t)

		require.NoError(t, stopFormRetention(t, runtime))
	})

	t.Run("the interval triggers sweeps", func(t *testing.T) {
		sweeper := newStubFormSweeper()
		runtime := NewFormRetentionRuntime(sweeper, 10*time.Millisecond)
		runtime.Start(context.Background())
		for i := 0; i < 3; i++ {
			sweeper.await(t)
		}
		require.NoError(t, stopFormRetention(t, runtime))
	})

	t.Run("a non-positive interval means the daily one", func(t *testing.T) {
		assert.Equal(t, FormRetentionSweepInterval, NewFormRetentionRuntime(newStubFormSweeper(), 0).Interval())
	})

	t.Run("a sweep error is logged and does not stop later ticks", func(t *testing.T) {
		hook.Reset()
		sweeper := newStubFormSweeper()
		sweeper.fail(errors.New("injected sweep failure"))
		runtime := NewFormRetentionRuntime(sweeper, time.Hour)
		runtime.Start(context.Background())
		sweeper.await(t)

		runtime.Wake()
		sweeper.await(t)

		// The stub reports the call before Sweep returns, so the line for the
		// second failure is awaited before Stop can turn it into an interruption.
		failures := func() int {
			count := 0
			for _, entry := range hook.AllEntries() {
				if entry.Message == "form retention sweep failed" {
					count++
					assert.Contains(t, entry.Data["error"].(error).Error(), "injected sweep failure")
				}
			}
			return count
		}
		deadline := time.Now().Add(formRetentionRuntimeWait)
		for failures() < 2 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		assert.Equal(t, 2, failures())
		require.NoError(t, stopFormRetention(t, runtime))
	})

	t.Run("stop joins, repeats on an ended context, and ends the runtime", func(t *testing.T) {
		sweeper := newStubFormSweeper()
		runtime := NewFormRetentionRuntime(sweeper, time.Hour)
		runtime.Start(context.Background())
		sweeper.await(t)
		require.NoError(t, stopFormRetention(t, runtime))

		ended, cancel := context.WithCancel(context.Background())
		cancel()
		require.NoError(t, runtime.Stop(ended), "a repeated stop returns nil even on an ended context")

		runtime.Start(context.Background())
		runtime.Wake()
		sweeper.assertQuiet(t)
	})

	t.Run("stop before start returns nil and start then does nothing", func(t *testing.T) {
		sweeper := newStubFormSweeper()
		runtime := NewFormRetentionRuntime(sweeper, time.Hour)
		ended, cancel := context.WithCancel(context.Background())
		cancel()
		require.NoError(t, runtime.Stop(ended))
		runtime.Start(context.Background())
		sweeper.assertQuiet(t)
	})

	t.Run("the parent context also ends the goroutine", func(t *testing.T) {
		sweeper := newStubFormSweeper()
		runtime := NewFormRetentionRuntime(sweeper, time.Hour)
		parent, cancel := context.WithCancel(context.Background())
		runtime.Start(parent)
		sweeper.await(t)
		cancel()
		require.NoError(t, stopFormRetention(t, runtime))
	})
}

// openFormRetentionFileSQLite is a file, not ":memory:": cancelling a transaction
// makes database/sql discard its connection, and a replacement connection to
// ":memory:" would be a fresh, empty database.
func openFormRetentionFileSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "form-retention.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateFormTestSchema(t, db)
	return db
}

func TestFormRetentionRuntimeStopDuringChunk(t *testing.T) {
	_, _ = capturePrivacyLogs(t)
	db := openFormRetentionFileSQLite(t)
	hooks := installFormPurgeDBHooks(t, db)
	form := seedFormPurgeForm(t, db)
	seedFormPurgeRows(t, db, form.ID, models.FormSubmissionSpam, 1001, oldFormPurgeTime())

	// The first chunk commits; the second parks before its delete, with its
	// transaction open. A third would be needed for the last row.
	var mu sync.Mutex
	deletes := 0
	secondChunk := newFormPurgeGate()
	hooks.onDelete(func(table string) error {
		if table != "form_submissions" {
			return nil
		}
		mu.Lock()
		deletes++
		current := deletes
		mu.Unlock()
		if current == 2 {
			secondChunk.pause()
		}
		return nil
	})

	runtime := NewFormRetentionRuntime(newFormPurger(db), time.Hour)
	runtime.Start(context.Background())
	secondChunk.awaitReached(t)

	// The chunk is still in flight, so a drain whose deadline passes first says
	// so instead of returning nil.
	short, cancelShort := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelShort()
	err := runtime.Stop(short)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// Once the chunk can move, it finds its transaction cancelled and rolls back,
	// and Stop joins the goroutine.
	close(secondChunk.release)
	require.NoError(t, stopFormRetention(t, runtime))

	assert.EqualValues(t, 501, allFormSubmissions(t, db),
		"the first chunk stays removed and the chunk in flight rolled back")
	mu.Lock()
	assert.Equal(t, 2, deletes, "no further chunk starts")
	mu.Unlock()

	// After a Stop that returned nil, no row changes.
	runtime.Wake()
	time.Sleep(200 * time.Millisecond)
	assert.EqualValues(t, 501, allFormSubmissions(t, db))
	mu.Lock()
	assert.Equal(t, 2, deletes)
	mu.Unlock()

	ended, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, runtime.Stop(ended), "a repeated stop returns nil")
}
