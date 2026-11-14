package service

// Regression roots for issue #229: AEO runs are safe across several API
// replicas sharing one database. Each replica is a separate service instance
// (its own instance id, engine, run owner and connection pool) on the same
// SQLite file or the same disposable MySQL schema, through the real
// repository and the real engine. Providers are in-process gates.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// aeoReplicaOpener returns two independent handles (two pools) on one
// database, the first of which has migrated the AEO schema.
type aeoReplicaOpener func(t *testing.T) (*gorm.DB, *gorm.DB)

// openAEOReplicaPairSQLite opens one SQLite file twice, each handle with the
// production one-connection pool and pragmas, the way two processes would.
func openAEOReplicaPairSQLite(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aeo-replicas.db")
	open := func() *gorm.DB {
		dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
		db, err := gorm.Open(sqlite.Open(dsn), aeoRuntimeGormConfig())
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	first := open()
	migrateAEORuntimeSchema(t, first)
	return first, open()
}

// openAEOReplicaPairMySQL creates a disposable schema on TEST_MYSQL_DSN and
// opens two pools on it. It skips without the variable and fails when the
// server is unreachable.
func openAEOReplicaPairMySQL(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	cfg, err := mysqldriver.ParseDSN(requireAEOMySQL(t))
	require.NoError(t, err)
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, aeoRuntimeTimeout, aeoRuntimeTimeout
	server, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	name := fmt.Sprintf("gocrm_aeorep_%d_%d", time.Now().UnixNano(), aeoRuntimeDBSeq.Add(1))
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "`")
	require.NoError(t, err, "TEST_MYSQL_DSN is set but the server is unusable")
	cfg.DBName = name
	open := func() *gorm.DB {
		db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), aeoRuntimeGormConfig())
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(4)
		sqlDB.SetMaxIdleConns(4)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	first := open()
	migrateAEORuntimeSchema(t, first)
	return first, open()
}

// aeoTestClock is an injectable service clock: the wall clock plus an offset
// the scenario moves.
type aeoTestClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *aeoTestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *aeoTestClock) set(offset time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset = offset
}

// aeoReplica is one API replica: a service instance with its own instance id,
// engine, run owner and provider gate.
type aeoReplica struct {
	db       *gorm.DB
	provider *gateProvider
	runtime  *AEORuntime
	service  AEOService
}

func newAEOReplica(t *testing.T, db *gorm.DB, id string, opts ...AEOServiceOption) *aeoReplica {
	t.Helper()
	provider := newGateProvider("gate")
	repo := repository.NewAEORepository(db)
	engine := aeo.NewEngine(repo, []aeo.Provider{provider}, aeo.EngineOptions{
		Concurrency:    2,
		QueryTimeout:   aeoRuntimeTimeout,
		CleanupTimeout: 2 * time.Second,
	})
	options := append([]AEOServiceOption{WithAEOInstanceID(id)}, opts...)
	runtime := NewAEORuntime(repo, engine, []aeo.Provider{provider}, utils.NewTransactionManager(db), options...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), aeoRuntimeTimeout)
		defer cancel()
		_ = runtime.Shutdown(ctx)
	})
	t.Cleanup(provider.open)
	return &aeoReplica{db: db, provider: provider, runtime: runtime, service: runtime.Service()}
}

// seedAEOReplicaData writes the brand profile and one active prompt.
func seedAEOReplicaData(t *testing.T, db *gorm.DB, brand, text string) models.AEOPrompt {
	t.Helper()
	profile := &models.AEOProfile{BrandName: brand, OwnedDomains: []string{"acme.example"}}
	profile.ID = aeoProfileID
	require.NoError(t, db.Create(profile).Error)
	prompt := models.AEOPrompt{Text: text, IsActive: true}
	require.NoError(t, db.Create(&prompt).Error)
	return prompt
}

