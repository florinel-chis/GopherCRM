package repository

// Regression roots for #196 at the repository level: the conditional outcome
// writes of a bulk_operations row and the stale-pending sweep, on a
// file-backed SQLite database and, when TEST_MYSQL_DSN is set, on a disposable
// MySQL schema (skipped without the variable, failing when it is unreachable).
// Every assertion reads committed rows.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

var bulkOutcomeDBSeq atomic.Uint64

// openBulkOutcomeDB opens the database with GORM's clock in UTC, as
// production does, and migrates the tables the roots touch.
func openBulkOutcomeDB(t *testing.T, useMySQL bool, maxConns int) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{
		Logger:  logger.Discard,
		NowFunc: func() time.Time { return time.Now().UTC() },

		// Lead is migrated without its users/customers/companies relations.
		DisableForeignKeyConstraintWhenMigrating: true,
	}
	var db *gorm.DB
	if !useMySQL {
		var err error
		dsn := filepath.Join(t.TempDir(), fmt.Sprintf("bulk_outcome_%d.db", bulkOutcomeDBSeq.Add(1))) +
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
		dsnCfg.Timeout, dsnCfg.ReadTimeout, dsnCfg.WriteTimeout = 5*time.Second, 60*time.Second, 60*time.Second
		server, err := sql.Open("mysql", dsnCfg.FormatDSN())
		require.NoError(t, err)
		name := fmt.Sprintf("gocrm_bulk_outcome_%d_%d", time.Now().UnixNano(), bulkOutcomeDBSeq.Add(1))
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
		sqlDB.SetMaxOpenConns(maxConns)
		sqlDB.SetMaxIdleConns(maxConns)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Lead{}, &models.BulkOperation{}, &models.BulkOperationItem{}))
	require.True(t, db.Migrator().HasIndex(&models.BulkOperation{}, "idx_bulk_operations_status"))
	return db
}

// bulkOutcomeFixture seeds history rows directly.
type bulkOutcomeFixture struct {
	t     *testing.T
	db    *gorm.DB
	owner models.User
}

func newBulkOutcomeFixture(t *testing.T, db *gorm.DB) *bulkOutcomeFixture {
	t.Helper()
	f := &bulkOutcomeFixture{t: t, db: db}
	f.owner = models.User{Email: fmt.Sprintf("bulk-outcome-%d@example.com", bulkOutcomeDBSeq.Add(1)), Password: "x",
		FirstName: "Bulk", LastName: "Owner", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(&f.owner).Error)
	return f
}

func (f *bulkOutcomeFixture) row(status models.BulkOperationStatus, createdAt time.Time) models.BulkOperation {
	row := models.BulkOperation{UserID: f.owner.ID, ResourceType: "leads", Type: models.BulkAction,
		Status: status, TotalItems: 1}
	row.CreatedAt = createdAt
	row.UpdatedAt = createdAt
	return row
}

// seed inserts one row and returns its id.
func (f *bulkOutcomeFixture) seed(status models.BulkOperationStatus, createdAt time.Time) uint {
	f.t.Helper()
	row := f.row(status, createdAt)
	require.NoError(f.t, f.db.Create(&row).Error)
	return row.ID
}

// seedMany inserts n rows of one status and returns their ids.
func (f *bulkOutcomeFixture) seedMany(n int, status models.BulkOperationStatus, createdAt time.Time) []uint {
	f.t.Helper()
	rows := make([]models.BulkOperation, n)
	for i := range rows {
		rows[i] = f.row(status, createdAt)
	}
	require.NoError(f.t, f.db.CreateInBatches(&rows, 200).Error)
	ids := make([]uint, n)
	for i, row := range rows {
		ids[i] = row.ID
	}
	return ids
}

func (f *bulkOutcomeFixture) get(id uint) models.BulkOperation {
	f.t.Helper()
	var row models.BulkOperation
	require.NoError(f.t, f.db.Unscoped().First(&row, id).Error)
	return row
}

func (f *bulkOutcomeFixture) status(id uint) models.BulkOperationStatus {
	f.t.Helper()
	return f.get(id).Status
}

