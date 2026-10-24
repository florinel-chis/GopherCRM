package service

// Regression roots for issue #111: the AEO service owns every run it starts.
// Shutdown closes admission, cancels manual, scheduled and single-prompt runs,
// joins them before the caller may close the database, and honours an explicit
// deadline when a provider or a database write refuses to cooperate; an
// interrupted run is finalized through bounded cleanup, and a finalization that
// cannot complete leaves the row for startup reconciliation.
//
// Every root runs on a real database — in-memory SQLite with the production
// one-connection pool, and a disposable MySQL schema when TEST_MYSQL_DSN is
// set — through the real repository and the real run engine. Providers are
// in-process gates, never a network, and every blocked step is a channel the
// scenario releases, so no assertion depends on a sleep.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// aeoRuntimeTimeout bounds every wait. It is generous for a loaded CI host but
// far below the check timeout, so a hung drain fails the scenario, not the
// package.
const aeoRuntimeTimeout = 20 * time.Second

// aeoRuntimeShortDeadline is the shutdown deadline a scenario uses when it
// wants to prove the deadline, not the drain.
const aeoRuntimeShortDeadline = 300 * time.Millisecond

var aeoRuntimeDBSeq atomic.Uint64

func aeoRuntimeGormConfig() *gorm.Config {
	cfg := &gorm.Config{Logger: logger.Discard}
	cfg.DisableForeignKeyConstraintWhenMigrating = true
	return cfg
}

func migrateAEORuntimeSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(
		&models.AEOProfile{}, &models.AEOPrompt{}, &models.AEORun{}, &models.AEOAnswer{}, &models.AEOCitation{},
	))
}

// openAEORuntimeSQLite is an in-memory database with the pool pinned to one
// connection, exactly as a SQLite deployment runs.
func openAEORuntimeSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), aeoRuntimeGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateAEORuntimeSchema(t, db)
	return db
}

// requireAEOMySQL skips the whole root when no disposable server is configured;
// the named acceptance checks always configure one and reject a skip.
func requireAEOMySQL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	return raw
}

// openAEORuntimeMySQL creates a uniquely named disposable schema on the server
// identified by TEST_MYSQL_DSN and drops it afterwards. A configured but
// unreachable server fails rather than skips.
func openAEORuntimeMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	cfg, err := mysqldriver.ParseDSN(requireAEOMySQL(t))
	require.NoError(t, err)
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, aeoRuntimeTimeout, aeoRuntimeTimeout
	server, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	name := fmt.Sprintf("gocrm_aeort_%d_%d", time.Now().UnixNano(), aeoRuntimeDBSeq.Add(1))
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "`")
	require.NoError(t, err, "TEST_MYSQL_DSN is set but the server is unusable")
	cfg.DBName = name
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), aeoRuntimeGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateAEORuntimeSchema(t, db)
	return db
}

// ---------------------------------------------------------------- doubles ---

// gateProvider is an in-process answer engine whose calls block at a gate the
// scenario controls. By default a call waits until the gate opens or its
// context is cancelled; ignoreCancel makes it wait for the gate alone, the way
// a provider that never checks its context would. blockOn narrows the gate to
// some prompts; the others answer at once with a text that mentions the brand.
type gateProvider struct {
	name         string
	ignoreCancel bool
	blockOn      func(prompt string) bool

	entered     chan string
	release     chan struct{}
	releaseOnce sync.Once
	calls       atomic.Int32
	cancelled   atomic.Int32
}

func newGateProvider(name string) *gateProvider {
	return &gateProvider{name: name, entered: make(chan string, 64), release: make(chan struct{})}
}

func (p *gateProvider) Name() string  { return p.name }
func (p *gateProvider) Model() string { return "gate" }

func (p *gateProvider) Query(ctx context.Context, prompt string) (aeo.ProviderAnswer, error) {
	p.calls.Add(1)
	answer := aeo.ProviderAnswer{Text: "Acme answers: " + prompt}
	if p.blockOn != nil && !p.blockOn(prompt) {
		return answer, nil
	}
	p.entered <- prompt
	if p.ignoreCancel {
		<-p.release
		if ctx.Err() != nil {
			p.cancelled.Add(1)
			return aeo.ProviderAnswer{}, ctx.Err()
		}
		return answer, nil
	}
	select {
	case <-p.release:
		return answer, nil
	case <-ctx.Done():
		p.cancelled.Add(1)
		return aeo.ProviderAnswer{}, ctx.Err()
	}
}

// open releases every blocked and future call.
func (p *gateProvider) open() { p.releaseOnce.Do(func() { close(p.release) }) }