// aeoSeedRun is a run row written directly, the way another (possibly dead)
// process left it.
type aeoSeedRun struct {
	status    string
	owner     string
	startedAt time.Time
	heartbeat *time.Time
	lock      bool
}

func seedAEORunRow(t *testing.T, db *gorm.DB, seed aeoSeedRun) models.AEORun {
	t.Helper()
	run := models.AEORun{Trigger: models.AEOTriggerManual, Status: seed.status, StartedAt: seed.startedAt.UTC(), TotalQueries: 1}
	if seed.owner != "" {
		owner := seed.owner
		run.OwnerInstance = &owner
	}
	if seed.heartbeat != nil {
		at := seed.heartbeat.UTC()
		run.HeartbeatAt = &at
	}
	if seed.lock {
		lock := uint8(1)
		run.RunningLock = &lock
	}
	if seed.status != models.AEORunStatusRunning {
		completed := seed.startedAt.UTC().Add(time.Minute)
		run.CompletedAt = &completed
	}
	require.NoError(t, db.Create(&run).Error)
	return run
}

func readAEORun(t *testing.T, db *gorm.DB, id uint) models.AEORun {
	t.Helper()
	var run models.AEORun
	require.NoError(t, db.First(&run, id).Error)
	return run
}

func readAEORuns(t *testing.T, db *gorm.DB) []models.AEORun {
	t.Helper()
	var runs []models.AEORun
	require.NoError(t, db.Order("id ASC").Find(&runs).Error)
	return runs
}

// waitAEORunTerminal polls until the run left "running" and returns the row.
func waitAEORunTerminal(t *testing.T, db *gorm.DB, id uint) models.AEORun {
	t.Helper()
	require.Eventually(t, func() bool {
		var run models.AEORun
		if err := db.First(&run, id).Error; err != nil {
			return false
		}
		return run.Status != models.AEORunStatusRunning
	}, aeoRuntimeTimeout, 5*time.Millisecond, "run %d never reached a terminal status", id)
	return readAEORun(t, db, id)
}

func timePtr(at time.Time) *time.Time { return &at }

// assertOwnedRunning checks a run that is live under owner.
func assertOwnedRunning(t *testing.T, run models.AEORun, owner string) {
	t.Helper()
	assert.Equal(t, models.AEORunStatusRunning, run.Status)
	require.NotNil(t, run.OwnerInstance)
	assert.Equal(t, owner, *run.OwnerInstance)
	require.NotNil(t, run.RunningLock, "a running run holds the running lock")
	assert.EqualValues(t, 1, *run.RunningLock)
	require.NotNil(t, run.HeartbeatAt)
	assert.Nil(t, run.CompletedAt)
}

// assertAbandonedFailed checks a run another replica failed as abandoned.
func assertAbandonedFailed(t *testing.T, run models.AEORun) {
	t.Helper()
	assert.Equal(t, models.AEORunStatusFailed, run.Status)
	assert.NotNil(t, run.CompletedAt)
	assert.Nil(t, run.RunningLock, "a terminal run releases the running lock")
}

// ------------------------------------------------------------------ roots ---

func TestAEOReplicaBootSparesLiveRunSQLite(t *testing.T) {
	runAEOReplicaBootSparesLiveRun(t, openAEOReplicaPairSQLite)
}

func TestAEOReplicaBootSparesLiveRunMySQL(t *testing.T) {
	requireAEOMySQL(t)
	runAEOReplicaBootSparesLiveRun(t, openAEOReplicaPairMySQL)
}

func TestAEOReplicaAdmissionSQLite(t *testing.T) {
	runAEOReplicaAdmission(t, openAEOReplicaPairSQLite, false)
}

func TestAEOReplicaAdmissionMySQL(t *testing.T) {
	requireAEOMySQL(t)
	runAEOReplicaAdmission(t, openAEOReplicaPairMySQL, true)
}