func (f *bulkOutcomeFixture) countStatus(ids []uint, status models.BulkOperationStatus) int64 {
	f.t.Helper()
	var n int64
	require.NoError(f.t, f.db.Unscoped().Model(&models.BulkOperation{}).Where("id IN ? AND status = ?", ids, status).Count(&n).Error)
	return n
}

// bulkSweepStatements records the SQL of every query and update the handle
// runs while recording is on.
type bulkSweepStatements struct {
	mu        sync.Mutex
	recording bool
	queries   []string
	updates   []string
}

func (c *bulkSweepStatements) install(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:bulk_sweep_query", func(tx *gorm.DB) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.recording {
			c.queries = append(c.queries, tx.Statement.SQL.String())
		}
	}))
	require.NoError(t, db.Callback().Update().After("gorm:update").Register("test:bulk_sweep_update", func(tx *gorm.DB) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.recording {
			c.updates = append(c.updates, tx.Statement.SQL.String())
		}
	}))
}

func (c *bulkSweepStatements) record(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recording = on
	if on {
		c.queries, c.updates = nil, nil
	}
}

func (c *bulkSweepStatements) snapshot() (queries, updates []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.queries...), append([]string(nil), c.updates...)
}

func TestBulkOperationRepositoryOutcomeSQLite(t *testing.T) {
	runBulkOperationRepositoryOutcome(t, openBulkOutcomeDB(t, false, 1))
}

func TestBulkOperationRepositoryOutcomeMySQL(t *testing.T) {
	runBulkOperationRepositoryOutcome(t, openBulkOutcomeDB(t, true, 4))
}