// awaitEntry blocks until a call has reached the gate and returns its prompt.
func (p *gateProvider) awaitEntry(t *testing.T) string {
	t.Helper()
	select {
	case prompt := <-p.entered:
		return prompt
	case <-time.After(aeoRuntimeTimeout):
		t.Fatal("no provider call reached the gate")
		return ""
	}
}

// aeoRepoHooks interposes on the repository the service and the engine write
// through. Every hook receives the context the repository was bound with, so a
// scenario can block the way a database would and observe whether cancellation
// reached that call. Writes are counted, and once the scenario seals the hooks
// after a successful join any further write is a late write.
type aeoRepoHooks struct {
	mu                 sync.Mutex
	beforeCreateRun    func(ctx context.Context) error
	afterCreateRun     func(ctx context.Context, run *models.AEORun) error
	beforeUpdateRun    func(ctx context.Context, run *models.AEORun) error
	beforeCreateAnswer func(ctx context.Context) error

	// profileReads counts completed GetProfile calls. A start's profile read
	// is its last cancellable step before it queues on the overlap guard, so
	// a scenario that needs a start parked on the guard waits for this count
	// rather than for the admission alone.
	profileReads  atomic.Int32
	createRuns    atomic.Int32
	updateRunCtxs []error
	writes        atomic.Int32
	sealed        atomic.Bool
	lateWrites    atomic.Int32
}

func (h *aeoRepoHooks) set(fn func(h *aeoRepoHooks)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

func (h *aeoRepoHooks) write() {
	h.writes.Add(1)
	if h.sealed.Load() {
		h.lateWrites.Add(1)
	}
}

// updateContexts reports, for every UpdateRun seen, whether the bound context
// was already cancelled at the time of the call.
func (h *aeoRepoHooks) updateContexts() []error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]error(nil), h.updateRunCtxs...)
}

type hookedAEORepo struct {
	repository.AEORepository
	hooks *aeoRepoHooks
	ctx   context.Context
}

func newHookedAEORepo(db *gorm.DB, hooks *aeoRepoHooks) *hookedAEORepo {
	return &hookedAEORepo{AEORepository: repository.NewAEORepository(db), hooks: hooks, ctx: context.Background()}
}

func (r *hookedAEORepo) WithContext(ctx context.Context) repository.AEORepository {
	return &hookedAEORepo{AEORepository: r.AEORepository.WithContext(ctx), hooks: r.hooks, ctx: ctx}
}

func (r *hookedAEORepo) WithTx(tx *gorm.DB) repository.AEORepository {
	return &hookedAEORepo{AEORepository: r.AEORepository.WithTx(tx), hooks: r.hooks, ctx: r.ctx}
}

func (r *hookedAEORepo) GetProfile() (*models.AEOProfile, error) {
	profile, err := r.AEORepository.GetProfile()
	r.hooks.profileReads.Add(1)
	return profile, err
}

func (r *hookedAEORepo) CreateRun(run *models.AEORun) error {
	r.hooks.write()
	r.hooks.createRuns.Add(1)
	r.hooks.mu.Lock()
	before, after := r.hooks.beforeCreateRun, r.hooks.afterCreateRun
	r.hooks.mu.Unlock()
	if before != nil {
		if err := before(r.ctx); err != nil {
			return err
		}
	}
	if err := r.AEORepository.CreateRun(run); err != nil {
		return err
	}
	if after != nil {
		return after(r.ctx, run)
	}
	return nil
}

func (r *hookedAEORepo) UpdateRun(run *models.AEORun) error {
	r.hooks.write()
	r.hooks.mu.Lock()
	r.hooks.updateRunCtxs = append(r.hooks.updateRunCtxs, r.ctx.Err())
	hook := r.hooks.beforeUpdateRun
	r.hooks.mu.Unlock()
	if hook != nil {
		if err := hook(r.ctx, run); err != nil {
			return err
		}
	}
	return r.AEORepository.UpdateRun(run)
}

func (r *hookedAEORepo) CreateAnswerWithCitations(answer *models.AEOAnswer, citations []models.AEOCitation) error {
	r.hooks.write()
	r.hooks.mu.Lock()
	hook := r.hooks.beforeCreateAnswer
	r.hooks.mu.Unlock()
	if hook != nil {
		if err := hook(r.ctx); err != nil {
			return err
		}
	}
	return r.AEORepository.CreateAnswerWithCitations(answer, citations)
}

// ---------------------------------------------------------------- fixture ---

