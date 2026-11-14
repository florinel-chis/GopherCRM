package repository

// Regression roots for issue #229: run ownership on aeo_runs. ClaimRun admits
// a run in one INSERT guarded by the unique running_lock and schedule_slot
// indexes; FinishRun and RenewRunHeartbeat are conditional on the owner and
// the running status; FailAbandonedRuns fails stale-heartbeat, legacy and
// over-6h rows in one UPDATE. Every root runs on a file-backed SQLite database
// and, when TEST_MYSQL_DSN is set, on a disposable MySQL schema; the MySQL
// roots skip without the variable and fail when it is unreachable.

import (
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openAEORunOwnershipDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{Logger: logger.Discard}
	var db *gorm.DB
	if !useMySQL {
		var err error
		dsn := filepath.Join(t.TempDir(), "aeo_run_ownership.db") +
			"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
		db, err = gorm.Open(sqlite.Open(dsn), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	} else {
		raw := os.Getenv("TEST_MYSQL_DSN")
		if raw == "" {
			t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
		}
		dsnCfg, err := mysqldriver.ParseDSN(raw)
		require.NoError(t, err)
		dsnCfg.DBName = ""
		dsnCfg.ParseTime = true
		dsnCfg.Timeout, dsnCfg.ReadTimeout, dsnCfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
		server, err := sql.Open("mysql", dsnCfg.FormatDSN())
		require.NoError(t, err)
		name := fmt.Sprintf("gocrm_aeo_run_owner_%d", time.Now().UnixNano())
		t.Cleanup(func() {
			_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
			_ = server.Close()
		})
		_, err = server.Exec("CREATE DATABASE `" + name + "`")
		require.NoError(t, err, "TEST_MYSQL_DSN is configured but the server is unavailable")
		dsnCfg.DBName = name
		db, err = gorm.Open(gormmysql.Open(dsnCfg.FormatDSN()), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(12)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(&models.AEORun{}))
	return db
}

// ownedRun builds a run a service would claim.
func ownedRun(owner string, startedAt time.Time, slot string) *models.AEORun {
	run := &models.AEORun{Trigger: models.AEOTriggerManual, StartedAt: startedAt, TotalQueries: 2}
	if owner != "" {
		run.OwnerInstance = &owner
	}
	if slot != "" {
		run.Trigger = models.AEOTriggerScheduled
		run.ScheduleSlot = &slot
	}
	return run
}

func countAEORuns(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&models.AEORun{}).Count(&count).Error)
	return count
}

func loadAEORun(t *testing.T, db *gorm.DB, id uint) models.AEORun {
	t.Helper()
	var run models.AEORun
	require.NoError(t, db.First(&run, id).Error)
	return run
}

// insertAEORunRow writes a run row directly, the way another process left it.
func insertAEORunRow(t *testing.T, db *gorm.DB, status, owner string, startedAt time.Time, heartbeat *time.Time, lock bool) models.AEORun {
	t.Helper()
	run := models.AEORun{Trigger: models.AEOTriggerManual, Status: status, StartedAt: startedAt}
	if owner != "" {
		run.OwnerInstance = &owner
	}
	run.HeartbeatAt = heartbeat
	if lock {
		one := uint8(1)
		run.RunningLock = &one
	}
	if status != models.AEORunStatusRunning {
		completed := startedAt.Add(time.Minute)
		run.CompletedAt = &completed
	}
	require.NoError(t, db.Create(&run).Error)
	return run
}

func ownershipNow() time.Time { return time.Now().UTC().Truncate(time.Second) }

func TestAEORunClaimSQLite(t *testing.T) {
	runAEORunClaim(t, openAEORunOwnershipDB(t, false), false)
}

func TestAEORunClaimMySQL(t *testing.T) {
	runAEORunClaim(t, openAEORunOwnershipDB(t, true), true)
}

func TestAEORunFinishSQLite(t *testing.T) {
	runAEORunFinish(t, openAEORunOwnershipDB(t, false))
}