func runBulkOperationRepositoryOutcome(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	capture := &bulkSweepStatements{}
	capture.install(t, db)
	repo := NewBulkOperationRepository(db)
	f := newBulkOutcomeFixture(t, db)
	now := time.Now().UTC()

	t.Run("the sentinel is not a not-found or a forbidden error", func(t *testing.T) {
		err := apperrors.ErrBulkOperationNotPending
		assert.False(t, errors.Is(err, apperrors.ErrNotFound))
		assert.False(t, errors.Is(err, apperrors.ErrForbidden))
		assert.False(t, errors.Is(err, gorm.ErrRecordNotFound))
		_, isAppErr := apperrors.AsAppError(err)
		assert.False(t, isAppErr)
	})

	t.Run("CompletePending and UpdateStatus change exactly one pending row", func(t *testing.T) {
		first := f.seed(models.StatusPending, now)
		second := f.seed(models.StatusPending, now)
		bystander := f.seed(models.StatusPending, now)

		require.NoError(t, repo.CompletePending(ctx, first))
		assert.Equal(t, models.StatusCompleted, f.status(first))
		assert.Equal(t, models.StatusPending, f.status(second), "only the named row changes")

		require.ErrorIs(t, repo.CompletePending(ctx, first), apperrors.ErrBulkOperationNotPending)
		require.ErrorIs(t, repo.UpdateStatus(ctx, first, models.StatusFailed), apperrors.ErrBulkOperationNotPending,
			"a late outcome write never overwrites a completed row")
		assert.Equal(t, models.StatusCompleted, f.status(first))

		require.NoError(t, repo.UpdateStatus(ctx, second, models.StatusFailed))
		assert.Equal(t, models.StatusFailed, f.status(second))
		require.ErrorIs(t, repo.UpdateStatus(ctx, second, models.StatusCompleted), apperrors.ErrBulkOperationNotPending)
		require.ErrorIs(t, repo.CompletePending(ctx, second), apperrors.ErrBulkOperationNotPending,
			"a late completion never overwrites a failed row")
		assert.Equal(t, models.StatusFailed, f.status(second))
		assert.Equal(t, models.StatusPending, f.status(bystander))

		for _, legacy := range []models.BulkOperationStatus{models.StatusProcessing, models.StatusPartial} {
			id := f.seed(legacy, now)
			require.ErrorIs(t, repo.CompletePending(ctx, id), apperrors.ErrBulkOperationNotPending)
			require.ErrorIs(t, repo.UpdateStatus(ctx, id, models.StatusFailed), apperrors.ErrBulkOperationNotPending)
			assert.Equal(t, legacy, f.status(id), "a legacy %s row is not rewritten", legacy)
		}

		deleted := f.seed(models.StatusPending, now)
		require.NoError(t, db.Delete(&models.BulkOperation{}, deleted).Error)
		require.ErrorIs(t, repo.CompletePending(ctx, deleted), apperrors.ErrBulkOperationNotPending)
		require.ErrorIs(t, repo.UpdateStatus(ctx, deleted, models.StatusFailed), apperrors.ErrBulkOperationNotPending)
		assert.Equal(t, models.StatusPending, f.status(deleted), "a soft-deleted row is not live")

		require.ErrorIs(t, repo.CompletePending(ctx, 999999999), apperrors.ErrBulkOperationNotPending)
		require.ErrorIs(t, repo.UpdateStatus(ctx, 999999999, models.StatusFailed), apperrors.ErrBulkOperationNotPending)
	})

	t.Run("CompletePending on the transaction-bound repository commits and rolls back with it", func(t *testing.T) {
		id := f.seed(models.StatusPending, now)

		tx := db.Begin()
		require.NoError(t, tx.Error)
		require.NoError(t, repo.WithTx(tx).CompletePending(ctx, id))
		require.NoError(t, tx.Rollback().Error)
		assert.Equal(t, models.StatusPending, f.status(id), "a rolled-back batch leaves the row pending")

		tx = db.Begin()
		require.NoError(t, tx.Error)
		require.NoError(t, repo.WithTx(tx).CompletePending(ctx, id))
		require.NoError(t, tx.Commit().Error)
		assert.Equal(t, models.StatusCompleted, f.status(id))
	})

	t.Run("MarkStalePendingFailed fails only live pending rows strictly before the cutoff, in chunks", func(t *testing.T) {
		// Wipe the rows of the earlier subtests so the counts are exact.
		require.NoError(t, db.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&models.BulkOperation{}).Error)

		cutoff := now.Add(-time.Hour).Truncate(time.Second)
		staleAt := cutoff.Add(-time.Hour)
		stale := f.seedMany(1201, models.StatusPending, staleAt)
		completed := f.seed(models.StatusCompleted, staleAt)
		failed := f.seed(models.StatusFailed, staleAt)
		processing := f.seed(models.StatusProcessing, staleAt)
		partial := f.seed(models.StatusPartial, staleAt)
		softDeleted := f.seed(models.StatusPending, staleAt)
		require.NoError(t, db.Delete(&models.BulkOperation{}, softDeleted).Error)
		atCutoff := f.seed(models.StatusPending, cutoff)
		newer := f.seed(models.StatusPending, cutoff.Add(time.Second))

		if db.Dialector.Name() == "mysql" {
			// The UPDATE the sweep issues runs on the primary key, never on the
			// status index (which would lock the secondary index first).
			type explainRow struct {
				Key sql.NullString `gorm:"column:key"`
			}
			var plan []explainRow
			require.NoError(t, db.Raw("EXPLAIN UPDATE `bulk_operations` SET `status` = ?, `updated_at` = ? "+
				"WHERE id IN ? AND status = ? AND created_at < ? AND `bulk_operations`.`deleted_at` IS NULL",
				models.StatusFailed, now, stale[:BulkOperationSweepChunk], models.StatusPending, cutoff).Scan(&plan).Error)
			require.Len(t, plan, 1)
			assert.Equal(t, "PRIMARY", plan[0].Key.String, "the chunk update is driven by the primary key")
		}

		capture.record(true)
		changed, err := repo.MarkStalePendingFailed(ctx, cutoff)
		capture.record(false)
		require.NoError(t, err)
		assert.EqualValues(t, 1201, changed, "the summed count of three chunks")
		assert.EqualValues(t, 1201, f.countStatus(stale, models.StatusFailed), "every stale pending row ends failed")
		assert.True(t, f.get(stale[0]).UpdatedAt.After(staleAt), "updated_at is stamped")
		assert.True(t, f.get(stale[len(stale)-1]).UpdatedAt.After(staleAt))

		assert.Equal(t, models.StatusCompleted, f.status(completed))
		assert.Equal(t, models.StatusFailed, f.status(failed))
		assert.WithinDuration(t, staleAt, f.get(failed).UpdatedAt, time.Second, "an already failed row is not rewritten")
		assert.Equal(t, models.StatusProcessing, f.status(processing))
		assert.Equal(t, models.StatusPartial, f.status(partial))
		assert.Equal(t, models.StatusPending, f.status(softDeleted), "a soft-deleted row is left alone")
		assert.Equal(t, models.StatusPending, f.status(atCutoff), "strictly before the cutoff")
		assert.Equal(t, models.StatusPending, f.status(newer), "a newer pending row is left alone")

		queries, updates := capture.snapshot()
		require.Len(t, queries, 3, "1,201 candidates are read in chunks of 500, 500 and 201")
		require.Len(t, updates, 3, "one UPDATE per chunk")
		for _, statement := range append(append([]string(nil), queries...), updates...) {
			upper := strings.ToUpper(statement)
			assert.NotContains(t, upper, "FOR UPDATE", "no locking read: %s", statement)
			assert.NotContains(t, upper, "FOR SHARE", "no locking read: %s", statement)
			assert.NotContains(t, upper, "LOCK IN SHARE MODE", "no locking read: %s", statement)
		}
		for _, query := range queries {
			assert.Regexp(t, "(?i)^SELECT .*id.* FROM .*bulk_operations", query)
			assert.Contains(t, query, "ORDER BY")
			assert.Contains(t, query, "LIMIT 500")
			assert.Contains(t, query, "deleted_at` IS NULL")
		}
		for _, update := range updates {
			assert.Regexp(t, "(?i)^UPDATE .*bulk_operations.* SET .*status.*updated_at", update)
			assert.Contains(t, update, "id IN (", "every UPDATE names its ids: %s", update)
			assert.Contains(t, update, "status = ?")
			assert.Contains(t, update, "created_at < ?")
			assert.Contains(t, update, "deleted_at` IS NULL")
		}

		again, err := repo.MarkStalePendingFailed(ctx, cutoff)
		require.NoError(t, err)
		assert.Zero(t, again, "an immediate repeat changes nothing")
	})
}