func TestAEOReplicaScheduleSlotSQLite(t *testing.T) {
	runAEOReplicaScheduleSlot(t, openAEOReplicaPairSQLite, false)
}

func TestAEOReplicaScheduleSlotMySQL(t *testing.T) {
	requireAEOMySQL(t)
	runAEOReplicaScheduleSlot(t, openAEOReplicaPairMySQL, true)
}

func TestAEOReplicaDeadOwnerRecoveredSQLite(t *testing.T) {
	runAEOReplicaDeadOwnerRecovered(t, openAEOReplicaPairSQLite)
}

func TestAEOReplicaDeadOwnerRecoveredMySQL(t *testing.T) {
	requireAEOMySQL(t)
	runAEOReplicaDeadOwnerRecovered(t, openAEOReplicaPairMySQL)
}

func TestAEOReplicaFinalizeAfterTakeoverSQLite(t *testing.T) {
	runAEOReplicaFinalizeAfterTakeover(t, openAEOReplicaPairSQLite)
}

func TestAEOReplicaFinalizeAfterTakeoverMySQL(t *testing.T) {
	requireAEOMySQL(t)
	runAEOReplicaFinalizeAfterTakeover(t, openAEOReplicaPairMySQL)
}

// AC-6: the boot reconciliation of a second replica spares the first one's
// live run and fails only abandoned rows.
func runAEOReplicaBootSparesLiveRun(t *testing.T, open aeoReplicaOpener) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "warn", Format: "json"}))
	dbA, dbB := open(t)
	seedAEOReplicaData(t, dbA, "Acme", "Which CRM fits a small team?")

	a := newAEOReplica(t, dbA, "replica-a")
	live, err := a.service.StartRun(context.Background(), aeoTriggerManual, nil)
	require.NoError(t, err)
	a.provider.awaitEntry(t)

	now := time.Now().UTC()
	stale := seedAEORunRow(t, dbB, aeoSeedRun{status: models.AEORunStatusRunning, owner: "replica-gone",
		startedAt: now.Add(-10 * time.Minute), heartbeat: timePtr(now.Add(-aeoRunHeartbeatStaleAfter - 5*time.Second))})
	legacy := seedAEORunRow(t, dbB, aeoSeedRun{status: models.AEORunStatusRunning, startedAt: now.Add(-time.Minute)})
	old := seedAEORunRow(t, dbB, aeoSeedRun{status: models.AEORunStatusRunning, owner: "replica-slow",
		startedAt: now.Add(-aeoRunStaleAfter - time.Minute), heartbeat: timePtr(now)})
	done := seedAEORunRow(t, dbB, aeoSeedRun{status: models.AEORunStatusCompleted, owner: "replica-gone",
		startedAt: now.Add(-time.Hour), heartbeat: timePtr(now.Add(-time.Hour))})
	doneBefore := readAEORun(t, dbB, done.ID)

	// Replica B boots while A's run is live.
	b := newAEOReplica(t, dbB, "replica-b")
	recovered, err := b.service.ReconcileRunningRuns()
	require.NoError(t, err)
	assert.EqualValues(t, 3, recovered, "the stale, legacy and over-6h rows")

	assertOwnedRunning(t, readAEORun(t, dbB, live.ID), "replica-a")
	for _, id := range []uint{stale.ID, legacy.ID, old.ID} {
		assertAbandonedFailed(t, readAEORun(t, dbB, id))
	}
	doneAfter := readAEORun(t, dbB, done.ID)
	assert.Equal(t, models.AEORunStatusCompleted, doneAfter.Status, "a terminal row is untouched")
	assert.True(t, doneAfter.UpdatedAt.Equal(doneBefore.UpdatedAt))

	// A second boot finds nothing more to recover.
	recovered, err = newAEOReplica(t, dbB, "replica-c").service.ReconcileRunningRuns()
	require.NoError(t, err)
	assert.EqualValues(t, 0, recovered)
	assertOwnedRunning(t, readAEORun(t, dbB, live.ID), "replica-a")

	// A's run ends under its owner as usual.
	a.provider.open()
	finished := waitAEORunTerminal(t, dbA, live.ID)
	assert.Equal(t, models.AEORunStatusCompleted, finished.Status)
	assert.Nil(t, finished.RunningLock)
}