type aeoRuntimeFixture struct {
	t       *testing.T
	db      *gorm.DB
	hooks   *aeoRepoHooks
	runtime *AEORuntime
	service AEOService
}

// newAEORuntimeFixture wires the real repository (hooked), the real engine and
// the runtime over db with the given providers. The engine runs two queries at
// a time with a short finalization bound so a blocked finalization shows up
// inside the test's own deadlines.
func newAEORuntimeFixture(t *testing.T, db *gorm.DB, providers ...aeo.Provider) *aeoRuntimeFixture {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "warn", Format: "json"}))

	hooks := &aeoRepoHooks{}
	repo := newHookedAEORepo(db, hooks)
	engine := aeo.NewEngine(repo, providers, aeo.EngineOptions{
		Concurrency:    2,
		QueryTimeout:   aeoRuntimeTimeout,
		CleanupTimeout: 2 * time.Second,
	})
	runtime := NewAEORuntime(repo, engine, providers, utils.NewTransactionManager(db))
	f := &aeoRuntimeFixture{t: t, db: db, hooks: hooks, runtime: runtime, service: runtime.Service()}
	t.Cleanup(func() {
		// Never leave a goroutine writing into a database the next scenario
		// is about to close.
		ctx, cancel := context.WithTimeout(context.Background(), aeoRuntimeTimeout)
		defer cancel()
		_ = runtime.Shutdown(ctx)
	})
	for _, provider := range providers {
		if gate, ok := provider.(*gateProvider); ok {
			t.Cleanup(gate.open)
		}
	}
	return f
}

// Always unblock a deliberately uncooperative write before runtime cleanup,
// including when a require assertion ends the scenario early.
func aeoRuntimeRelease(t *testing.T) (chan struct{}, func()) {
	t.Helper()
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	return release, unblock
}

func (f *aeoRuntimeFixture) seedProfile() {
	f.t.Helper()
	profile := &models.AEOProfile{BrandName: "Acme", OwnedDomains: []string{"acme.com"}}
	profile.ID = aeoProfileID
	require.NoError(f.t, f.db.Create(profile).Error)
}

// seedPrompts creates n active prompts named "prompt 1".."prompt n".
func (f *aeoRuntimeFixture) seedPrompts(n int) []models.AEOPrompt {
	f.t.Helper()
	prompts := make([]models.AEOPrompt, 0, n)
	for i := 1; i <= n; i++ {
		prompt := models.AEOPrompt{Text: fmt.Sprintf("prompt %d", i), IsActive: true}
		require.NoError(f.t, f.db.Create(&prompt).Error)
		prompts = append(prompts, prompt)
	}
	return prompts
}

func (f *aeoRuntimeFixture) seedInactivePrompt() models.AEOPrompt {
	f.t.Helper()
	prompt := models.AEOPrompt{Text: "draft prompt", IsActive: true}
	require.NoError(f.t, f.db.Create(&prompt).Error)
	require.NoError(f.t, f.db.Model(&prompt).UpdateColumn("is_active", false).Error)
	return prompt
}

// runs reads every run row, oldest first, outside any transaction.
func (f *aeoRuntimeFixture) runs() []models.AEORun {
	f.t.Helper()
	var runs []models.AEORun
	require.NoError(f.t, f.db.Order("id ASC").Find(&runs).Error)
	return runs
}

func (f *aeoRuntimeFixture) run(id uint) models.AEORun {
	f.t.Helper()
	var run models.AEORun
	require.NoError(f.t, f.db.First(&run, id).Error)
	return run
}

func (f *aeoRuntimeFixture) answers(runID uint) []models.AEOAnswer {
	f.t.Helper()
	var answers []models.AEOAnswer
	require.NoError(f.t, f.db.Where("run_id = ?", runID).Order("id ASC").Find(&answers).Error)
	return answers
}

// status and answerCount are the poll-safe readers: they never fail the test,
// so they can run inside Eventually's own goroutine.
func (f *aeoRuntimeFixture) status(id uint) string {
	var run models.AEORun
	if err := f.db.First(&run, id).Error; err != nil {
		return ""
	}
	return run.Status
}

func (f *aeoRuntimeFixture) answerCount(runID uint) int64 {
	var count int64
	if err := f.db.Model(&models.AEOAnswer{}).Where("run_id = ?", runID).Count(&count).Error; err != nil {
		return -1
	}
	return count
}

func (f *aeoRuntimeFixture) runningCount() int64 {
	f.t.Helper()
	var count int64
	require.NoError(f.t, f.db.Model(&models.AEORun{}).Where("status = ?", models.AEORunStatusRunning).Count(&count).Error)
	return count
}