func TestBulkOperationSweepConcurrentSQLite(t *testing.T) {
	db := openBulkOutcomeDB(t, false, 1)
	runBulkOperationSweepSequential(t, db)
}

// TestBulkOperationSweepConcurrentMySQL runs the sweeps and the batches on
// separate connections, so they really overlap.
func TestBulkOperationSweepConcurrentMySQL(t *testing.T) {
	db := openBulkOutcomeDB(t, true, 16)
	runBulkOperationSweepConcurrentMySQL(t, db)
}

// bulkBatch is what a bulk status batch does in the order the service does
// it: a transaction that writes the record, then completes its own history
// row, then commits. A completion that finds no pending row rolls everything
// back.
func bulkBatch(ctx context.Context, db *gorm.DB, repo BulkOperationRepository, operationID, leadID uint, status models.LeadStatus) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Lead{}).Where("id = ?", leadID).Update("status", status).Error; err != nil {
			return err
		}
		return repo.WithTx(tx).CompletePending(ctx, operationID)
	})
}

func seedBulkLead(t *testing.T, db *gorm.DB, owner uint) models.Lead {
	t.Helper()
	lead := models.Lead{FirstName: "Bulk", LastName: "Lead", Email: fmt.Sprintf("bulk-lead-%d@example.com", bulkOutcomeDBSeq.Add(1)),
		Status: models.LeadStatusNew, OwnerID: owner}
	require.NoError(t, db.Create(&lead).Error)
	return lead
}

func leadStatusOf(t *testing.T, db *gorm.DB, id uint) models.LeadStatus {
	t.Helper()
	var lead models.Lead
	require.NoError(t, db.First(&lead, id).Error)
	return lead.Status
}