// AC-7: a run on replica A blocks starts on replica B; after it finishes B can
// start. On MySQL concurrent starts on both replicas admit exactly one.
func runAEOReplicaAdmission(t *testing.T, open aeoReplicaOpener, concurrent bool) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "warn", Format: "json"}))

	t.Run("a run on one replica blocks the other", func(t *testing.T) {
		dbA, dbB := open(t)
		prompt := seedAEOReplicaData(t, dbA, "Acme", "Which CRM fits a small team?")
		a := newAEOReplica(t, dbA, "replica-a")
		b := newAEOReplica(t, dbB, "replica-b")

		run, err := a.service.StartRun(context.Background(), aeoTriggerManual, nil)
		require.NoError(t, err)
		a.provider.awaitEntry(t)

		got, err := b.service.StartRun(context.Background(), aeoTriggerManual, nil)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, apperrors.ErrRunInProgress)
		got, err = b.service.StartPromptRun(context.Background(), prompt.ID, nil)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, apperrors.ErrRunInProgress)
		got, err = b.service.StartScheduledRun(context.Background(), time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC))
		assert.Nil(t, got)
		assert.ErrorIs(t, err, apperrors.ErrRunInProgress, "a free slot refused by the running lock is a run in progress")

		rows := readAEORuns(t, dbB)
		require.Len(t, rows, 1, "a refused start inserts no row")
		assertOwnedRunning(t, rows[0], "replica-a")
		assert.EqualValues(t, 0, b.provider.calls.Load())

		a.provider.open()
		finished := waitAEORunTerminal(t, dbA, run.ID)
		assert.Equal(t, models.AEORunStatusCompleted, finished.Status)

		next, err := b.service.StartRun(context.Background(), aeoTriggerManual, nil)
		require.NoError(t, err, "the lock is free once A's run finished")
		b.provider.awaitEntry(t)
		assertOwnedRunning(t, readAEORun(t, dbA, next.ID), "replica-b")
		require.Len(t, readAEORuns(t, dbA), 2)
	})

	if !concurrent {
		return
	}

	t.Run("concurrent starts on both replicas admit exactly one", func(t *testing.T) {
		dbA, dbB := open(t)
		seedAEOReplicaData(t, dbA, "Acme", "Which CRM fits a small team?")
		replicas := []*aeoReplica{newAEOReplica(t, dbA, "replica-a"), newAEOReplica(t, dbB, "replica-b")}

		const perReplica = 4
		results := make(chan error, perReplica*len(replicas))
		var ready, start sync.WaitGroup
		start.Add(1)
		for _, replica := range replicas {
			for i := 0; i < perReplica; i++ {
				ready.Add(1)
				go func(svc AEOService) {
					ready.Done()
					start.Wait()
					_, err := svc.StartRun(context.Background(), aeoTriggerManual, nil)
					results <- err
				}(replica.service)
			}
		}
		ready.Wait()
		start.Done()

		started, refused := 0, 0
		for i := 0; i < perReplica*len(replicas); i++ {
			switch err := <-results; {
			case err == nil:
				started++
			case errors.Is(err, apperrors.ErrRunInProgress):
				refused++
			default:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		assert.Equal(t, 1, started, "exactly one start wins the running lock")
		assert.Equal(t, perReplica*len(replicas)-1, refused)
		rows := readAEORuns(t, dbB)
		require.Len(t, rows, 1, "one aeo_runs row")
		assert.Equal(t, models.AEORunStatusRunning, rows[0].Status)
	})
}

// AC-9: one scheduled run per slot across replicas, even after it finished.
func runAEOReplicaScheduleSlot(t *testing.T, open aeoReplicaOpener, concurrent bool) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "warn", Format: "json"}))
	dbA, dbB := open(t)
	seedAEOReplicaData(t, dbA, "Acme", "Which CRM fits a small team?")
	a := newAEOReplica(t, dbA, "replica-a")
	b := newAEOReplica(t, dbB, "replica-b")

	slot := time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC)
	run, err := a.service.StartScheduledRun(context.Background(), slot)
	require.NoError(t, err)
	assert.Equal(t, aeoTriggerScheduled, run.Trigger)
	a.provider.awaitEntry(t)
	row := readAEORun(t, dbB, run.ID)
	require.NotNil(t, row.ScheduleSlot)
	assert.Equal(t, "2026-11-14T06:00Z", *row.ScheduleSlot)
	assert.Nil(t, row.TriggeredByID)

	got, err := b.service.StartScheduledRun(context.Background(), slot)
	assert.Nil(t, got)
	assert.ErrorIs(t, err, apperrors.ErrAEOScheduleSlotTaken)
	require.Len(t, readAEORuns(t, dbB), 1)

	a.provider.open()
	assert.Equal(t, models.AEORunStatusCompleted, waitAEORunTerminal(t, dbA, run.ID).Status)

	// The slot stays taken after its run finished, whatever location names
	// the same instant.
	got, err = b.service.StartScheduledRun(context.Background(), slot)
	assert.Nil(t, got)
	assert.ErrorIs(t, err, apperrors.ErrAEOScheduleSlotTaken)
	got, err = b.service.StartScheduledRun(context.Background(), slot.In(time.FixedZone("UTC+3", 3*3600)))
	assert.Nil(t, got)
	assert.ErrorIs(t, err, apperrors.ErrAEOScheduleSlotTaken)
	require.Len(t, readAEORuns(t, dbB), 1, "a refused slot inserts no row")

	// The next slot starts a new run.
	nextSlot := slot.Add(24 * time.Hour)
	next, err := b.service.StartScheduledRun(context.Background(), nextSlot)
	require.NoError(t, err)
	b.provider.open()
	nextRow := waitAEORunTerminal(t, dbB, next.ID)
	require.NotNil(t, nextRow.ScheduleSlot)
	assert.Equal(t, "2026-11-15T06:00Z", *nextRow.ScheduleSlot)
	require.NotNil(t, nextRow.OwnerInstance)
	assert.Equal(t, "replica-b", *nextRow.OwnerInstance)

	if !concurrent {
		return
	}

	// Both replicas wake for the same slot at once: exactly one row.
	race := slot.Add(48 * time.Hour)
	results := make(chan error, 2)
	var ready, start sync.WaitGroup
	start.Add(1)
	for _, replica := range []*aeoReplica{a, b} {
		ready.Add(1)
		go func(svc AEOService) {
			ready.Done()
			start.Wait()
			_, err := svc.StartScheduledRun(context.Background(), race)
			results <- err
		}(replica.service)
	}
	ready.Wait()
	start.Done()
	started, taken := 0, 0
	for i := 0; i < 2; i++ {
		switch err := <-results; {
		case err == nil:
			started++
		case errors.Is(err, apperrors.ErrAEOScheduleSlotTaken):
			taken++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	assert.Equal(t, 1, started)
	assert.Equal(t, 1, taken)
	var count int64
	require.NoError(t, dbB.Model(&models.AEORun{}).Where("schedule_slot = ?", "2026-11-16T06:00Z").Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

// AC-10: a run whose owner stopped renewing its heartbeat blocks starts until
// the horizon, then the next start on any replica fails it and is admitted.
func runAEOReplicaDeadOwnerRecovered(t *testing.T, open aeoReplicaOpener) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "warn", Format: "json"}))
	for _, scheduled := range []bool{false, true} {
		name := "manual start"
		if scheduled {
			name = "scheduled start"
		}
		t.Run(name, func(t *testing.T) {
			dbA, dbB := open(t)
			seedAEOReplicaData(t, dbA, "Acme", "Which CRM fits a small team?")
			clock := &aeoTestClock{}
			b := newAEOReplica(t, dbB, "replica-b", WithAEOClock(clock.now))

			now := time.Now().UTC()
			dead := seedAEORunRow(t, dbA, aeoSeedRun{status: models.AEORunStatusRunning, owner: "replica-dead",
				startedAt: now, heartbeat: timePtr(now), lock: true})

			slot := time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC)
			start := func() (*models.AEORun, error) {
				if scheduled {
					return b.service.StartScheduledRun(context.Background(), slot)
				}
				return b.service.StartRun(context.Background(), aeoTriggerManual, nil)
			}

			clock.set(aeoRunHeartbeatStaleAfter - 30*time.Second)
			got, err := start()
			assert.Nil(t, got)
			assert.ErrorIs(t, err, apperrors.ErrRunInProgress, "before the horizon the dead owner's run still holds the lock")
			assertOwnedRunning(t, readAEORun(t, dbB, dead.ID), "replica-dead")
			require.Len(t, readAEORuns(t, dbB), 1)

			clock.set(aeoRunHeartbeatStaleAfter + 5*time.Second)
			run, err := start()
			require.NoError(t, err, "past the horizon the start fails the abandoned run and is admitted")
			assertAbandonedFailed(t, readAEORun(t, dbB, dead.ID))
			assertOwnedRunning(t, readAEORun(t, dbB, run.ID), "replica-b")
			b.provider.awaitEntry(t)
		})
	}
}

