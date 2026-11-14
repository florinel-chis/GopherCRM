package repository

// Regression roots of #182 at the repository level: CompanyRepository.LockLive
// is the first lock of the shared order companies -> leads (id ascending) ->
// customers -> deals. On SQLite it is a plain read (the dialector drops the
// locking clause); on MySQL/MariaDB a `SELECT id … FOR UPDATE` by primary key
// that makes a second connection's LockLive on the same id wait — seen in
// information_schema.PROCESSLIST on two consecutive polls — until the first
// transaction ends. Missing and soft-deleted companies are
// gorm.ErrRecordNotFound. The MySQL roots use a disposable schema on
// TEST_MYSQL_DSN, skip without it and fail when it is unreachable.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const lockOrderRepoTimeout = 20 * time.Second

var lockOrderRepoSeq atomic.Uint64

// openLockOrderRepoDB opens a file-backed SQLite database with one pooled
// connection, or a disposable MySQL schema with maxConns connections, and
// migrates the tables of the lock order.
func openLockOrderRepoDB(t *testing.T, useMySQL bool, maxConns int) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{Logger: logger.Discard, DisableForeignKeyConstraintWhenMigrating: true}
	var db *gorm.DB
	if !useMySQL {
		dsn := filepath.Join(t.TempDir(), fmt.Sprintf("lock_order_%d.db", lockOrderRepoSeq.Add(1))) +
			"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
		var err error
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
		name := fmt.Sprintf("gocrm_lock_order_%d_%d", time.Now().UnixNano(), lockOrderRepoSeq.Add(1))
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
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Lead{}, &models.Customer{}, &models.Deal{}))
	return db
}

// lockOrderRecorder captures every statement a session runs.
type lockOrderRecorder struct {
	logger.Interface
	mu         sync.Mutex
	statements []string
}

func newLockOrderRecorder() *lockOrderRecorder {
	return &lockOrderRecorder{Interface: logger.Discard}
}

func (r *lockOrderRecorder) LogMode(logger.LogLevel) logger.Interface { return r }

func (r *lockOrderRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	statement, _ := fc()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, statement)
}

func (r *lockOrderRecorder) matching(fragment string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, statement := range r.statements {
		if strings.Contains(statement, fragment) {
			out = append(out, statement)
		}
	}
	return out
}

func (r *lockOrderRecorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.statements, "\n")
}

// lockOrderStatement is one row of information_schema.PROCESSLIST.
type lockOrderStatement struct {
	ID   int64
	Info string
}

// awaitLockOrderWait returns once a statement matching target is seen
// executing on the same connection on two consecutive PROCESSLIST polls (an
// uncontended statement finishes in microseconds, so one still running a poll
// later waits for a lock). PROCESSLIST exists on MySQL 8 and MariaDB 10.11.
func awaitLockOrderWait(t *testing.T, db *gorm.DB, target *regexp.Regexp) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), lockOrderRepoTimeout)
	defer cancel()
	var lastID int64
	var lastInfo string
	seen := 0
	for ctx.Err() == nil {
		var rows []lockOrderStatement
		err := db.WithContext(ctx).Raw(`SELECT ID AS id, INFO AS info FROM information_schema.PROCESSLIST
			WHERE DB = DATABASE() AND ID <> CONNECTION_ID() AND COMMAND IN ('Query', 'Execute')
			AND INFO IS NOT NULL`).Scan(&rows).Error
		if err != nil {
			rows = nil
		}
		var match *lockOrderStatement
		for i := range rows {
			if !target.MatchString(rows[i].Info) {
				continue
			}
			if match == nil || (rows[i].ID == lastID && rows[i].Info == lastInfo) {
				match = &rows[i]
			}
		}
		switch {
		case match == nil:
			seen, lastID, lastInfo = 0, 0, ""
		case seen > 0 && match.ID == lastID && match.Info == lastInfo:
			seen++
		default:
			seen, lastID, lastInfo = 1, match.ID, match.Info
		}
		if seen >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no statement matching %s waited on a lock", target)
}

// companyLockLiveStatement is LockLive's statement as PROCESSLIST shows it.
var companyLockLiveStatement = regexp.MustCompile("(?s)^SELECT `id` FROM `companies` WHERE .*FOR UPDATE$")

func seedLockLiveCompanies(t *testing.T, db *gorm.DB) (live, deleted models.Company) {
	t.Helper()
	seq := lockOrderRepoSeq.Add(1)
	live = models.Company{Name: "Lock Live", Domain: fmt.Sprintf("lock-live-%d.example", seq)}
	require.NoError(t, db.Create(&live).Error)
	deleted = models.Company{Name: "Lock Gone", Domain: fmt.Sprintf("lock-gone-%d.example", seq)}
	require.NoError(t, db.Create(&deleted).Error)
	require.NoError(t, db.Delete(&deleted).Error)
	return live, deleted
}

