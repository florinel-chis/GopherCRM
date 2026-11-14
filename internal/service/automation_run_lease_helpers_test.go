package service

// Shared fixture of the run lease roots (#237): two engines, each with its own
// instance id, connection pool, recording repository and mailer, over one
// SQLite file or one disposable MySQL schema, sharing the injected clock. The
// recording repository wraps the real one; it counts renewals, can inject a
// renewal failure, and records every write that changed a row so a root can
// prove that a stale executor wrote nothing.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// openLeasePairSQLite opens one SQLite file twice, each handle with the
// production one-connection pool and pragmas, the way two processes would.
func openLeasePairSQLite(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "automation-leases.db")
	open := func() *gorm.DB {
		dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
		db, err := gorm.Open(sqlite.Open(dsn), automationGormConfig())
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	first := open()
	migrateAutomationTestSchema(t, first)
	return first, open()
}

// openLeasePairMySQL creates a disposable schema on TEST_MYSQL_DSN and opens
// two pools on it. It skips without the variable and fails when the server
// is unreachable.
func openLeasePairMySQL(t *testing.T, maxConns int) (*gorm.DB, *gorm.DB) {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	cfg, err := mysqldriver.ParseDSN(raw)
	require.NoError(t, err)
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, automationTestTimeout, automationTestTimeout
	server, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	name := fmt.Sprintf("gocrm_autolease_%d_%d", time.Now().UnixNano(), automationTestSeq.Add(1))
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "`")
	require.NoError(t, err, "TEST_MYSQL_DSN is set but the server is unusable")
	cfg.DBName = name
	open := func() *gorm.DB {
		db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), automationGormConfig())
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(maxConns)
		sqlDB.SetMaxIdleConns(maxConns)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	first := open()
	migrateAutomationTestSchema(t, first)
	return first, open()
}

var errInjectedRenewal = errors.New("injected renewal outage")

// leaseRecorder observes one engine's repository calls.
type leaseRecorder struct {
	mu        sync.Mutex
	renewals  int
	renewErrs []error
	failNext  int
	writes    []string
	marked    int
	errs      []error

	notOwned     chan struct{}
	notOwnedOnce sync.Once
}

func newLeaseRecorder() *leaseRecorder {
	return &leaseRecorder{notOwned: make(chan struct{})}
}

// failRenewals makes the next n renewals fail with errInjectedRenewal before
// they reach the database.
func (r *leaseRecorder) failRenewals(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failNext = n
}

func (r *leaseRecorder) renewal(do func() error) error {
	r.mu.Lock()
	inject := r.failNext > 0
	if inject {
		r.failNext--
	}
	r.mu.Unlock()
	err := errInjectedRenewal
	if !inject {
		err = do()
	}
	r.mu.Lock()
	r.renewals++
	r.renewErrs = append(r.renewErrs, err)
	r.mu.Unlock()
	if errors.Is(err, repository.ErrAutomationRunNotOwned) {
		r.notOwnedOnce.Do(func() { close(r.notOwned) })
	}
	return err
}

func (r *leaseRecorder) renewCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.renewals
}

func (r *leaseRecorder) renewErrors() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.renewErrs...)
}

// write records a repository write; only a write that changed a row counts.
func (r *leaseRecorder) write(name string, rows int64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.errs = append(r.errs, err)
		return
	}
	if rows > 0 {
		r.writes = append(r.writes, name)
	}
}

// mark starts a new window of writesSinceMark.
func (r *leaseRecorder) mark() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.marked = len(r.writes)
}

func (r *leaseRecorder) writesSinceMark() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.writes[r.marked:]...)
}

func (r *leaseRecorder) allWrites() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.writes...)
}

func (r *leaseRecorder) writeErrors() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

// leaseRecordingRepo wraps the real repository, transaction-bound copies
// included.
type leaseRecordingRepo struct {
	repository.AutomationRepository
	rec *leaseRecorder
}

func (r *leaseRecordingRepo) WithTx(tx *gorm.DB) repository.AutomationRepository {
	return &leaseRecordingRepo{AutomationRepository: r.AutomationRepository.WithTx(tx), rec: r.rec}
}