func TestAEORunFinishMySQL(t *testing.T) {
	runAEORunFinish(t, openAEORunOwnershipDB(t, true))
}

func TestAEORunHeartbeatSQLite(t *testing.T) {
	runAEORunHeartbeat(t, openAEORunOwnershipDB(t, false))
}

func TestAEORunHeartbeatMySQL(t *testing.T) {
	runAEORunHeartbeat(t, openAEORunOwnershipDB(t, true))
}

func TestAEORunAbandonedSweepSQLite(t *testing.T) {
	runAEORunAbandonedSweep(t, openAEORunOwnershipDB(t, false), false)
}

func TestAEORunAbandonedSweepMySQL(t *testing.T) {
	runAEORunAbandonedSweep(t, openAEORunOwnershipDB(t, true), true)
}

// AC-3: the claim inserts a running row with its owner, heartbeat_at =
// started_at and running_lock = 1, refuses while the lock is held, and refuses
// a taken slot; concurrent claims commit exactly one row.
func runAEORunClaim(t *testing.T, db *gorm.DB, concurrent bool) {
	repo := NewAEORepository(db)
	now := ownershipNow()

	first := ownedRun("owner-a", now, "")
	require.NoError(t, repo.ClaimRun(first))
	require.NotZero(t, first.ID)
	stored := loadAEORun(t, db, first.ID)
	assert.Equal(t, models.AEORunStatusRunning, stored.Status)
	require.NotNil(t, stored.OwnerInstance)
	assert.Equal(t, "owner-a", *stored.OwnerInstance)
	require.NotNil(t, stored.HeartbeatAt)
	assert.True(t, stored.HeartbeatAt.Equal(stored.StartedAt), "heartbeat_at = started_at")
	assert.True(t, stored.StartedAt.Equal(now))
	require.NotNil(t, stored.RunningLock)
	assert.EqualValues(t, 1, *stored.RunningLock)
	assert.Nil(t, stored.CompletedAt)
	assert.Nil(t, stored.ScheduleSlot)

	// While the lock is held nothing else is inserted.
	second := ownedRun("owner-b", now, "")
	assert.ErrorIs(t, repo.ClaimRun(second), apperrors.ErrRunInProgress)
	assert.Zero(t, second.ID)
	free := ownedRun("owner-b", now, "2026-11-14T06:00Z")
	err := repo.ClaimRun(free)
	assert.ErrorIs(t, err, apperrors.ErrRunInProgress, "a free slot refused by the lock is a run in progress")
	assert.False(t, errors.Is(err, apperrors.ErrAEOScheduleSlotTaken))
	assert.EqualValues(t, 1, countAEORuns(t, db))

	first.Status = models.AEORunStatusCompleted
	require.NoError(t, repo.FinishRun(first))

	// A scheduled claim takes its slot.
	scheduled := ownedRun("owner-b", now.Add(time.Minute), "2026-11-14T06:00Z")
	require.NoError(t, repo.ClaimRun(scheduled))
	slotRow := loadAEORun(t, db, scheduled.ID)
	require.NotNil(t, slotRow.ScheduleSlot)
	assert.Equal(t, "2026-11-14T06:00Z", *slotRow.ScheduleSlot)
	scheduled.Status = models.AEORunStatusCompleted
	require.NoError(t, repo.FinishRun(scheduled))

	// The slot stays taken after its run finished.
	again := ownedRun("owner-a", now.Add(2*time.Minute), "2026-11-14T06:00Z")
	assert.ErrorIs(t, repo.ClaimRun(again), apperrors.ErrAEOScheduleSlotTaken)
	assert.Zero(t, again.ID)

	// With a manual run holding the lock, a taken slot is still reported as taken.
	manual := ownedRun("owner-c", now.Add(3*time.Minute), "")
	require.NoError(t, repo.ClaimRun(manual))
	assert.ErrorIs(t, repo.ClaimRun(ownedRun("owner-a", now, "2026-11-14T06:00Z")), apperrors.ErrAEOScheduleSlotTaken)
	assert.EqualValues(t, 3, countAEORuns(t, db))
	manual.Status = models.AEORunStatusFailed
	require.NoError(t, repo.FinishRun(manual))

	// Several rows with NULL running_lock and NULL schedule_slot coexist.
	var nullLocks, nullSlots int64
	require.NoError(t, db.Model(&models.AEORun{}).Where("running_lock IS NULL").Count(&nullLocks).Error)
	require.NoError(t, db.Model(&models.AEORun{}).Where("schedule_slot IS NULL").Count(&nullSlots).Error)
	assert.EqualValues(t, 3, nullLocks)
	assert.EqualValues(t, 2, nullSlots)

	// A claim without an owner is refused before any insert.
	err = repo.ClaimRun(ownedRun("", now, ""))
	require.Error(t, err)
	assert.False(t, errors.Is(err, apperrors.ErrRunInProgress))
	assert.EqualValues(t, 3, countAEORuns(t, db))

	if !concurrent {
		// SQLite: one connection serialises claims; the second is refused.
		one := ownedRun("owner-x", now, "")
		require.NoError(t, repo.ClaimRun(one))
		assert.ErrorIs(t, repo.ClaimRun(ownedRun("owner-y", now, "")), apperrors.ErrRunInProgress)
		assert.EqualValues(t, 4, countAEORuns(t, db))
		return
	}

	// MySQL: concurrent claims from separate connections commit exactly one.
	const claimers = 8
	var ready, start sync.WaitGroup
	start.Add(1)
	results := make(chan error, claimers)
	for i := 0; i < claimers; i++ {
		ready.Add(1)
		go func(i int) {
			ready.Done()
			start.Wait()
			results <- repo.ClaimRun(ownedRun(fmt.Sprintf("owner-%d", i), now, ""))
		}(i)
	}
	ready.Wait()
	start.Done()
	won, refused := 0, 0
	for i := 0; i < claimers; i++ {
		switch err := <-results; {
		case err == nil:
			won++
		case errors.Is(err, apperrors.ErrRunInProgress):
			refused++
		default:
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	assert.Equal(t, 1, won)
	assert.Equal(t, claimers-1, refused)
	var running int64
	require.NoError(t, db.Model(&models.AEORun{}).Where("status = ?", models.AEORunStatusRunning).Count(&running).Error)
	assert.EqualValues(t, 1, running)

	// Concurrent claims of one slot: one row carries it.
	var winner models.AEORun
	require.NoError(t, db.Where("status = ?", models.AEORunStatusRunning).First(&winner).Error)
	winner.Status = models.AEORunStatusCompleted
	require.NoError(t, repo.FinishRun(&winner))
	ready = sync.WaitGroup{}
	start = sync.WaitGroup{}
	start.Add(1)
	slotResults := make(chan error, claimers)
	for i := 0; i < claimers; i++ {
		ready.Add(1)
		go func(i int) {
			ready.Done()
			start.Wait()
			slotResults <- repo.ClaimRun(ownedRun(fmt.Sprintf("owner-s%d", i), now, "2026-11-15T06:00Z"))
		}(i)
	}
	ready.Wait()
	start.Done()
	won, taken := 0, 0
	for i := 0; i < claimers; i++ {
		switch err := <-slotResults; {
		case err == nil:
			won++
		case errors.Is(err, apperrors.ErrAEOScheduleSlotTaken):
			taken++
		default:
			t.Fatalf("unexpected slot claim error: %v", err)
		}
	}
	assert.Equal(t, 1, won)
	assert.Equal(t, claimers-1, taken)
	var slotRows int64
	require.NoError(t, db.Model(&models.AEORun{}).Where("schedule_slot = ?", "2026-11-15T06:00Z").Count(&slotRows).Error)
	assert.EqualValues(t, 1, slotRows)
}

// AC-4: FinishRun is conditional on the owner and the running status, and the
// repository has no full-struct write of aeo_runs.
func runAEORunFinish(t *testing.T, db *gorm.DB) {
	repo := NewAEORepository(db)
	now := ownershipNow()

	run := ownedRun("owner-a", now, "")
	require.NoError(t, repo.ClaimRun(run))
	before := loadAEORun(t, db, run.ID)

	// Another instance cannot finish it.
	intruder := "owner-b"
	foreign := *run
	foreign.OwnerInstance = &intruder
	foreign.Status = models.AEORunStatusCompleted
	assert.ErrorIs(t, repo.FinishRun(&foreign), apperrors.ErrAEORunNotOwned)
	unchanged := loadAEORun(t, db, run.ID)
	assert.Equal(t, models.AEORunStatusRunning, unchanged.Status)
	assert.True(t, unchanged.UpdatedAt.Equal(before.UpdatedAt))
	require.NotNil(t, unchanged.RunningLock)

	// A finish that names "running" is refused outright.
	stillRunning := *run
	stillRunning.Status = models.AEORunStatusRunning
	require.Error(t, repo.FinishRun(&stillRunning))

	// The owner finishes it.
	completedAt := now.Add(4 * time.Minute)
	run.Status = models.AEORunStatusPartial
	run.CompletedAt = &completedAt
	run.TotalQueries = 4
	run.FailedQueries = 1
	require.NoError(t, repo.FinishRun(run))
	finished := loadAEORun(t, db, run.ID)
	assert.Equal(t, models.AEORunStatusPartial, finished.Status)
	require.NotNil(t, finished.CompletedAt)
	assert.True(t, finished.CompletedAt.Equal(completedAt))
	assert.Equal(t, 4, finished.TotalQueries)
	assert.Equal(t, 1, finished.FailedQueries)
	assert.Nil(t, finished.RunningLock, "the terminal write releases the lock")
	require.NotNil(t, finished.HeartbeatAt)
	assert.True(t, finished.HeartbeatAt.Equal(*before.HeartbeatAt), "FinishRun does not touch heartbeat_at")

	// A second finish matches nothing.
	run.Status = models.AEORunStatusCompleted
	assert.ErrorIs(t, repo.FinishRun(run), apperrors.ErrAEORunNotOwned)
	assert.Equal(t, models.AEORunStatusPartial, loadAEORun(t, db, run.ID).Status)

	// A run another replica failed is not resurrected by its late owner.
	late := ownedRun("owner-a", now.Add(time.Minute), "")
	require.NoError(t, repo.ClaimRun(late))
	swept, err := repo.FailAbandonedRuns(now.Add(time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, swept)
	failed := loadAEORun(t, db, late.ID)
	late.Status = models.AEORunStatusCompleted
	late.FailedQueries = 0
	assert.ErrorIs(t, repo.FinishRun(late), apperrors.ErrAEORunNotOwned)
	after := loadAEORun(t, db, late.ID)
	assert.Equal(t, models.AEORunStatusFailed, after.Status)
	assert.True(t, after.UpdatedAt.Equal(failed.UpdatedAt))
	assert.True(t, after.CompletedAt.Equal(*failed.CompletedAt))

	// An unknown id or a run without an owner matches nothing.
	ghost := ownedRun("owner-a", now, "")
	ghost.ID = 999999
	ghost.Status = models.AEORunStatusCompleted
	assert.ErrorIs(t, repo.FinishRun(ghost), apperrors.ErrAEORunNotOwned)

	assertNoFullRunWrite(t)
}

// assertNoFullRunWrite proves AEORepository has no UpdateRun and that Save is
// used on no aeo_runs path: the only Save left in aeo_repository.go is the
// prompt update.
func assertNoFullRunWrite(t *testing.T) {
	t.Helper()
	iface := reflect.TypeOf((*AEORepository)(nil)).Elem()
	_, hasUpdateRun := iface.MethodByName("UpdateRun")
	assert.False(t, hasUpdateRun, "AEORepository must not offer a full-struct run update")
	_, hasMarkStale := iface.MethodByName("MarkStaleRunsFailed")
	assert.False(t, hasMarkStale)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "aeo_repository.go", nil, 0)
	require.NoError(t, err)
	var savers []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Save" {
				savers = append(savers, fn.Name.Name)
			}
			return true
		})
	}
	sort.Strings(savers)
	assert.Equal(t, []string{"UpdatePrompt"}, savers, "Save is used only for prompts, never on aeo_runs")
}