func TestCompanyRepositoryLockLiveSQLite(t *testing.T) {
	db := openLockOrderRepoDB(t, false, 1)
	live, deleted := seedLockLiveCompanies(t, db)

	recorder := newLockOrderRecorder()
	repo := NewCompanyRepository(db.Session(&gorm.Session{Logger: recorder}))

	require.NoError(t, repo.LockLive(live.ID), "a live company is found")
	assert.True(t, errors.Is(repo.LockLive(deleted.ID), gorm.ErrRecordNotFound), "a soft-deleted company is not found")
	assert.True(t, errors.Is(repo.LockLive(live.ID+deleted.ID+100), gorm.ErrRecordNotFound), "a missing company is not found")

	// Inside a transaction through WithTx, as the services call it.
	require.NoError(t, db.Session(&gorm.Session{Logger: recorder}).Transaction(func(tx *gorm.DB) error {
		return NewCompanyRepository(db).WithTx(tx).LockLive(live.ID)
	}))

	reads := recorder.matching("FROM `companies`")
	require.NotEmpty(t, reads, "the reads were captured:\n%s", recorder.all())
	assert.Empty(t, recorder.matching("FOR UPDATE"), "SQLite drops the locking clause:\n%s", recorder.all())
}

func TestCompanyRepositoryLockLiveMySQL(t *testing.T) {
	// One connection per transaction, one for the probe and the setup.
	db := openLockOrderRepoDB(t, true, 4)
	live, deleted := seedLockLiveCompanies(t, db)

	t.Run("captured statement and not-found", func(t *testing.T) {
		recorder := newLockOrderRecorder()
		repo := NewCompanyRepository(db.Session(&gorm.Session{Logger: recorder}))
		require.NoError(t, repo.LockLive(live.ID))
		assert.True(t, errors.Is(repo.LockLive(deleted.ID), gorm.ErrRecordNotFound))
		assert.True(t, errors.Is(repo.LockLive(live.ID+deleted.ID+100), gorm.ErrRecordNotFound))

		locking := recorder.matching("FOR UPDATE")
		require.Len(t, locking, 3, "every LockLive is a locking read:\n%s", recorder.all())
		for _, statement := range locking {
			assert.Regexp(t, companyLockLiveStatement, statement)
			assert.Contains(t, statement, "`companies`.`deleted_at` IS NULL")
			assert.NotContains(t, statement, "SELECT *", "only the primary key is read")
		}
	})

	// lockAndWait holds LockLive in one transaction, starts a second
	// transaction's LockLive on the same id, waits until PROCESSLIST shows it
	// blocked, then ends the first with finish. It returns the waiter's error.
	lockAndWait := func(t *testing.T, id uint, finish func(holder *gorm.DB) error) error {
		holder := db.Begin()
		require.NoError(t, holder.Error)
		defer func() { _ = holder.Rollback().Error }()
		require.NoError(t, NewCompanyRepository(db).WithTx(holder).LockLive(id))

		done := make(chan error, 1)
		go func() {
			waiter := db.Begin()
			if waiter.Error != nil {
				done <- waiter.Error
				return
			}
			err := NewCompanyRepository(db).WithTx(waiter).LockLive(id)
			_ = waiter.Rollback().Error
			done <- err
		}()

		awaitLockOrderWait(t, db, companyLockLiveStatement)
		select {
		case err := <-done:
			t.Fatalf("the second LockLive returned before the holder ended: %v", err)
		default:
		}
		require.NoError(t, finish(holder))
		select {
		case err := <-done:
			return err
		case <-time.After(lockOrderRepoTimeout):
			t.Fatal("the second LockLive never returned")
			return nil
		}
	}

	t.Run("a second LockLive waits until the holder rolls back", func(t *testing.T) {
		err := lockAndWait(t, live.ID, func(holder *gorm.DB) error { return holder.Rollback().Error })
		require.NoError(t, err, "the company is still live")
	})

	t.Run("a second LockLive waits for a delete and then finds nothing", func(t *testing.T) {
		victim := models.Company{Name: "Lock Victim", Domain: fmt.Sprintf("lock-victim-%d.example", lockOrderRepoSeq.Add(1))}
		require.NoError(t, db.Create(&victim).Error)
		err := lockAndWait(t, victim.ID, func(holder *gorm.DB) error {
			if err := NewCompanyRepository(db).WithTx(holder).Delete(victim.ID); err != nil {
				return err
			}
			return holder.Commit().Error
		})
		assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "the waiter sees the committed soft delete, got %v", err)
	})
}