func (r *leaseRecordingRepo) RenewRunLease(id uint, owner string, at time.Time) error {
	return r.rec.renewal(func() error { return r.AutomationRepository.RenewRunLease(id, owner, at) })
}

func (r *leaseRecordingRepo) ClaimRun(id uint, fromStatus string, owner string, now time.Time) (int64, error) {
	rows, err := r.AutomationRepository.ClaimRun(id, fromStatus, owner, now)
	r.rec.write("ClaimRun", rows, err)
	return rows, err
}

func (r *leaseRecordingRepo) AdvanceRun(id uint, expectedPosition int, owner string, advance repository.AutomationRunAdvance, now time.Time) (int64, error) {
	rows, err := r.AutomationRepository.AdvanceRun(id, expectedPosition, owner, advance, now)
	r.rec.write("AdvanceRun", rows, err)
	return rows, err
}

func (r *leaseRecordingRepo) ReleaseRun(id uint, expectedPosition int, owner string, now time.Time) (int64, error) {
	rows, err := r.AutomationRepository.ReleaseRun(id, expectedPosition, owner, now)
	r.rec.write("ReleaseRun", rows, err)
	return rows, err
}

func (r *leaseRecordingRepo) RecoverStaleRun(id uint, observedOwner *string, heartbeatBefore time.Time, failCode string, now time.Time) (int64, error) {
	rows, err := r.AutomationRepository.RecoverStaleRun(id, observedOwner, heartbeatBefore, failCode, now)
	r.rec.write("RecoverStaleRun", rows, err)
	return rows, err
}

func (r *leaseRecordingRepo) FinishSendingStep(id uint, outcome, errorCode string, details map[string]interface{}) (int64, error) {
	rows, err := r.AutomationRepository.FinishSendingStep(id, outcome, errorCode, details)
	r.rec.write("FinishSendingStep", rows, err)
	return rows, err
}

func (r *leaseRecordingRepo) CreateRunStep(step *models.AutomationRunStep) error {
	err := r.AutomationRepository.CreateRunStep(step)
	r.rec.write("CreateRunStep", 1, err)
	return err
}

// newLeaseEngine builds an engine over db through a recording repository,
// on the shared clock, with a heartbeat that never fires unless a root
// shortens it.
func newLeaseEngine(db *gorm.DB, rec *leaseRecorder, m *quoteTestMailer, clock *automationClock) *AutomationEngine {
	e := NewAutomationEngine(&leaseRecordingRepo{AutomationRepository: repository.NewAutomationRepository(db), rec: rec},
		repository.NewNotificationRepository(db), repository.NewTaskRepository(db), repository.NewUserRepository(db), m,
		utils.NewTransactionManager(db))
	e.SetClock(clock.Now)
	e.heartbeatInterval = time.Hour
	return e
}

// leasePair is two API replicas: engine A over the env's pool (the env's
// engine is replaced by it) and engine B over a second pool.
type leasePair struct {
	*automationEnv
	dbB   *gorm.DB
	a     *AutomationEngine
	b     *AutomationEngine
	recA  *leaseRecorder
	recB  *leaseRecorder
	mailB *quoteTestMailer
}

func newLeasePair(t *testing.T, dbA, dbB *gorm.DB) *leasePair {
	t.Helper()
	env := newAutomationEnv(t, dbA)
	p := &leasePair{automationEnv: env, dbB: dbB, recA: newLeaseRecorder(), recB: newLeaseRecorder(), mailB: &quoteTestMailer{}}
	p.a = newLeaseEngine(dbA, p.recA, env.mailer, env.clock)
	p.b = newLeaseEngine(dbB, p.recB, p.mailB, env.clock)
	require.NotEqual(t, p.a.instanceID, p.b.instanceID)
	env.engine = p.a
	return p
}

// runRow reads the bare run row, lease columns included, through B's pool:
// on SQLite A's single connection may be held by a parked transaction.
func (p *leasePair) runRow(id uint) models.AutomationRun {
	p.t.Helper()
	var run models.AutomationRun
	require.NoError(p.t, p.dbB.First(&run, id).Error)
	return run
}