// shutdown drains the runtime under the test deadline and seals the hooks, so
// any write after a successful join is a late write.
func (f *aeoRuntimeFixture) shutdown() error {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), aeoRuntimeTimeout)
	defer cancel()
	err := f.runtime.Shutdown(ctx)
	if err == nil {
		f.hooks.sealed.Store(true)
	}
	return err
}

func (f *aeoRuntimeFixture) shutdownWithin(d time.Duration) error {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return f.runtime.Shutdown(ctx)
}

// assertTerminal checks the persisted row of an interrupted or finished run.
func assertTerminal(t *testing.T, run models.AEORun, status string, total, failed int) {
	t.Helper()
	assert.Equal(t, status, run.Status)
	assert.Equal(t, total, run.TotalQueries)
	assert.Equal(t, failed, run.FailedQueries)
	require.NotNil(t, run.CompletedAt, "a terminal run carries completed_at")
	assert.False(t, run.CompletedAt.Before(run.StartedAt), "completed_at is not before started_at")
}

// ------------------------------------------------------------------ roots ---

func TestAEORunLifecycleSQLite(t *testing.T) {
	runAEOLifecycleScenarios(t, openAEORuntimeSQLite)
}

func TestAEORunLifecycleMySQL(t *testing.T) {
	requireAEOMySQL(t)
	runAEOLifecycleScenarios(t, openAEORuntimeMySQL)
}

func TestAEOConcurrentShutdownSQLite(t *testing.T) {
	runAEOConcurrentShutdownScenarios(t, openAEORuntimeSQLite)
}

func TestAEOConcurrentShutdownMySQL(t *testing.T) {
	requireAEOMySQL(t)
	runAEOConcurrentShutdownScenarios(t, openAEORuntimeMySQL)
}

