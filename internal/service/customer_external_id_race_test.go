package service

// A mapping create racing the erasure of its customer (#212). AddExternalID
// locks the live customer row and then inserts, in one transaction; every
// erasure of the customer scrubs (and so locks) the row before it deletes the
// customer's mappings. The race therefore ends in one of two states, never
// with a mapping on an erased customer: the create commits first and the
// erasure deletes its mapping, or the erasure commits first and the create
// answers ErrNotFound with nothing written.
//
// On SQLite the single pooled connection serialises the two, so both
// orderings are run in sequence (and once concurrently, where either outcome
// is acceptable). On MySQL both orderings are forced with GORM callbacks on
// the test's own handles, each wait is confirmed in
// information_schema.PROCESSLIST on two consecutive polls (lockWaitProbe), and
// no error 1213 may occur. The MySQL root uses a disposable schema on
// TEST_MYSQL_DSN, skips without it and fails when it is unreachable.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const externalIDRaceTimeout = 20 * time.Second

// The statements the MySQL orderings wait on. MariaDB can keep `?` in INFO, so
// no pattern depends on a parameter's value.
var (
	externalIDErasureScrub = regexp.MustCompile("^UPDATE `customers` SET ")
	externalIDCustomerLock = regexp.MustCompile("^SELECT `id` FROM `customers` .*FOR UPDATE$")
)

// externalIDGate parks the first caller until released.
type externalIDGate struct {
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

func newExternalIDGate() *externalIDGate {
	return &externalIDGate{reached: make(chan struct{}), release: make(chan struct{})}
}

func (g *externalIDGate) pause() {
	first := false
	g.once.Do(func() {
		first = true
		close(g.reached)
	})
	if !first {
		return
	}
	select {
	case <-g.release:
	case <-time.After(externalIDRaceTimeout):
	}
}

func (g *externalIDGate) awaitReached(t *testing.T) {
	t.Helper()
	select {
	case <-g.reached:
	case <-time.After(externalIDRaceTimeout):
		t.Fatal("the transaction never reached the gate")
	}
}

func assertNoMappingOnErasedCustomer(t *testing.T, db *gorm.DB) {
	t.Helper()
	var orphans int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM customer_external_ids m JOIN customers c ON c.id = m.customer_id
		WHERE c.deleted_at IS NOT NULL`).Scan(&orphans).Error)
	assert.Zero(t, orphans, "no mapping row on an erased customer")
}

func externalIDMappings(t *testing.T, db *gorm.DB, customerID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&models.CustomerExternalID{}).Where("customer_id = ?", customerID).Count(&n).Error)
	return n
}

func assertCustomerErased(t *testing.T, db *gorm.DB, customerID uint) {
	t.Helper()
	var c models.Customer
	require.NoError(t, db.Unscoped().First(&c, customerID).Error)
	assert.True(t, c.DeletedAt.Valid, "the customer is erased")
}

func TestCustomerExternalIDCreateRacesErasureSQLite(t *testing.T) {
	db := openExternalIDSQLite(t)
	svc := NewCustomerExternalIDService(repository.NewCustomerExternalIDRepository(db), utils.NewTransactionManager(db))
	ctx := context.Background()

	t.Run("create_commits_first", func(t *testing.T) {
		customer := seedExternalIDServiceCustomer(t, db, "race-create-first")
		mapping, err := svc.AddExternalID(ctx, customer.ID, "race", "", "create-first")
		require.NoError(t, err)
		require.NotZero(t, mapping.ID)
		require.NoError(t, repository.NewCustomerRepository(db).Delete(customer.ID))
		assertCustomerErased(t, db, customer.ID)
		assert.Zero(t, externalIDMappings(t, db, customer.ID), "the erasure removed the mapping")
		assertNoMappingOnErasedCustomer(t, db)
	})

	t.Run("erasure_commits_first", func(t *testing.T) {
		customer := seedExternalIDServiceCustomer(t, db, "race-erase-first")
		require.NoError(t, repository.NewCustomerRepository(db).Delete(customer.ID))
		mapping, err := svc.AddExternalID(ctx, customer.ID, "race", "", "erase-first")
		assert.Nil(t, mapping)
		require.ErrorIs(t, err, apperrors.ErrNotFound)
		assert.Zero(t, externalIDMappings(t, db, customer.ID), "nothing is written")
		assertNoMappingOnErasedCustomer(t, db)
	})

	t.Run("concurrent", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			customer := seedExternalIDServiceCustomer(t, db, fmt.Sprintf("race-concurrent-%d", i))
			var wg sync.WaitGroup
			var createErr, eraseErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, createErr = svc.AddExternalID(ctx, customer.ID, "race", "", fmt.Sprintf("concurrent-%d", i))
			}()
			go func() {
				defer wg.Done()
				eraseErr = repository.NewCustomerRepository(db).Delete(customer.ID)
			}()
			wg.Wait()
			require.NoError(t, eraseErr)
			if createErr != nil {
				require.ErrorIs(t, createErr, apperrors.ErrNotFound, "the only refusal is a customer already erased")
			}
			assertCustomerErased(t, db, customer.ID)
			assert.Zero(t, externalIDMappings(t, db, customer.ID))
		}
		assertNoMappingOnErasedCustomer(t, db)
	})
}

// openExternalIDMySQL creates a uniquely named disposable schema with the full
// application schema and returns handles to it, each with its own pool and
// its own callbacks.
func openExternalIDMySQL(t *testing.T, handles int) []*gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	cfg, err := mysqldriver.ParseDSN(raw)
	require.NoError(t, err)
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, externalIDRaceTimeout, externalIDRaceTimeout
	server, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	name := fmt.Sprintf("gocrm_extid_race_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "`")
	require.NoError(t, err, "TEST_MYSQL_DSN is set but the server is unusable")
	cfg.DBName = name

	out := make([]*gorm.DB, 0, handles)
	for i := 0; i < handles; i++ {
		db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Discard})
		require.NoError(t, err)
		pool, err := db.DB()
		require.NoError(t, err)
		pool.SetMaxOpenConns(3)
		t.Cleanup(func() { _ = pool.Close() })
		out = append(out, db)
	}
	migrateExternalIDSchema(t, out[0])
	return out
}