// runBulkOperationSweepSequential is the SQLite half: its single connection
// cannot overlap two statements, so the interleavings run in sequence.
func runBulkOperationSweepSequential(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	repo := NewBulkOperationRepository(db)
	f := newBulkOutcomeFixture(t, db)
	now := time.Now().UTC()
	cutoff := now.Add(-time.Hour)

	t.Run("two sweeps together report exactly the stale rows", func(t *testing.T) {
		stale := f.seedMany(700, models.StatusPending, cutoff.Add(-time.Minute))
		first, err := repo.MarkStalePendingFailed(ctx, cutoff)
		require.NoError(t, err)
		second, err := repo.MarkStalePendingFailed(ctx, cutoff)
		require.NoError(t, err)
		assert.EqualValues(t, 700, first+second)
		assert.EqualValues(t, 700, f.countStatus(stale, models.StatusFailed))
	})

	t.Run("a batch that completes before the sweep stays completed", func(t *testing.T) {
		lead := seedBulkLead(t, db, f.owner.ID)
		id := f.seed(models.StatusPending, cutoff.Add(-time.Minute))
		require.NoError(t, bulkBatch(ctx, db, repo, id, lead.ID, models.LeadStatusContacted))
		_, err := repo.MarkStalePendingFailed(ctx, cutoff)
		require.NoError(t, err)
		assert.Equal(t, models.StatusCompleted, f.status(id))
		assert.Equal(t, models.LeadStatusContacted, leadStatusOf(t, db, lead.ID))
	})

	t.Run("a batch whose row was swept first rolls back and the row stays failed", func(t *testing.T) {
		lead := seedBulkLead(t, db, f.owner.ID)
		id := f.seed(models.StatusPending, cutoff.Add(-time.Minute))
		swept, err := repo.MarkStalePendingFailed(ctx, cutoff)
		require.NoError(t, err)
		assert.EqualValues(t, 1, swept)
		err = bulkBatch(ctx, db, repo, id, lead.ID, models.LeadStatusContacted)
		require.ErrorIs(t, err, apperrors.ErrBulkOperationNotPending)
		assert.Equal(t, models.StatusFailed, f.status(id))
		assert.Equal(t, models.LeadStatusNew, leadStatusOf(t, db, lead.ID), "the batch's record write rolled back")
	})
}

// isDeadlock reports MySQL/MariaDB error 1213.
func isDeadlock(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1213
}