// AC-11: once another replica failed the run, the owner's finalisation leaves
// it failed, the owner's next heartbeat cancels the executor, and nothing
// later changes the row.
func runAEOReplicaFinalizeAfterTakeover(t *testing.T, open aeoReplicaOpener) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "warn", Format: "json"}))
	dbA, dbB := open(t)
	seedAEOReplicaData(t, dbA, "Acme", "Which CRM fits a small team?")

	const interval = 20 * time.Millisecond
	a := newAEOReplica(t, dbA, "replica-a", WithAEOHeartbeatInterval(interval))
	later := &aeoTestClock{}
	later.set(aeoRunHeartbeatStaleAfter + time.Minute)
	b := newAEOReplica(t, dbB, "replica-b", WithAEOClock(later.now))

	run, err := a.service.StartRun(context.Background(), aeoTriggerManual, nil)
	require.NoError(t, err)
	a.provider.awaitEntry(t)
	require.Eventually(t, func() bool {
		var row models.AEORun
		if err := dbB.First(&row, run.ID).Error; err != nil || row.HeartbeatAt == nil {
			return false
		}
		return row.HeartbeatAt.After(row.StartedAt)
	}, aeoRuntimeTimeout, 5*time.Millisecond, "the owner renews its heartbeat while the run executes")

	// B's clock is past A's horizon, so B takes A's run over as abandoned.
	recovered, err := b.service.ReconcileRunningRuns()
	require.NoError(t, err)
	assert.EqualValues(t, 1, recovered)
	taken := readAEORun(t, dbB, run.ID)
	assertAbandonedFailed(t, taken)

	// Without any shutdown, A's next heartbeat is refused and cancels the
	// executor; the engine's conditional finalisation is refused as well.
	require.Eventually(t, func() bool { return a.runtime.ActiveRuns() == 0 }, aeoRuntimeTimeout, 5*time.Millisecond,
		"the refused heartbeat must end the owner's run")
	assert.EqualValues(t, 1, a.provider.cancelled.Load(), "the executor's provider call was cancelled")

	time.Sleep(10 * interval)
	final := readAEORun(t, dbB, run.ID)
	assert.Equal(t, models.AEORunStatusFailed, final.Status, "the owner's finalisation did not write completed or partial")
	assert.Equal(t, taken.FailedQueries, final.FailedQueries)
	assert.Equal(t, taken.TotalQueries, final.TotalQueries)
	require.NotNil(t, final.CompletedAt)
	assert.True(t, final.CompletedAt.Equal(*taken.CompletedAt), "completed_at is the takeover's")
	assert.True(t, final.UpdatedAt.Equal(taken.UpdatedAt), "no later write touched the row")
	require.NotNil(t, final.HeartbeatAt)
	assert.True(t, final.HeartbeatAt.Equal(*taken.HeartbeatAt), "no heartbeat after the takeover")
	assert.Nil(t, final.RunningLock)

	ctx, cancel := context.WithTimeout(context.Background(), aeoRuntimeTimeout)
	defer cancel()
	require.NoError(t, a.runtime.Shutdown(ctx))
}