// AC-4: RenewRunHeartbeat writes heartbeat_at and updated_at only, for the
// owner of a running row.
func runAEORunHeartbeat(t *testing.T, db *gorm.DB) {
	repo := NewAEORepository(db)
	now := ownershipNow()

	run := ownedRun("owner-a", now, "")
	require.NoError(t, repo.ClaimRun(run))
	before := loadAEORun(t, db, run.ID)

	at := now.Add(30 * time.Second)
	require.NoError(t, repo.RenewRunHeartbeat(run.ID, "owner-a", at))
	renewed := loadAEORun(t, db, run.ID)
	require.NotNil(t, renewed.HeartbeatAt)
	assert.True(t, renewed.HeartbeatAt.Equal(at))
	assert.True(t, renewed.UpdatedAt.Equal(at))
	assert.Equal(t, models.AEORunStatusRunning, renewed.Status)
	assert.True(t, renewed.StartedAt.Equal(before.StartedAt))
	assert.Equal(t, before.TotalQueries, renewed.TotalQueries)
	assert.Equal(t, before.FailedQueries, renewed.FailedQueries)
	require.NotNil(t, renewed.RunningLock)
	assert.Nil(t, renewed.CompletedAt)

	// The same timestamp again changes no value; MySQL reports zero changed
	// rows, which must not read as lost ownership.
	require.NoError(t, repo.RenewRunHeartbeat(run.ID, "owner-a", at))

	// Another instance, or an unknown run, is refused and changes nothing.
	assert.ErrorIs(t, repo.RenewRunHeartbeat(run.ID, "owner-b", at.Add(time.Minute)), apperrors.ErrAEORunNotOwned)
	assert.ErrorIs(t, repo.RenewRunHeartbeat(999999, "owner-a", at.Add(time.Minute)), apperrors.ErrAEORunNotOwned)
	assert.True(t, loadAEORun(t, db, run.ID).HeartbeatAt.Equal(at))

	// A finished run is no longer renewable.
	run.Status = models.AEORunStatusCompleted
	require.NoError(t, repo.FinishRun(run))
	finished := loadAEORun(t, db, run.ID)
	assert.ErrorIs(t, repo.RenewRunHeartbeat(run.ID, "owner-a", at.Add(2*time.Minute)), apperrors.ErrAEORunNotOwned)
	after := loadAEORun(t, db, run.ID)
	assert.True(t, after.HeartbeatAt.Equal(at))
	assert.True(t, after.UpdatedAt.Equal(finished.UpdatedAt))
}