func runBulkOperationSweepConcurrentMySQL(t *testing.T, db *gorm.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	repo := NewBulkOperationRepository(db)
	f := newBulkOutcomeFixture(t, db)
	now := time.Now().UTC()
	cutoff := now.Add(-time.Hour)

	t.Run("two concurrent sweeps together report exactly the stale rows", func(t *testing.T) {
		stale := f.seedMany(1201, models.StatusPending, cutoff.Add(-time.Minute))
		var wg sync.WaitGroup
		counts := make([]int64, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		for i := range counts {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				counts[i], errs[i] = repo.MarkStalePendingFailed(ctx, cutoff)
			}(i)
		}
		close(start)
		wg.Wait()
		require.NoError(t, errs[0])
		require.NoError(t, errs[1])
		assert.EqualValues(t, 1201, counts[0]+counts[1], "each row is counted once")
		assert.EqualValues(t, 1201, f.countStatus(stale, models.StatusFailed))
	})

	t.Run("a sweep racing a batch leaves the row terminal and consistent with the record", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			lead := seedBulkLead(t, db, f.owner.ID)
			id := f.seed(models.StatusPending, cutoff.Add(-time.Minute))
			var wg sync.WaitGroup
			var batchErr, sweepErr error
			start := make(chan struct{})
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				batchErr = bulkBatch(ctx, db, repo, id, lead.ID, models.LeadStatusContacted)
			}()
			go func() {
				defer wg.Done()
				<-start
				_, sweepErr = repo.MarkStalePendingFailed(ctx, cutoff)
			}()
			close(start)
			wg.Wait()
			require.NoError(t, sweepErr)
			require.False(t, isDeadlock(batchErr), "no error 1213: %v", batchErr)

			switch f.status(id) {
			case models.StatusCompleted:
				require.NoError(t, batchErr)
				assert.Equal(t, models.LeadStatusContacted, leadStatusOf(t, db, lead.ID), "iteration %d", i)
			case models.StatusFailed:
				require.ErrorIs(t, batchErr, apperrors.ErrBulkOperationNotPending)
				assert.Equal(t, models.LeadStatusNew, leadStatusOf(t, db, lead.ID), "iteration %d", i)
			default:
				t.Fatalf("iteration %d: the row is %s, never pending after both finished", i, f.status(id))
			}
		}
	})

	t.Run("live batches against looping sweeps never deadlock", func(t *testing.T) {
		const workers, batchesPerWorker, sweepers = 6, 25, 3
		existingStale := f.seedMany(600, models.StatusPending, cutoff.Add(-time.Minute))
		existingFresh := f.seedMany(50, models.StatusPending, now)

		var (
			mu        sync.Mutex
			failures  []string
			batchRows []uint
			swept     atomic.Int64
		)
		staleRows := append([]uint(nil), existingStale...)
		fail := func(format string, args ...interface{}) {
			mu.Lock()
			defer mu.Unlock()
			failures = append(failures, fmt.Sprintf(format, args...))
		}

		batchesDone := make(chan struct{})
		var batchWG, sweepWG sync.WaitGroup
		for w := 0; w < workers; w++ {
			lead := seedBulkLead(t, db, f.owner.ID)
			batchWG.Add(1)
			go func(leadID uint) {
				defer batchWG.Done()
				statuses := []models.LeadStatus{models.LeadStatusContacted, models.LeadStatusQualified}
				for b := 0; b < batchesPerWorker; b++ {
					operation := f.row(models.StatusPending, time.Now().UTC())
					if err := repo.Create(ctx, &operation); err != nil {
						fail("create: deadlock=%t %v", isDeadlock(err), err)
						return
					}
					mu.Lock()
					batchRows = append(batchRows, operation.ID)
					mu.Unlock()
					if err := bulkBatch(ctx, db, repo, operation.ID, leadID, statuses[b%2]); err != nil {
						fail("batch: deadlock=%t %v", isDeadlock(err), err)
						return
					}
				}
			}(lead.ID)
		}
		for s := 0; s < sweepers; s++ {
			sweepWG.Add(1)
			go func() {
				defer sweepWG.Done()
				for i := 0; i < 500; i++ {
					// Each pass first adds stale rows, so the sweeps keep writing
					// while the batches run.
					more := make([]models.BulkOperation, 20)
					for j := range more {
						more[j] = f.row(models.StatusPending, cutoff.Add(-time.Minute))
					}
					if err := db.WithContext(ctx).Create(&more).Error; err != nil {
						fail("stale create: deadlock=%t %v", isDeadlock(err), err)
						return
					}
					mu.Lock()
					for _, row := range more {
						staleRows = append(staleRows, row.ID)
					}
					mu.Unlock()
					n, err := repo.MarkStalePendingFailed(ctx, cutoff)
					if err != nil {
						fail("sweep: deadlock=%t %v", isDeadlock(err), err)
						return
					}
					swept.Add(n)
					select {
					case <-batchesDone:
						return
					default:
					}
				}
			}()
		}
		batchWG.Wait()
		close(batchesDone)
		sweepWG.Wait()
		require.Empty(t, failures, "no batch, sweep or create failed (error 1213 included)")

		// One last sweep picks up the rows the final passes added after the
		// others had read their candidates.
		n, err := repo.MarkStalePendingFailed(ctx, cutoff)
		require.NoError(t, err)
		swept.Add(n)

		require.Len(t, batchRows, workers*batchesPerWorker)
		assert.EqualValues(t, len(batchRows), f.countStatus(batchRows, models.StatusCompleted), "no fresh batch row was failed")
		assert.EqualValues(t, len(existingFresh), f.countStatus(existingFresh, models.StatusPending), "no fresh row was failed")
		assert.EqualValues(t, len(staleRows), f.countStatus(staleRows, models.StatusFailed), "every stale row ends failed")
		assert.EqualValues(t, len(staleRows), swept.Load(), "the sweeps together counted every stale row once")
	})
}