func runAEOLifecycleScenarios(t *testing.T, open func(t *testing.T) *gorm.DB) {
	t.Run("an interrupted manual run is finalized failed with no late writes", func(t *testing.T) {
		provider := newGateProvider("gate")
		f := newAEORuntimeFixture(t, open(t), provider)
		f.seedProfile()
		f.seedPrompts(1)

		run, err := f.service.StartRun(context.Background(), "manual", nil)
		require.NoError(t, err)
		provider.awaitEntry(t)
		assert.Equal(t, 1, f.runtime.ActiveRuns())
		assert.EqualValues(t, 1, f.runningCount())

		require.NoError(t, f.shutdown())

		assert.EqualValues(t, 1, provider.cancelled.Load(), "cancellation reached the provider call")
		assert.Equal(t, 0, f.runtime.ActiveRuns())
		rows := f.runs()
		require.Len(t, rows, 1)
		assert.Equal(t, run.ID, rows[0].ID)
		assert.Equal(t, models.AEOTriggerManual, rows[0].Trigger)
		assertTerminal(t, rows[0], models.AEORunStatusFailed, 1, 1)
		assert.Empty(t, f.answers(run.ID), "a cancelled query is not recorded as an answer")
		assert.EqualValues(t, 0, f.runningCount())

		// A second drain is a no-op and nothing wrote after the first join.
		require.NoError(t, f.shutdown())
		assert.EqualValues(t, 0, f.hooks.lateWrites.Load())
	})

	t.Run("a scheduled run interrupted after recorded progress ends partial", func(t *testing.T) {
		provider := newGateProvider("gate")
		provider.blockOn = func(prompt string) bool { return prompt == "prompt 2" }
		f := newAEORuntimeFixture(t, open(t), provider)
		f.seedProfile()
		f.seedPrompts(2)

		run, err := f.service.StartRun(context.Background(), aeoTriggerScheduled, nil)
		require.NoError(t, err)
		assert.Equal(t, "prompt 2", provider.awaitEntry(t))
		require.Eventually(t, func() bool { return f.answerCount(run.ID) == 1 }, aeoRuntimeTimeout, 5*time.Millisecond,
			"the unblocked prompt's answer must be recorded before the run is interrupted")

		require.NoError(t, f.shutdown())

		row := f.run(run.ID)
		assert.Equal(t, models.AEOTriggerScheduled, row.Trigger)
		assertTerminal(t, row, models.AEORunStatusPartial, 2, 1)
		answers := f.answers(run.ID)
		require.Len(t, answers, 1, "the recorded answer survives, the interrupted one is not written")
		assert.Equal(t, "gate", answers[0].Provider)
		assert.True(t, answers[0].BrandMentioned)
		assert.Empty(t, answers[0].Error)
		assert.EqualValues(t, 0, f.hooks.lateWrites.Load())
	})

	t.Run("a single-prompt run is owned like a full run", func(t *testing.T) {
		provider := newGateProvider("gate")
		f := newAEORuntimeFixture(t, open(t), provider)
		f.seedProfile()
		draft := f.seedInactivePrompt()

		run, err := f.service.StartPromptRun(context.Background(), draft.ID, nil)
		require.NoError(t, err)
		assert.Equal(t, "draft prompt", provider.awaitEntry(t))

		require.NoError(t, f.shutdown())

		row := f.run(run.ID)
		assert.Equal(t, models.AEOTriggerManual, row.Trigger)
		assertTerminal(t, row, models.AEORunStatusFailed, 1, 1)
		assert.EqualValues(t, 1, provider.cancelled.Load())
		assert.EqualValues(t, 0, f.runningCount())
	})

	t.Run("an accepted run is not coupled to the request context", func(t *testing.T) {
		provider := newGateProvider("gate")
		provider.blockOn = func(string) bool { return false } // answers at once
		f := newAEORuntimeFixture(t, open(t), provider)
		f.seedProfile()
		f.seedPrompts(2)

		requestCtx, cancelRequest := context.WithCancel(context.Background())
		cancelRequest() // the client is gone before the service even looks
		run, err := f.service.StartRun(requestCtx, "manual", nil)
		require.NoError(t, err, "a cancelled request context must not refuse or abort the run")

		require.Eventually(t, func() bool {
			status := f.status(run.ID)
			return status != "" && status != models.AEORunStatusRunning
		}, aeoRuntimeTimeout, 5*time.Millisecond)
		assertTerminal(t, f.run(run.ID), models.AEORunStatusCompleted, 2, 0)
		assert.Len(t, f.answers(run.ID), 2)
		assert.EqualValues(t, 0, provider.cancelled.Load())

		require.NoError(t, f.shutdown())
	})

	t.Run("cancellation reaches a blocked answer write and finalization uses a live context", func(t *testing.T) {
		provider := newGateProvider("gate")
		provider.blockOn = func(string) bool { return false }
		f := newAEORuntimeFixture(t, open(t), provider)
		f.seedProfile()
		f.seedPrompts(1)

		writeEntered := make(chan struct{}, 1)
		var writeSaw atomic.Value
		f.hooks.set(func(h *aeoRepoHooks) {
			// A write that honours its context the way the drivers do: it
			// returns only once the run context is cancelled.
			h.beforeCreateAnswer = func(ctx context.Context) error {
				writeEntered <- struct{}{}
				<-ctx.Done()
				writeSaw.Store(ctx.Err())
				return ctx.Err()
			}
		})

		run, err := f.service.StartRun(context.Background(), "manual", nil)
		require.NoError(t, err)
		select {
		case <-writeEntered:
		case <-time.After(aeoRuntimeTimeout):
			t.Fatal("the answer write never started")
		}

		require.NoError(t, f.shutdown())

		saw, _ := writeSaw.Load().(error)
		assert.ErrorIs(t, saw, context.Canceled, "the blocked write observed the run cancellation")
		updates := f.hooks.updateContexts()
		require.Len(t, updates, 1, "exactly one finalizing write")
		assert.NoError(t, updates[0], "finalization runs on a context that is still alive")
		assertTerminal(t, f.run(run.ID), models.AEORunStatusFailed, 1, 1)
		assert.Empty(t, f.answers(run.ID))
	})

	t.Run("a finalization that fails leaves the row for startup reconciliation", func(t *testing.T) {
		provider := newGateProvider("gate")
		db := open(t)
		f := newAEORuntimeFixture(t, db, provider)
		f.seedProfile()
		f.seedPrompts(1)
		f.hooks.set(func(h *aeoRepoHooks) {
			h.beforeUpdateRun = func(context.Context, *models.AEORun) error { return errors.New("disk full") }
		})

		run, err := f.service.StartRun(context.Background(), "manual", nil)
		require.NoError(t, err)
		provider.awaitEntry(t)

		require.NoError(t, f.shutdown(), "the join succeeds even though the terminal write failed")
		assert.Equal(t, models.AEORunStatusRunning, f.run(run.ID).Status, "nothing could record the interruption")

		// The next boot reconciles exactly as after a crash.
		restarted := NewAEORuntime(repository.NewAEORepository(db), aeo.NewEngine(repository.NewAEORepository(db), nil, aeo.EngineOptions{}), nil, utils.NewTransactionManager(db))
		recovered, err := restarted.Service().ReconcileRunningRuns()
		require.NoError(t, err)
		assert.EqualValues(t, 1, recovered)
		row := f.run(run.ID)
		assert.Equal(t, models.AEORunStatusFailed, row.Status)
		require.NotNil(t, row.CompletedAt)
		assert.EqualValues(t, 0, f.runningCount())
	})

	t.Run("a finalization blocked past the deadline reports active work, then drains", func(t *testing.T) {
		provider := newGateProvider("gate")
		f := newAEORuntimeFixture(t, open(t), provider)
		f.seedProfile()
		f.seedPrompts(1)

		release, unblock := aeoRuntimeRelease(t)
		f.hooks.set(func(h *aeoRepoHooks) {
			// A database write that ignores cancellation until the scenario
			// lets it through.
			h.beforeUpdateRun = func(context.Context, *models.AEORun) error { <-release; return nil }
		})

		run, err := f.service.StartRun(context.Background(), "manual", nil)
		require.NoError(t, err)
		provider.awaitEntry(t)

		started := time.Now()
		err = f.shutdownWithin(aeoRuntimeShortDeadline)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Contains(t, err.Error(), "1 run(s) still active")
		assert.Less(t, time.Since(started), 5*time.Second, "the deadline, not the blocked write, decided when Shutdown returned")
		assert.Equal(t, 1, f.runtime.ActiveRuns(), "owned work is reported, not hidden")
		assert.Equal(t, models.AEORunStatusRunning, f.run(run.ID).Status)

		unblock()
		require.NoError(t, f.shutdown())
		assert.Equal(t, 0, f.runtime.ActiveRuns())
		assertTerminal(t, f.run(run.ID), models.AEORunStatusFailed, 1, 1)
		assert.EqualValues(t, 0, f.hooks.lateWrites.Load())
	})
}

