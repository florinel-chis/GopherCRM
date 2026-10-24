package main

// Startup and shutdown roots for issue #111, exercised through the real
// wiring: setupRouter builds every repository, service and handler over a
// SQLite file exactly as main does, the scheduler is armed by setupDependencies
// and the stop handle it returns is what main drains before closing the
// database. The only substitutions are the scheduler's clock — so a due tick
// arrives when the test fires it rather than at the configured hour — and a
// loopback OpenAI-compatible stub authorized through the ordinary egress policy,
// so no request leaves the machine and no real credential exists.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// aeoBootTimeout bounds every wait in these roots.
const aeoBootTimeout = 15 * time.Second

const aeoBootSecret = "aeo-runtime-startup-test-secret-32-bytes!"

func init() {
	gin.SetMode(gin.TestMode)
}

// lockedLogBuffer captures the application logger so a root can assert on the
// startup and shutdown lines main's operator would read.
type lockedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedLogBuffer) count(substring string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), substring)
}

// manualClock is the scheduler clock these roots install: every timer the
// scheduler arms is handed to the test, which fires it on demand.
type manualClock struct {
	mu    sync.Mutex
	now   time.Time
	calls int
	ticks chan chan time.Time
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Now(), ticks: make(chan chan time.Time, 8)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) After(time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.ticks <- ch
	return ch, func() bool { return true }
}

func (c *manualClock) afterCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// armed waits for the scheduler to arm a timer and returns the function that
// delivers the due tick.
func (c *manualClock) armed(t *testing.T) func() {
	t.Helper()
	select {
	case ch := <-c.ticks:
		return func() { ch <- c.Now() }
	case <-time.After(aeoBootTimeout):
		t.Fatal("the scheduler did not arm a timer")
		return nil
	}
}

// stubEngine is a loopback OpenAI-compatible server. It records the
// Authorization header of every request — which is how a root proves the key
// came from the database — and, when blocking, holds the request until the
// client cancels it, the way a provider call looks from a run that is being
// shut down.
type stubEngine struct {
	server    *httptest.Server
	block     atomic.Bool
	mu        sync.Mutex
	auths     []string
	entered   chan struct{}
	cleanup   chan struct{}
	cancelled atomic.Int32
}

func newStubEngine(t *testing.T) *stubEngine {
	t.Helper()
	stub := &stubEngine{entered: make(chan struct{}, 16), cleanup: make(chan struct{})}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HTTP/1 observes a peer disconnect after the request body reaches EOF.
		// Consume it before waiting for cancellation, and keep failed-test
		// cleanup independent of the client ever cancelling its request.
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		stub.mu.Lock()
		stub.auths = append(stub.auths, r.Header.Get("Authorization"))
		stub.mu.Unlock()
		stub.entered <- struct{}{}
		if stub.block.Load() {
			select {
			case <-r.Context().Done():
				stub.cancelled.Add(1)
			case <-stub.cleanup:
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gemini-test",`+
			`"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant",`+
			`"content":"Acme is the leading CRM for small teams."}}]}`)
	}))
	t.Cleanup(func() { close(stub.cleanup); stub.server.Close() })
	return stub
}

func (s *stubEngine) awaitRequest(t *testing.T) {
	t.Helper()
	select {
	case <-s.entered:
	case <-time.After(aeoBootTimeout):
		t.Fatal("no provider request reached the stub engine")
	}
}

func (s *stubEngine) authorizations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auths...)
}

// aeoBoot is one booted process: configuration, database, router and the drain
// handle main receives.
type aeoBoot struct {
	cfg     *config.Config
	clock   *manualClock
	logs    *lockedLogBuffer
	stub    *stubEngine
	stop    func(context.Context) error
	configs service.ConfigurationService
}