// AC-5: one conditional UPDATE fails stale-heartbeat, NULL-heartbeat and
// over-6h running rows, clears running_lock, and leaves fresh and terminal rows.
func runAEORunAbandonedSweep(t *testing.T, db *gorm.DB, concurrent bool) {
	repo := NewAEORepository(db)
	now := ownershipNow()
	startedBefore := now.Add(-6 * time.Hour)
	heartbeatBefore := now.Add(-3 * time.Minute)

	fresh := insertAEORunRow(t, db, models.AEORunStatusRunning, "owner-live", now.Add(-time.Hour), timePointer(now), false)
	stale := insertAEORunRow(t, db, models.AEORunStatusRunning, "owner-gone", now.Add(-10*time.Minute), timePointer(now.Add(-4*time.Minute)), false)
	legacy := insertAEORunRow(t, db, models.AEORunStatusRunning, "", now.Add(-time.Minute), nil, false)
	old := insertAEORunRow(t, db, models.AEORunStatusRunning, "owner-slow", now.Add(-7*time.Hour), timePointer(now), true)
	completed := insertAEORunRow(t, db, models.AEORunStatusCompleted, "owner-gone", now.Add(-8*time.Hour), timePointer(now.Add(-8*time.Hour)), false)
	failed := insertAEORunRow(t, db, models.AEORunStatusFailed, "owner-gone", now.Add(-8*time.Hour), nil, false)
	completedBefore := loadAEORun(t, db, completed.ID)
	failedBefore := loadAEORun(t, db, failed.ID)
	freshBefore := loadAEORun(t, db, fresh.ID)

	var updates []string
	var mu sync.Mutex
	callback := fmt.Sprintf("test:aeo_abandoned_sweep_%d", time.Now().UnixNano())
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
		mu.Lock()
		defer mu.Unlock()
		updates = append(updates, tx.Statement.SQL.String())
	}))

	swept, err := repo.FailAbandonedRuns(startedBefore, heartbeatBefore)
	require.NoError(t, err)
	assert.EqualValues(t, 3, swept)
	mu.Lock()
	require.Len(t, updates, 1, "one conditional UPDATE")
	assert.Contains(t, updates[0], "running_lock")
	mu.Unlock()
	require.NoError(t, db.Callback().Update().Remove(callback))

	for _, id := range []uint{stale.ID, legacy.ID, old.ID} {
		row := loadAEORun(t, db, id)
		assert.Equal(t, models.AEORunStatusFailed, row.Status, "run %d", id)
		assert.NotNil(t, row.CompletedAt, "run %d", id)
		assert.Nil(t, row.RunningLock, "run %d releases the lock", id)
		assert.False(t, row.UpdatedAt.IsZero())
	}
	freshAfter := loadAEORun(t, db, fresh.ID)
	assert.Equal(t, models.AEORunStatusRunning, freshAfter.Status)
	assert.True(t, freshAfter.UpdatedAt.Equal(freshBefore.UpdatedAt))
	assert.Nil(t, freshAfter.CompletedAt)
	completedAfter := loadAEORun(t, db, completed.ID)
	assert.Equal(t, models.AEORunStatusCompleted, completedAfter.Status)
	assert.True(t, completedAfter.UpdatedAt.Equal(completedBefore.UpdatedAt))
	failedAfter := loadAEORun(t, db, failed.ID)
	assert.True(t, failedAfter.UpdatedAt.Equal(failedBefore.UpdatedAt))
	assert.True(t, failedAfter.CompletedAt.Equal(*failedBefore.CompletedAt))

	// The released lock admits a new claim.
	next := ownedRun("owner-next", now, "")
	require.NoError(t, repo.ClaimRun(next))

	swept, err = repo.FailAbandonedRuns(startedBefore, heartbeatBefore)
	require.NoError(t, err)
	assert.EqualValues(t, 0, swept, "nothing is abandoned twice")

	if !concurrent {
		return
	}

	// MySQL: two concurrent sweeps change each abandoned row once.
	for i := 0; i < 5; i++ {
		insertAEORunRow(t, db, models.AEORunStatusRunning, "owner-gone", now.Add(-time.Hour), timePointer(now.Add(-10*time.Minute)), false)
	}
	var wg sync.WaitGroup
	counts := make(chan int64, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := repo.FailAbandonedRuns(startedBefore, heartbeatBefore)
			assert.NoError(t, err)
			counts <- n
		}()
	}
	wg.Wait()
	close(counts)
	var total int64
	for n := range counts {
		total += n
	}
	assert.EqualValues(t, 5, total, "each abandoned row is failed exactly once")
	assert.Equal(t, models.AEORunStatusRunning, loadAEORun(t, db, next.ID).Status, "a fresh claim survives both sweeps")
}

func timePointer(at time.Time) *time.Time { return &at }