// AC-12: the owner renews heartbeat_at at the configured interval while the run
// executes, and Shutdown returning nil leaves no heartbeat writer.
func TestAEORunHeartbeatLifecycle(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "warn", Format: "json"}))
	assert.Equal(t, 30*time.Second, time.Duration(aeoRunHeartbeatInterval))
	assert.Equal(t, 3*time.Minute, time.Duration(aeoRunHeartbeatStaleAfter))
	assert.Equal(t, 6*time.Hour, time.Duration(aeoRunStaleAfter))

	db := openAEORuntimeSQLite(t)
	seedAEOReplicaData(t, db, "Acme", "Which CRM fits a small team?")
	hooks := &aeoRepoHooks{}
	repo := newHookedAEORepo(db, hooks)
	provider := newGateProvider("gate")
	t.Cleanup(provider.open)
	engine := aeo.NewEngine(repo, []aeo.Provider{provider}, aeo.EngineOptions{CleanupTimeout: 2 * time.Second})
	runtime := NewAEORuntime(repo, engine, []aeo.Provider{provider}, utils.NewTransactionManager(db),
		WithAEOInstanceID("replica-heartbeat"), WithAEOHeartbeatInterval(20*time.Millisecond))

	run, err := runtime.Service().StartRun(context.Background(), aeoTriggerManual, nil)
	require.NoError(t, err)
	provider.awaitEntry(t)

	require.Eventually(t, func() bool { return hooks.heartbeats.Load() >= 3 }, aeoRuntimeTimeout, 5*time.Millisecond,
		"the owner renews its heartbeat at the interval")
	row := readAEORun(t, db, run.ID)
	require.NotNil(t, row.HeartbeatAt)
	assert.True(t, row.HeartbeatAt.After(row.StartedAt), "heartbeat_at advances past started_at")
	assertOwnedRunning(t, row, "replica-heartbeat")

	ctx, cancel := context.WithTimeout(context.Background(), aeoRuntimeTimeout)
	defer cancel()
	require.NoError(t, runtime.Shutdown(ctx))
	hooks.sealed.Store(true)
	beats := hooks.heartbeats.Load()

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, beats, hooks.heartbeats.Load(), "no heartbeat after Shutdown returned")
	assert.EqualValues(t, 0, hooks.lateWrites.Load())
	assert.Equal(t, 0, runtime.ActiveRuns())
	final := readAEORun(t, db, run.ID)
	assert.Equal(t, models.AEORunStatusFailed, final.Status)
	assert.Nil(t, final.RunningLock)
}