// bootAEO performs main's startup over a fresh SQLite file with no provider key
// anywhere in the environment, the stub engine as Gemini's endpoint and the
// scheduler on a manual clock, then returns the handles a root drives.
func bootAEO(t *testing.T, scheduleEnabled bool) *aeoBoot {
	t.Helper()

	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	logs := &lockedLogBuffer{}
	utils.Logger.SetOutput(logs)
	t.Cleanup(func() { utils.Logger.SetOutput(os.Stdout) })

	stub := newStubEngine(t)
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Driver:   config.DriverSQLite,
			Path:     filepath.Join(t.TempDir(), "aeo-runtime.db"),
			LogLevel: "warn",
		},
		Server: config.ServerConfig{Mode: "development"},
		JWT: config.JWTConfig{
			Secret: aeoBootSecret, ExpiryHours: 24, AccessTokenMinutes: 15, RefreshTokenDays: 30, CookieSameSite: "Lax",
		},
		Logging: config.LoggingConfig{Level: "debug", Format: "json"},
		API:     config.APIConfig{Prefix: "/api/v1", APIKeySecret: aeoBootSecret},
		SMTP:    config.SMTPConfig{From: "no-reply@localhost"},
		App:     config.AppConfig{BaseURL: "http://localhost:5173"},
		AEO: config.AEOConfig{
			// No key for any engine: the boot-time roster is empty. The base
			// URL and the egress exception are environment settings; the key
			// arrives later through the database, like one saved in Settings.
			GeminiModel:         "gemini-test",
			GeminiBaseURL:       stub.server.URL,
			ScheduleEnabled:     scheduleEnabled,
			ScheduleHour:        6,
			QueryTimeoutSeconds: 30,
			Egress:              config.ParseAEOEgressPolicy("", config.AEOEgressGemini+"="+stub.server.URL),
		},
		Forms: config.FormsConfig{PublicBaseURL: "http://localhost:8080"},
	}
	require.NoError(t, cfg.AEO.Egress.Err)

	require.NoError(t, models.InitDatabase(&cfg.Database))
	t.Cleanup(func() { _ = models.CloseDatabase() })
	require.NoError(t, models.MigrateDatabase())
	require.NoError(t, repository.NewConfigurationRepository(models.DB).InitializeDefaults())

	clock := newManualClock()
	previous := aeoSchedulerClock
	aeoSchedulerClock = clock
	t.Cleanup(func() { aeoSchedulerClock = previous })

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	_, stop := setupRouter(backgroundCtx, cfg)
	require.NotNil(t, stop, "setupRouter returns the drain handle main calls at shutdown")
	t.Cleanup(func() {
		// Whatever a root asserted, no goroutine may outlive it and write into
		// the database the next root is about to open.
		ctx, cancel := context.WithTimeout(context.Background(), aeoBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	configs := service.NewConfigurationService(
		repository.NewConfigurationRepository(models.DB),
		utils.NewSecretBox(cfg.API.APIKeySecret, "configuration-secret"))

	return &aeoBoot{cfg: cfg, clock: clock, logs: logs, stub: stub, stop: stop, configs: configs}
}

func (b *aeoBoot) stopWithin(t *testing.T, d time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return b.stop(ctx)
}

// storeGeminiKey saves a fake key through the configuration service — the same
// path the settings UI uses — after the process has booted.
func (b *aeoBoot) storeGeminiKey(t *testing.T) {
	t.Helper()
	require.NoError(t, b.configs.Set(service.ConfigAEOGeminiKey, "not-a-real-gemini-key"))
}

// seedAEOTracking saves the brand profile and one active prompt directly in the
// database, so a due tick has something to run.
func seedAEOTracking(t *testing.T) {
	t.Helper()
	profile := &models.AEOProfile{BrandName: "Acme", OwnedDomains: []string{"acme.com"}}
	profile.ID = 1
	require.NoError(t, models.DB.Create(profile).Error)
	require.NoError(t, models.DB.Create(&models.AEOPrompt{Text: "Which CRM suits a small team?", IsActive: true}).Error)
}

func aeoRuns(t *testing.T) []models.AEORun {
	t.Helper()
	var runs []models.AEORun
	require.NoError(t, models.DB.Order("id ASC").Find(&runs).Error)
	return runs
}

func aeoAnswers(t *testing.T, runID uint) []models.AEOAnswer {
	t.Helper()
	var answers []models.AEOAnswer
	require.NoError(t, models.DB.Where("run_id = ?", runID).Find(&answers).Error)
	return answers
}

// waitForTerminalRun polls until exactly one run exists and has left "running".
func waitForTerminalRun(t *testing.T) models.AEORun {
	t.Helper()
	var run models.AEORun
	require.Eventually(t, func() bool {
		// No require inside the condition: Eventually polls on its own goroutine.
		var runs []models.AEORun
		if err := models.DB.Order("id ASC").Find(&runs).Error; err != nil || len(runs) != 1 {
			return false
		}
		run = runs[0]
		return run.Status != models.AEORunStatusRunning
	}, aeoBootTimeout, 10*time.Millisecond, "the scheduled run did not reach a terminal state")
	return run
}

func TestAEORuntimeStartup(t *testing.T) {
	t.Run("enabled scheduling is armed without boot credentials and runs on stored ones when due", func(t *testing.T) {
		boot := bootAEO(t, true)

		// logrus sorts JSON keys, so the roster follows the message on one line.
		assert.Equal(t, 1, boot.logs.count(`"msg":"AEO providers loaded","providers":[]`), "no engine has credentials at boot")
		assert.Equal(t, 1, boot.logs.count(`"msg":"AEO scheduler started"`), "the scheduler is armed regardless of the empty roster")
		assert.Equal(t, 0, boot.logs.count(`"msg":"AEO scheduler not started"`))
		fire := boot.clock.armed(t)
		assert.Equal(t, 1, boot.clock.afterCalls())

		// The administrator finishes setup while the process is running.
		seedAEOTracking(t)
		boot.storeGeminiKey(t)
		fire()

		boot.stub.awaitRequest(t)
		run := waitForTerminalRun(t)
		assert.Equal(t, models.AEOTriggerScheduled, run.Trigger)
		assert.Equal(t, models.AEORunStatusCompleted, run.Status)
		assert.Equal(t, 1, run.TotalQueries, "one prompt against the one engine resolved from the database")
		assert.Equal(t, 0, run.FailedQueries)
		assert.Nil(t, run.TriggeredByID)
		require.NotNil(t, run.CompletedAt)

		answers := aeoAnswers(t, run.ID)
		require.Len(t, answers, 1)
		assert.Equal(t, "gemini", answers[0].Provider)
		assert.Equal(t, "gemini-test", answers[0].Model)
		assert.True(t, answers[0].BrandMentioned)
		assert.Empty(t, answers[0].Error)
		assert.Equal(t, []string{"Bearer not-a-real-gemini-key"}, boot.stub.authorizations(),
			"the request carried the key stored after boot, not an environment key")

		boot.clock.armed(t)
		assert.Equal(t, 2, boot.clock.afterCalls(), "the loop re-armed for the next day")

		require.NoError(t, boot.stopWithin(t, aeoBootTimeout))
		assert.Equal(t, 1, boot.logs.count(`"msg":"AEO scheduler stopped"`), "the scheduler goroutine was joined")
		assert.Equal(t, 0, boot.logs.count(`"level":"error"`))
	})

	t.Run("a due tick with incomplete configuration is logged once and the loop re-arms", func(t *testing.T) {
		boot := bootAEO(t, true)

		boot.clock.armed(t)()
		boot.clock.armed(t)
		assert.Equal(t, 2, boot.clock.afterCalls(), "the loop survived the tick and re-armed")
		assert.Equal(t, 1, boot.logs.count(`"msg":"AEO scheduled run skipped: the module is not fully configured"`))
		assert.Equal(t, 1, boot.logs.count(`"reason":"`), "the skip names its reason once")
		assert.Equal(t, 0, boot.logs.count(`"level":"error"`), "an unconfigured module is not a fault")
		assert.Empty(t, aeoRuns(t))
		assert.Empty(t, boot.stub.authorizations(), "no request is attempted without a key")

		require.NoError(t, boot.stopWithin(t, aeoBootTimeout))
	})

	t.Run("disabled scheduling never arms a timer", func(t *testing.T) {
		boot := bootAEO(t, false)

		assert.Equal(t, 1, boot.logs.count(`"msg":"AEO scheduler not started"`))
		assert.Equal(t, 0, boot.logs.count(`"msg":"AEO scheduler started"`))
		assert.Equal(t, 0, boot.clock.afterCalls())

		require.NoError(t, boot.stopWithin(t, aeoBootTimeout), "stopping a runtime without a scheduler is immediate")
		assert.Equal(t, 0, boot.clock.afterCalls())
		assert.Equal(t, 0, boot.logs.count(`"msg":"AEO scheduler stopped"`))
		assert.Empty(t, aeoRuns(t))
	})
}

func TestAEORuntimeShutdown(t *testing.T) {
	t.Run("stop cancels a scheduled admission waiting for the database", func(t *testing.T) {
		boot := bootAEO(t, true)
		fire := boot.clock.armed(t)
		seedAEOTracking(t)
		boot.storeGeminiKey(t)

		sqlDB, err := models.DB.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		held, err := sqlDB.Conn(context.Background())
		require.NoError(t, err)
		t.Cleanup(func() { _ = held.Close() })
		waits := sqlDB.Stats().WaitCount
		fire()
		require.Eventually(t, func() bool { return sqlDB.Stats().WaitCount > waits },
			aeoBootTimeout, time.Millisecond, "the due tick must be waiting in run admission")

		// Keep the connection held: only cancelling the owner's context can
		// release StartRun and let the scheduler finish before the deadline.
		require.NoError(t, boot.stopWithin(t, 5*time.Second))
		assert.Equal(t, 1, boot.logs.count(`"msg":"AEO scheduler stopped"`))
		assert.Empty(t, boot.stub.authorizations())
		require.NoError(t, held.Close())
		assert.Empty(t, aeoRuns(t), "no run was admitted after shutdown")
	})

	t.Run("stop cancels an active run, joins the scheduler and leaves terminal state before the database closes", func(t *testing.T) {
		boot := bootAEO(t, true)
		boot.stub.block.Store(true)

		fire := boot.clock.armed(t)
		seedAEOTracking(t)
		boot.storeGeminiKey(t)
		fire()
		boot.stub.awaitRequest(t)
		runs := aeoRuns(t)
		require.Len(t, runs, 1)
		assert.Equal(t, models.AEORunStatusRunning, runs[0].Status)

		require.NoError(t, boot.stopWithin(t, aeoBootTimeout))

		assert.Eventually(t, func() bool { return boot.stub.cancelled.Load() == 1 },
			aeoBootTimeout, time.Millisecond, "the in-flight provider request was cancelled")
		assert.Equal(t, 1, boot.logs.count(`"msg":"AEO scheduler stopped"`))
		assert.Equal(t, 1, boot.logs.count(`"msg":"AEO run interrupted by shutdown"`))

		run := aeoRuns(t)[0]
		assert.Equal(t, models.AEORunStatusFailed, run.Status)
		assert.Equal(t, 1, run.TotalQueries)
		assert.Equal(t, 1, run.FailedQueries)
		require.NotNil(t, run.CompletedAt, "the interruption is recorded before the handle returns")
		assert.Empty(t, aeoAnswers(t, run.ID), "a cancelled query is not recorded as an answer")

		// Only now does main close the database; nothing is left to write.
		require.NoError(t, models.CloseDatabase())
		require.NoError(t, boot.stopWithin(t, time.Second), "stopping again is a no-op")
	})

	t.Run("a blocked database write holds the drain deadline without closing the database", func(t *testing.T) {
		boot := bootAEO(t, true)
		boot.stub.block.Store(true)

		fire := boot.clock.armed(t)
		seedAEOTracking(t)
		boot.storeGeminiKey(t)
		fire()
		boot.stub.awaitRequest(t)

		// SQLite runs on one pooled connection. Holding it is exactly what a
		// write blocked in the database looks like to the engine's finalizing
		// UPDATE: it waits for the connection and cannot complete.
		sqlDB, err := models.DB.DB()
		require.NoError(t, err)
		held, err := sqlDB.Conn(context.Background())
		require.NoError(t, err)
		t.Cleanup(func() { _ = held.Close() })

		started := time.Now()
		err = drainAEO(boot.stop, 300*time.Millisecond)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Less(t, time.Since(started), 5*time.Second, "the deadline decided, not the blocked write")
		assert.Contains(t, err.Error(), "1 run(s) still active")
		assert.Equal(t, 1, boot.logs.count(`"msg":"AEO background work did not drain before the shutdown deadline"`))
		assert.Eventually(t, func() bool { return boot.stub.cancelled.Load() == 1 },
			aeoBootTimeout, time.Millisecond, "the provider call was cancelled even though the write is stuck")

		// The database is still open: releasing the connection lets the
		// bounded finalization land, and the next drain succeeds.
		require.NoError(t, held.Close())
		require.NoError(t, drainAEO(boot.stop, aeoBootTimeout))
		assert.Equal(t, 1, boot.logs.count(`"msg":"AEO background work drained"`))

		run := aeoRuns(t)[0]
		assert.Equal(t, models.AEORunStatusFailed, run.Status)
		require.NotNil(t, run.CompletedAt)
		require.NoError(t, models.CloseDatabase())
	})

	t.Run("drain helpers", func(t *testing.T) {
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "warn", Format: "json"}))
		logs := &lockedLogBuffer{}
		utils.Logger.SetOutput(logs)
		t.Cleanup(func() { utils.Logger.SetOutput(os.Stdout) })

		assert.NoError(t, drainAEO(nil, time.Second), "a process without an AEO runtime has nothing to drain")
		assert.NoError(t, drainAEO(func(context.Context) error { return nil }, time.Second))

		stuck := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
		started := time.Now()
		assert.ErrorIs(t, drainAEO(stuck, 50*time.Millisecond), context.DeadlineExceeded)
		assert.Less(t, time.Since(started), 5*time.Second)
		assert.Equal(t, 1, logs.count(`"deadline":"50ms"`), "the deadline is named in the warning")

		assert.NoError(t, shutdownFailure(nil, nil), "a clean HTTP and AEO drain allows the database close")
		assert.Error(t, shutdownFailure(nil, context.DeadlineExceeded), "an undrained AEO runtime forces the crash policy")
		assert.Error(t, shutdownFailure(context.DeadlineExceeded, nil))
	})
}