func runAEOConcurrentShutdownScenarios(t *testing.T, open func(t *testing.T) *gorm.DB) {
	t.Run("concurrent manual and scheduled starts admit exactly one run", func(t *testing.T) {
		provider := newGateProvider("gate")
		f := newAEORuntimeFixture(t, open(t), provider)
		f.seedProfile()
		f.seedPrompts(1)

		const callers = 8
		results := make(chan error, callers)
		var ready, start sync.WaitGroup
		start.Add(1)
		for i := 0; i < callers; i++ {
			trigger := aeoTriggerManual
			if i%2 == 1 {
				trigger = aeoTriggerScheduled
			}
			ready.Add(1)
			go func() {
				ready.Done()
				start.Wait()
				_, err := f.service.StartRun(context.Background(), trigger, nil)
				results <- err
			}()
		}
		ready.Wait()
		start.Done()

		started, refused := 0, 0
		for i := 0; i < callers; i++ {
			switch err := <-results; {
			case err == nil:
				started++
			case errors.Is(err, apperrors.ErrRunInProgress):
				refused++
			default:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		assert.Equal(t, 1, started, "exactly one caller wins the overlap guard")
		assert.Equal(t, callers-1, refused)
		require.Len(t, f.runs(), 1, "one aeo_runs row")
		provider.awaitEntry(t)

		require.NoError(t, f.shutdown())
		rows := f.runs()
		require.Len(t, rows, 1)
		assertTerminal(t, rows[0], models.AEORunStatusFailed, 1, 1)
		assert.EqualValues(t, 0, f.runningCount())
	})

	t.Run("shutdown does not deadlock on a start blocked before its insert", func(t *testing.T) {
		provider := newGateProvider("gate")
		f := newAEORuntimeFixture(t, open(t), provider)
		f.seedProfile()
		f.seedPrompts(1)

		entered := make(chan struct{}, 1)
		release, unblock := aeoRuntimeRelease(t)
		f.hooks.set(func(h *aeoRepoHooks) {
			h.beforeCreateRun = func(context.Context) error {
				entered <- struct{}{}
				<-release
				return nil
			}
		})

		first := make(chan error, 1)
		go func() {
			_, err := f.service.StartRun(context.Background(), aeoTriggerManual, nil)
			first <- err
		}()
		select {
		case <-entered:
		case <-time.After(aeoRuntimeTimeout):
			t.Fatal("the start never reached the insert")
		}
		// A second start queues on the overlap guard behind the blocked one.
		// Admission alone does not prove it is parked there: its profile read
		// runs on the run context and would simply fail if shutdown cancelled
		// it first. Once that read has returned, the only step left before the
		// guard is in-memory, so the start is committed to the mutex.
		second := make(chan error, 1)
		go func() {
			_, err := f.service.StartRun(context.Background(), aeoTriggerScheduled, nil)
			second <- err
		}()
		require.Eventually(t, func() bool {
			return f.runtime.ActiveRuns() == 2 && f.hooks.profileReads.Load() == 2
		}, aeoRuntimeTimeout, time.Millisecond, "the second start must be queued on the guard before shutdown")

		started := time.Now()
		err := f.shutdownWithin(aeoRuntimeShortDeadline)
		require.ErrorIs(t, err, context.DeadlineExceeded, "shutdown must not wait on the guard mutex")
		assert.Contains(t, err.Error(), "2 run(s) still active", "the start blocked in its insert and the one queued on the guard are both owned work")
		assert.Less(t, time.Since(started), 5*time.Second)

		unblock()
		for _, done := range []chan error{first, second} {
			select {
			case err := <-done:
				assert.ErrorIs(t, err, ErrAEOShuttingDown)
			case <-time.After(aeoRuntimeTimeout):
				t.Fatal("a start did not return after the database was released")
			}
		}
		require.NoError(t, f.shutdown())
		assert.Equal(t, 0, f.runtime.ActiveRuns())
		// The insert itself ran on the cancelled run context, so it never
		// reached the database.
		assert.Empty(t, f.runs())
		assert.EqualValues(t, 0, provider.calls.Load())
	})

	t.Run("a run inserted as shutdown arrives is failed, not executed", func(t *testing.T) {
		provider := newGateProvider("gate")
		f := newAEORuntimeFixture(t, open(t), provider)
		f.seedProfile()
		f.seedPrompts(1)

		inserted := make(chan struct{}, 1)
		release, unblock := aeoRuntimeRelease(t)
		f.hooks.set(func(h *aeoRepoHooks) {
			// The row is committed, then the call stalls — the window between
			// the insert and the executor taking over.
			h.afterCreateRun = func(context.Context, *models.AEORun) error {
				inserted <- struct{}{}
				<-release
				return nil
			}
		})

		result := make(chan error, 1)
		go func() {
			_, err := f.service.StartRun(context.Background(), aeoTriggerManual, nil)
			result <- err
		}()
		select {
		case <-inserted:
		case <-time.After(aeoRuntimeTimeout):
			t.Fatal("the insert never happened")
		}
		assert.EqualValues(t, 1, f.runningCount())

		require.ErrorIs(t, f.shutdownWithin(aeoRuntimeShortDeadline), context.DeadlineExceeded)
		unblock()
		select {
		case err := <-result:
			assert.ErrorIs(t, err, ErrAEOShuttingDown)
		case <-time.After(aeoRuntimeTimeout):
			t.Fatal("the start did not return")
		}
		require.NoError(t, f.shutdown())

		rows := f.runs()
		require.Len(t, rows, 1)
		assertTerminal(t, rows[0], models.AEORunStatusFailed, 1, 1)
		assert.EqualValues(t, 0, provider.calls.Load(), "the interrupted run never reached a provider")
		assert.EqualValues(t, 0, f.hooks.lateWrites.Load())
	})

	t.Run("nothing starts after shutdown", func(t *testing.T) {
		provider := newGateProvider("gate")
		f := newAEORuntimeFixture(t, open(t), provider)
		f.seedProfile()
		prompts := f.seedPrompts(1)

		require.NoError(t, f.shutdown())

		run, err := f.service.StartRun(context.Background(), aeoTriggerManual, nil)
		assert.Nil(t, run)
		assert.ErrorIs(t, err, ErrAEOShuttingDown)
		run, err = f.service.StartRun(context.Background(), aeoTriggerScheduled, nil)
		assert.Nil(t, run)
		assert.ErrorIs(t, err, ErrAEOShuttingDown)
		run, err = f.service.StartPromptRun(context.Background(), prompts[0].ID, nil)
		assert.Nil(t, run)
		assert.ErrorIs(t, err, ErrAEOShuttingDown)

		assert.EqualValues(t, 0, f.hooks.createRuns.Load(), "a refused start never reaches the database")
		assert.Empty(t, f.runs())
		assert.EqualValues(t, 0, provider.calls.Load())
		assert.Equal(t, 0, f.runtime.ActiveRuns())
	})

	t.Run("repeated and concurrent shutdown calls are safe", func(t *testing.T) {
		provider := newGateProvider("gate")
		f := newAEORuntimeFixture(t, open(t), provider)
		f.seedProfile()
		f.seedPrompts(1)

		run, err := f.service.StartRun(context.Background(), aeoTriggerManual, nil)
		require.NoError(t, err)
		provider.awaitEntry(t)

		const callers = 4
		results := make(chan error, callers)
		for i := 0; i < callers; i++ {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), aeoRuntimeTimeout)
				defer cancel()
				results <- f.runtime.Shutdown(ctx)
			}()
		}
		for i := 0; i < callers; i++ {
			select {
			case err := <-results:
				assert.NoError(t, err)
			case <-time.After(aeoRuntimeTimeout):
				t.Fatal("a concurrent Shutdown did not return")
			}
		}
		require.NoError(t, f.shutdown())
		assertTerminal(t, f.run(run.ID), models.AEORunStatusFailed, 1, 1)
		assert.EqualValues(t, 1, provider.cancelled.Load(), "the run was cancelled once")
		assert.Equal(t, 0, f.runtime.ActiveRuns())
	})

	t.Run("starts racing shutdown leave no owned work and no running row", func(t *testing.T) {
		db := open(t)
		for round := 0; round < 5; round++ {
			require.NoError(t, db.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&models.AEOAnswer{}).Error)
			require.NoError(t, db.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&models.AEORun{}).Error)
			require.NoError(t, db.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&models.AEOPrompt{}).Error)
			require.NoError(t, db.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&models.AEOProfile{}).Error)

			provider := newGateProvider("gate")
			f := newAEORuntimeFixture(t, db, provider)
			f.seedProfile()
			f.seedPrompts(1)

			const starters = 6
			var ready, start, finished sync.WaitGroup
			start.Add(1)
			results := make(chan error, starters)
			for i := 0; i < starters; i++ {
				trigger := aeoTriggerManual
				if i%2 == 1 {
					trigger = aeoTriggerScheduled
				}
				ready.Add(1)
				finished.Add(1)
				go func() {
					defer finished.Done()
					ready.Done()
					start.Wait()
					_, err := f.service.StartRun(context.Background(), trigger, nil)
					results <- err
				}()
			}
			shutdownDone := make(chan error, 1)
			ready.Add(1)
			go func() {
				ready.Done()
				start.Wait()
				shutdownDone <- f.shutdown()
			}()
			ready.Wait()
			start.Done()

			finished.Wait()
			close(results)
			for err := range results {
				if err != nil {
					assert.True(t, errors.Is(err, apperrors.ErrRunInProgress) || errors.Is(err, ErrAEOShuttingDown),
						"round %d: unexpected error %v", round, err)
				}
			}
			select {
			case err := <-shutdownDone:
				require.NoError(t, err, "round %d", round)
			case <-time.After(aeoRuntimeTimeout):
				t.Fatalf("round %d: shutdown did not return", round)
			}
			require.NoError(t, f.shutdown())

			assert.Equal(t, 0, f.runtime.ActiveRuns(), "round %d", round)
			rows := f.runs()
			assert.LessOrEqual(t, len(rows), 1, "round %d: the overlap guard holds under the race", round)
			for _, row := range rows {
				assert.NotEqual(t, models.AEORunStatusRunning, row.Status, "round %d: every admitted run is terminal", round)
				assert.NotNil(t, row.CompletedAt, "round %d", round)
			}
			assert.EqualValues(t, 0, f.hooks.lateWrites.Load(), "round %d: nothing wrote after the join", round)
		}
	})

	t.Run("a provider that ignores cancellation holds the deadline but not the database", func(t *testing.T) {
		provider := newGateProvider("gate")
		provider.ignoreCancel = true
		f := newAEORuntimeFixture(t, open(t), provider)
		f.seedProfile()
		f.seedPrompts(1)

		run, err := f.service.StartRun(context.Background(), aeoTriggerManual, nil)
		require.NoError(t, err)
		provider.awaitEntry(t)

		err = f.shutdownWithin(aeoRuntimeShortDeadline)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, 1, f.runtime.ActiveRuns(), "the uncooperative call is still owned work")
		assert.Equal(t, models.AEORunStatusRunning, f.run(run.ID).Status, "the row is left for recovery while work is active")

		// Nothing new is admitted while the drain is outstanding.
		_, err = f.service.StartRun(context.Background(), aeoTriggerScheduled, nil)
		assert.ErrorIs(t, err, ErrAEOShuttingDown)

		provider.open()
		require.NoError(t, f.shutdown())
		assert.Equal(t, 0, f.runtime.ActiveRuns())
		assertTerminal(t, f.run(run.ID), models.AEORunStatusFailed, 1, 1)
		assert.Empty(t, f.answers(run.ID), "an answer returned after cancellation is not recorded")
	})
}