// awaitExternalIDLockWait returns once a statement matching target is seen
// executing on the same connection on two consecutive PROCESSLIST polls.
func awaitExternalIDLockWait(t *testing.T, db *gorm.DB, target *regexp.Regexp) {
	t.Helper()
	probe := &lockWaitProbe{target: target}
	deadline := time.Now().Add(externalIDRaceTimeout)
	for time.Now().Before(deadline) {
		var rows []executingStatement
		err := db.Raw(`SELECT ID AS id, INFO AS info FROM information_schema.PROCESSLIST
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

func assertNotDeadlock(t *testing.T, err error) {
	t.Helper()
	var driverErr *mysqldriver.MySQLError
	if errors.As(err, &driverErr) {
		assert.NotEqual(t, uint16(1213), driverErr.Number, "no deadlock")
	}
}

func TestCustomerExternalIDCreateRacesErasureMySQL(t *testing.T) {
	handles := openExternalIDMySQL(t, 3)
	createDB, eraseDB, probeDB := handles[0], handles[1], handles[2]
	svc := NewCustomerExternalIDService(repository.NewCustomerExternalIDRepository(createDB), utils.NewTransactionManager(createDB))
	eraser := repository.NewCustomerRepository(eraseDB)

	var mu sync.Mutex
	var beforeInsert, afterPurge *externalIDGate
	require.NoError(t, createDB.Callback().Create().Before("gorm:create").Register("extid_race:before_insert", func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "customer_external_ids" {
			return
		}
		mu.Lock()
		gate := beforeInsert
		mu.Unlock()
		if gate != nil {
			gate.pause()
		}
	}))
	require.NoError(t, eraseDB.Callback().Delete().After("gorm:delete").Register("extid_race:after_purge", func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "customer_external_ids" {
			return
		}
		mu.Lock()
		gate := afterPurge
		mu.Unlock()
		if gate != nil {
			gate.pause()
		}
	}))

	t.Run("create_commits_first", func(t *testing.T) {
		customer := seedExternalIDServiceCustomer(t, createDB, "mysql-create-first")
		gate := newExternalIDGate()
		mu.Lock()
		beforeInsert = gate
		mu.Unlock()
		t.Cleanup(func() {
			mu.Lock()
			beforeInsert = nil
			mu.Unlock()
		})

		createDone := make(chan error, 1)
		var created *models.CustomerExternalID
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), externalIDRaceTimeout)
			defer cancel()
			mapping, err := svc.AddExternalID(ctx, customer.ID, "race", "", "mysql-create-first")
			created = mapping
			createDone <- err
		}()
		// The create holds the customer row lock and is parked before its insert.
		gate.awaitReached(t)

		eraseDone := make(chan error, 1)
		go func() { eraseDone <- eraser.Delete(customer.ID) }()
		// The erasure's scrub of the customer row waits for that lock.
		awaitExternalIDLockWait(t, probeDB, externalIDErasureScrub)
		close(gate.release)

		var createErr, eraseErr error
		select {
		case createErr = <-createDone:
		case <-time.After(externalIDRaceTimeout):
			t.Fatal("the create never finished")
		}
		select {
		case eraseErr = <-eraseDone:
		case <-time.After(externalIDRaceTimeout):
			t.Fatal("the erasure never finished")
		}
		assertNotDeadlock(t, createErr)
		assertNotDeadlock(t, eraseErr)
		require.NoError(t, createErr, "the create committed first")
		require.NotNil(t, created)
		require.NoError(t, eraseErr)
		assertCustomerErased(t, createDB, customer.ID)
		assert.Zero(t, externalIDMappings(t, createDB, customer.ID), "the erasure removed the mapping the create committed")
		assertNoMappingOnErasedCustomer(t, createDB)
	})

	t.Run("erasure_commits_first", func(t *testing.T) {
		customer := seedExternalIDServiceCustomer(t, createDB, "mysql-erase-first")
		gate := newExternalIDGate()
		mu.Lock()
		afterPurge = gate
		mu.Unlock()
		t.Cleanup(func() {
			mu.Lock()
			afterPurge = nil
			mu.Unlock()
		})

		eraseDone := make(chan error, 1)
		go func() { eraseDone <- eraser.Delete(customer.ID) }()
		// The erasure has scrubbed (locked) the customer and deleted its
		// mappings, and is parked before its commit.
		gate.awaitReached(t)

		createDone := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), externalIDRaceTimeout)
			defer cancel()
			_, err := svc.AddExternalID(ctx, customer.ID, "race", "", "mysql-erase-first")
			createDone <- err
		}()
		// The create's locking read of the customer waits for the erasure.
		awaitExternalIDLockWait(t, probeDB, externalIDCustomerLock)
		close(gate.release)

		var createErr, eraseErr error
		select {
		case eraseErr = <-eraseDone:
		case <-time.After(externalIDRaceTimeout):
			t.Fatal("the erasure never finished")
		}
		select {
		case createErr = <-createDone:
		case <-time.After(externalIDRaceTimeout):
			t.Fatal("the create never finished")
		}
		assertNotDeadlock(t, createErr)
		assertNotDeadlock(t, eraseErr)
		require.NoError(t, eraseErr, "the erasure committed first")
		require.ErrorIs(t, createErr, apperrors.ErrNotFound, "the create finds no live customer")
		assertCustomerErased(t, createDB, customer.ID)
		assert.Zero(t, externalIDMappings(t, createDB, customer.ID), "nothing is written")
		assertNoMappingOnErasedCustomer(t, createDB)
	})
}