// aeoLogSink is a goroutine-safe log buffer.
type aeoLogSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *aeoLogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *aeoLogSink) entries(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s.buf.String()), "\n") {
		if line == "" {
			continue
		}
		entry := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		out = append(out, entry)
	}
	return out
}

// AC-15: the lines of a claim refusal, a slot skip, a heartbeat loss, a refused
// finalisation and an abandoned-run recovery carry run_id, counts, trigger,
// status and error codes only.
func TestAEOReplicaLogsOmitPersonalData(t *testing.T) {
	const (
		brand   = "Zylophant Brandworks"
		text    = "Which quokka-grade CRM suits a lighthouse keeper?"
		ownerA  = "owner-alpha-7f3c1d"
		ownerB  = "owner-beta-9d1e5a"
		slotKey = "2031-03-09T05:00Z"
	)
	sink := &aeoLogSink{}
	previous := utils.Logger
	logger := logrus.New()
	logger.SetOutput(sink)
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.DebugLevel)
	utils.Logger = logger
	t.Cleanup(func() { utils.Logger = previous })

	dbA, dbB := openAEOReplicaPairSQLite(t)
	seedAEOReplicaData(t, dbA, brand, text)
	a := newAEOReplica(t, dbA, ownerA, WithAEOHeartbeatInterval(20*time.Millisecond))
	b := newAEOReplica(t, dbB, ownerB)
	later := &aeoTestClock{}
	later.set(aeoRunHeartbeatStaleAfter + time.Minute)
	bLater := newAEOReplica(t, dbB, ownerB, WithAEOClock(later.now))

	slot := time.Date(2031, 3, 9, 5, 0, 0, 0, time.UTC)
	run, err := a.service.StartScheduledRun(context.Background(), slot)
	require.NoError(t, err)
	a.provider.awaitEntry(t)

	_, err = b.service.StartRun(context.Background(), aeoTriggerManual, nil)
	require.ErrorIs(t, err, apperrors.ErrRunInProgress)
	_, err = b.service.StartScheduledRun(context.Background(), slot)
	require.ErrorIs(t, err, apperrors.ErrAEOScheduleSlotTaken)

	recovered, err := bLater.service.ReconcileRunningRuns()
	require.NoError(t, err)
	require.EqualValues(t, 1, recovered)
	require.Eventually(t, func() bool { return a.runtime.ActiveRuns() == 0 }, aeoRuntimeTimeout, 5*time.Millisecond)
	assert.Equal(t, models.AEORunStatusFailed, readAEORun(t, dbB, run.ID).Status)

	for _, replica := range []*aeoReplica{a, b, bLater} {
		ctx, cancel := context.WithTimeout(context.Background(), aeoRuntimeTimeout)
		require.NoError(t, replica.runtime.Shutdown(ctx))
		cancel()
	}

	required := map[string]bool{}
	for _, msg := range []string{
		"Refusing to start an AEO run: another run holds the running lock",
		"Skipping the AEO scheduled run: its slot was already claimed",
		"Failed AEO runs whose owner stopped renewing its heartbeat",
		"AEO run heartbeat refused: the run is no longer owned by this instance",
		"AEO run finalization refused: the run is no longer owned by this instance",
		"AEO run stopped: another instance failed it as abandoned",
	} {
		required[msg] = false
	}
	allowed := map[string]bool{
		"level": true, "msg": true, "time": true, "entity": true, "service": true, "method": true,
		"component": true, "run_id": true, "recovered_runs": true, "trigger": true, "status": true,
		"error_code": true,
	}
	for _, entry := range sink.entries(t) {
		for key, value := range entry {
			textValue := fmt.Sprint(value)
			for _, secret := range []string{brand, text, ownerA, ownerB, slotKey, "2031-03-09T05:00", "2031-03-09 05:00"} {
				assert.NotContains(t, textValue, secret, "log field %s of %q leaks a protected value", key, entry["msg"])
			}
		}
		msg, _ := entry["msg"].(string)
		if _, ok := required[msg]; !ok {
			continue
		}
		required[msg] = true
		for key := range entry {
			assert.True(t, allowed[key], "line %q carries field %s", msg, key)
		}
	}
	for msg, seen := range required {
		assert.True(t, seen, "expected log line %q", msg)
	}
}