// stepRowsAt counts the run-step rows at one position.
func (p *leasePair) stepRowsAt(runID uint, position int) int64 {
	p.t.Helper()
	var count int64
	require.NoError(p.t, p.db.Model(&models.AutomationRunStep{}).Where("run_id = ? AND position = ?", runID, position).Count(&count).Error)
	return count
}

func (p *leasePair) notificationsOfRun(runID uint) int64 {
	p.t.Helper()
	var count int64
	require.NoError(p.t, p.db.Model(&models.Notification{}).Where("run_id = ?", runID).Count(&count).Error)
	return count
}

// leaseGate parks the first caller until it is opened; later callers pass.
type leaseGate struct {
	fired    sync.Once
	reached  chan struct{}
	release  chan struct{}
	openOnce sync.Once
}

// newLeaseGate returns a closed-on-cleanup gate, so a root that fails while
// an executor is parked does not leave it parked.
func newLeaseGate(t *testing.T) *leaseGate {
	g := &leaseGate{reached: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(g.open)
	return g
}

func (g *leaseGate) park() {
	first := false
	g.fired.Do(func() {
		first = true
		close(g.reached)
	})
	if first {
		<-g.release
	}
}

func (g *leaseGate) await(t *testing.T) {
	t.Helper()
	select {
	case <-g.reached:
	case <-time.After(automationTestTimeout):
		t.Fatal("the executor never reached the gate")
	}
}

func (g *leaseGate) open() {
	g.openOnce.Do(func() { close(g.release) })
}

// goAsync runs fn on its own goroutine and returns its result channel.
func goAsync(fn func() error) chan error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	return done
}

func waitDone(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(automationTestTimeout):
		t.Fatal("the call did not return")
		return nil
	}
}

// waitUntil polls cond from the test goroutine, bounded.
func waitUntil(t *testing.T, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(automationTestTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitRunOwner waits until the run row names owner.
func awaitRunOwner(t *testing.T, db *gorm.DB, runID uint, owner string) {
	t.Helper()
	waitUntil(t, "the run is claimed by the other engine", func() bool {
		var run models.AutomationRun
		if err := db.First(&run, runID).Error; err != nil {
			return false
		}
		return run.OwnerInstance != nil && *run.OwnerInstance == owner
	})
}

// awaitGoroutinesAtMost polls from the test goroutine until the process holds
// no more goroutines than baseline.
func awaitGoroutinesAtMost(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(automationTestTimeout)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines left behind: %d running, %d before", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Statements the MySQL lock-order root watches for in PROCESSLIST.
var (
	leaseRenewalSQL = regexp.MustCompile("(?is)^UPDATE [`\"]?automation_runs[`\"]? SET [`\"]?heartbeat_at[`\"]?=")
	leaseRecoverSQL = regexp.MustCompile("(?is)^UPDATE [`\"]?automation_runs[`\"]? SET [`\"]?claimed_at[`\"]?=")
	leaseAdvanceSQL = regexp.MustCompile("(?is)^UPDATE [`\"]?automation_runs[`\"]? SET [`\"]?cancel_reason[`\"]?=")
)

// awaitLeaseLockWait returns once a statement matching target is seen
// executing on the same connection on two consecutive PROCESSLIST polls.
func awaitLeaseLockWait(t *testing.T, db *gorm.DB, target *regexp.Regexp) {
	t.Helper()
	probe := &lockWaitProbe{target: target}
	ctx, cancel := context.WithTimeout(context.Background(), automationTestTimeout)
	defer cancel()
	for ctx.Err() == nil {
		var rows []executingStatement
		err := db.WithContext(ctx).Raw(`SELECT ID AS id, INFO AS info FROM information_schema.PROCESSLIST
			WHERE DB = DATABASE() AND ID <> CONNECTION_ID() AND COMMAND IN ('Query', 'Execute')
			AND INFO IS NOT NULL`).Scan(&rows).Error
		if err != nil {
			rows = nil
		}
		if probe.observe(rows) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no statement matching %s waited on a lock", target)
}
