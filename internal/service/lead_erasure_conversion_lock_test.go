package service

// Regression roots of #337: lead-rooted erasure (LeadService.Delete, admin and
// non-admin) against LeadService.ConvertToCustomer of the same lead. The
// erasure cancels the lead's automation runs, then locks the lead row by
// primary key and decides the refusal and the graph on that row; the
// conversion locks the same row (companies -> leads -> customers). The MySQL
// roots park one party through an instance-local seam (the lead service's
// leadCompanyLockHooks.beforeCommit, or the erasure hooks of a lead repository
// built with repository.WithLeadErasureHooks), see the other party's wait in
// information_schema.PROCESSLIST on two consecutive polls, and release; they
// run on a disposable TEST_MYSQL_DSN schema, skip without it and fail when it
// is unreachable. The SQLite root runs the same outcomes in sequence.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var (
	// erasureLeadLock is the erasure's locking read of the lead (lockGraph).
	erasureLeadLock = regexp.MustCompile("(?s)^SELECT `id`,`status`,`customer_id` FROM `leads` WHERE .*FOR UPDATE$")
	// conversionLeadLock is the conversion's lockedLeadRead.
	conversionLeadLock = regexp.MustCompile("(?s)^SELECT \\* FROM `leads` WHERE .*FOR UPDATE$")
	// conversionStatements are the statements of a conversion that could wait
	// on a lock the erasure holds.
	conversionStatements = regexp.MustCompile("(?s)^(SELECT .*FROM `leads` .*FOR UPDATE$|INSERT INTO `customers`|UPDATE `leads` SET|INSERT INTO `automation_events`)")
)

// leadErasureRoles are the two erasure paths of LeadService.Delete.
var leadErasureRoles = []struct {
	name  string
	role  models.UserRole
	admin bool
}{
	{"admin", models.RoleAdmin, true},
	{"non-admin", models.RoleSales, false},
}

// seedPendingLeadRun inserts one pending automation run of the lead.
func seedPendingLeadRun(t *testing.T, db *gorm.DB, leadID uint) *models.AutomationRun {
	t.Helper()
	run := &models.AutomationRun{
		RuleID:       1,
		RuleVersion:  1,
		EntityType:   models.AutomationEntityLead,
		EntityID:     leadID,
		DedupeKey:    fmt.Sprintf("lead-erasure-conversion:%d:%d", leadID, automationTestSeq.Add(1)),
		Status:       models.AutomationRunPending,
		NextPosition: 1,
	}
	require.NoError(t, db.Create(run).Error)
	return run
}

// withLeadErasureHooks makes the fixture's lead service erase through a lead
// repository that carries hooks. Only Delete runs them.
func withLeadErasureHooks(f *leadCompanyFixture, hooks repository.LeadErasureHooks) {
	f.leadSvc.leadRepo = repository.NewLeadRepository(f.db, repository.WithLeadErasureHooks(hooks))
}

// leadState is what a refused erasure must leave unchanged.
type leadState struct {
	Email      string
	FirstName  string
	LastName   string
	Phone      string
	Notes      string
	Status     models.LeadStatus
	CustomerID *uint
	Deleted    bool
}

func storedLeadState(f *leadCompanyFixture, id uint) leadState {
	lead := f.storedLead(id)
	return leadState{lead.Email, lead.FirstName, lead.LastName, lead.Phone, lead.Notes, lead.Status, lead.CustomerID, lead.DeletedAt.Valid}
}

func runStatus(t *testing.T, db *gorm.DB, id uint) string {
	t.Helper()
	var run models.AutomationRun
	require.NoError(t, db.First(&run, id).Error)
	return run.Status
}

// assertLeadAndCustomerErased checks that the lead and the customer are
// scrubbed and soft-deleted and that no live customer is reachable from the
// lead.
func assertLeadAndCustomerErased(t *testing.T, f *leadCompanyFixture, leadID, customerID uint) {
	t.Helper()
	lead := f.storedLead(leadID)
	assert.True(t, lead.DeletedAt.Valid, "the lead is soft-deleted")
	assert.Empty(t, lead.FirstName, "the lead is scrubbed")
	assert.True(t, strings.HasSuffix(lead.Email, "@anonymized.invalid"), "the lead's address is a placeholder")
	customer := f.storedCustomer(customerID)
	assert.True(t, customer.DeletedAt.Valid, "the customer is soft-deleted")
	assert.Empty(t, customer.FirstName, "the customer is scrubbed")
	assert.True(t, strings.HasSuffix(customer.Email, "@anonymized.invalid"), "the customer's address is a placeholder")
	if lead.CustomerID != nil {
		var live int64
		require.NoError(t, f.db.Model(&models.Customer{}).Where("id = ?", *lead.CustomerID).Count(&live).Error)
		assert.Zero(t, live, "no live customer is reachable from the lead")
	}
}

// assertConvertedUnchanged checks the state a refused non-admin erasure of a
// converted lead leaves: the lead and its customer live and unscrubbed, and
// the lead's pending run still pending.
func assertConvertedUnchanged(t *testing.T, f *leadCompanyFixture, lead *models.Lead, customer *models.Customer, run *models.AutomationRun) {
	t.Helper()
	stored := f.storedLead(lead.ID)
	assert.False(t, stored.DeletedAt.Valid, "the lead is live")
	assert.Equal(t, lead.Email, stored.Email, "the lead is unscrubbed")
	assert.Equal(t, automationCanaryFirstName, stored.FirstName)
	assert.Equal(t, models.LeadStatusConverted, stored.Status)
	require.NotNil(t, stored.CustomerID)
	assert.Equal(t, customer.ID, *stored.CustomerID)
	storedCustomer := f.storedCustomer(customer.ID)
	assert.False(t, storedCustomer.DeletedAt.Valid, "the customer is live")
	assert.Equal(t, lead.Email, storedCustomer.Email, "the customer is unscrubbed")
	assert.Equal(t, automationCanaryFirstName, storedCustomer.FirstName)
	assert.Equal(t, models.AutomationRunPending, runStatus(t, f.db, run.ID), "the lead's run is still pending")
}

func assertRefusedForNonAdmin(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.ErrForbidden)
	assert.ErrorIs(t, err, repository.ErrConvertedLeadNeedsAdmin)
}

// conversionEvents returns the lead's events of origin conversion.
func conversionEvents(f *leadCompanyFixture, leadID uint) []models.AutomationEvent {
	var out []models.AutomationEvent
	for _, event := range f.env.eventsOf(models.AutomationEntityLead, leadID) {
		if event.Origin == models.AutomationOriginConversion &&
			(event.EventType == models.AutomationEventLeadUpdated || event.EventType == models.AutomationEventLeadStatusChanged) {
			out = append(out, event)
		}
	}
	return out
}

// TestLeadErasureConversionFirstMySQL parks a conversion in its beforeCommit
// seam, holding the lead row lock with its customer inserted; the erasure
// waits on its locking read of the lead, and once the conversion commits it
// sees the new customer.
func TestLeadErasureConversionFirstMySQL(t *testing.T) {
	for _, tc := range leadErasureRoles {
		t.Run(tc.name, func(t *testing.T) {
			f := newLeadCompanyFixture(t, openAutomationMySQL(t, 6))
			lead := f.lead(nil)
			run := seedPendingLeadRun(t, f.db, lead.ID)

			gate := newParkGate()
			f.leadSvc.lockHooks = leadCompanyLockHooks{beforeCommit: func(op string, id uint) {
				if op == leadCompanyOpConvert && id == lead.ID {
					gate.park()
				}
			}}
			t.Cleanup(func() { f.leadSvc.lockHooks = leadCompanyLockHooks{} })

			var customer *models.Customer
			convertDone := make(chan error, 1)
			go func() {
				var err error
				customer, err = f.leadSvc.ConvertToCustomer(context.Background(), lead.ID, &models.Customer{})
				convertDone <- err
			}()
			gate.awaitReached(t, "conversion")

			eraseDone := make(chan error, 1)
			go func() { eraseDone <- f.leadSvc.Delete(lead.ID, tc.role) }()
			awaitLeadCompanyLockWait(t, f.db, erasureLeadLock)
			close(gate.release)

			convertErr := awaitDone(t, "conversion", convertDone)
			eraseErr := awaitDone(t, "erasure", eraseDone)
			assertNotDeadlock(t, convertErr)
			assertNotDeadlock(t, eraseErr)
			require.NoError(t, convertErr, "the conversion commits")
			require.NotNil(t, customer)

			if tc.admin {
				require.NoError(t, eraseErr, "the admin erasure commits")
				assertLeadAndCustomerErased(t, f, lead.ID, customer.ID)
				assert.Equal(t, models.AutomationRunCancelled, runStatus(t, f.db, run.ID))
				return
			}
			assertRefusedForNonAdmin(t, eraseErr)
			assertConvertedUnchanged(t, f, lead, customer, run)
		})
	}
}

// TestLeadErasureErasureFirstMySQL parks an erasure right after its locking
// read of the lead (AfterLeadLock); the conversion waits on its
// lockedLeadRead, and once the erasure commits it finds the lead gone.
func TestLeadErasureErasureFirstMySQL(t *testing.T) {
	for _, tc := range leadErasureRoles {
		t.Run(tc.name, func(t *testing.T) {
			f := newLeadCompanyFixture(t, openAutomationMySQL(t, 6))
			lead := f.lead(nil)
			customersBefore := f.customerCount()

			gate := newParkGate()
			withLeadErasureHooks(f, repository.LeadErasureHooks{AfterLeadLock: func(id uint) {
				if id == lead.ID {
					gate.park()
				}
			}})

			eraseDone := make(chan error, 1)
			go func() { eraseDone <- f.leadSvc.Delete(lead.ID, tc.role) }()
			gate.awaitReached(t, "erasure")

			convertDone := make(chan error, 1)
			go func() {
				_, err := f.leadSvc.ConvertToCustomer(context.Background(), lead.ID, &models.Customer{})
				convertDone <- err
			}()
			awaitLeadCompanyLockWait(t, f.db, conversionLeadLock)
			close(gate.release)

			eraseErr := awaitDone(t, "erasure", eraseDone)
			convertErr := awaitDone(t, "conversion", convertDone)
			assertNotDeadlock(t, eraseErr)
			assertNotDeadlock(t, convertErr)
			require.NoError(t, eraseErr, "the erasure commits")
			require.Error(t, convertErr)
			assert.True(t, apperrors.IsNotFound(convertErr), "the conversion finds the lead gone (404), got %v", convertErr)

			assert.Equal(t, customersBefore, f.customerCount(), "no customer is inserted")
			stored := f.storedLead(lead.ID)
			assert.True(t, stored.DeletedAt.Valid, "the lead is erased")
			assert.Equal(t, models.LeadStatusQualified, stored.Status, "the status is unchanged")
			assert.Nil(t, stored.CustomerID, "customer_id is unchanged")
			assert.Empty(t, conversionEvents(f, lead.ID), "no automation event of origin conversion")
		})
	}
}

// TestLeadErasureConversionCommittedAfterCancelMySQL is the stale-snapshot
// case: the erasure is parked after its automation cancel and before its lead
// lock, and a conversion of the same lead commits in between without waiting.
// The erasure must see it.
//
// Before parking, the hook issues a plain (consistent, non-locking) read of
// the lead on the erasure's own transaction, as the pre-#337 discovery read
// did. On InnoDB REPEATABLE READ that read opens the unit's snapshot; nothing
// before it does (the HasTable probes, the automation_runs UPDATE and the
// notifications DELETE establish no read view). The conversion therefore
// commits after the snapshot, and after the release the hook proves the
// snapshot is still stale: a plain read of the new customer by id on the same
// transaction finds nothing. Only the erasure's locking reads (the lead,
// readErasedEmail, eraseCustomer's customer read) see past it, so with those
// reads reverted to plain reads the admin case leaves the new customer live
// and this root fails.
func TestLeadErasureConversionCommittedAfterCancelMySQL(t *testing.T) {
	for _, tc := range leadErasureRoles {
		t.Run(tc.name, func(t *testing.T) {
			f := newLeadCompanyFixture(t, openAutomationMySQL(t, 6))
			lead := f.lead(nil)
			run := seedPendingLeadRun(t, f.db, lead.ID)

			var (
				snapshotLead     models.Lead
				snapshotErr      error
				newCustomerID    uint
				staleCustomers   int64 = -1
				staleCustomerErr error
			)
			gate := newParkGate()
			var firstCancel atomic.Bool
			firstCancel.Store(true)
			withLeadErasureHooks(f, repository.LeadErasureHooks{AfterAutomationCancel: func(tx *gorm.DB, id uint) {
				// Only the first transaction of the erasure is observed: the
				// admin erasure finds the graph changed under its lead lock and
				// starts over from a new discovery (#385), which runs this hook
				// again on a transaction that began after the conversion.
				if id != lead.ID || !firstCancel.CompareAndSwap(true, false) {
					return
				}
				// The pre-#337 discovery read: plain, so it opens the snapshot.
				snapshotErr = tx.Unscoped().Select("id", "customer_id").First(&snapshotLead, id).Error
				gate.park()
				// newCustomerID was written before the release (happens-before
				// through the gate's channel).
				staleCustomerErr = tx.Unscoped().Model(&models.Customer{}).
					Where("id = ?", newCustomerID).Count(&staleCustomers).Error
			}})

			eraseDone := make(chan error, 1)
			go func() { eraseDone <- f.leadSvc.Delete(lead.ID, tc.role) }()
			gate.awaitReached(t, "erasure")

			// The conversion runs to commit without waiting on the erasure.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			customer, convertErr := f.leadSvc.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
			cancel()
			if convertErr != nil || customer == nil {
				close(gate.release)
				_ = awaitDone(t, "erasure", eraseDone)
			}
			assertNotDeadlock(t, convertErr)
			require.NoError(t, convertErr, "the conversion commits while the erasure is parked")
			require.NotNil(t, customer)
			newCustomerID = customer.ID

			close(gate.release)
			eraseErr := awaitDone(t, "erasure", eraseDone)
			assertNotDeadlock(t, eraseErr)

			// The snapshot was opened before the conversion and is still stale
			// when the erasure resumes: this is what makes the root fail
			// without the locking discovery reads.
			require.NoError(t, snapshotErr, "the snapshot-opening read")
			assert.Nil(t, snapshotLead.CustomerID, "the snapshot was taken before the conversion committed")
			require.NoError(t, staleCustomerErr, "the stale-snapshot probe")
			assert.Zero(t, staleCustomers, "a plain read in the erasure unit misses the new customer (the snapshot is stale)")

			if tc.admin {
				require.NoError(t, eraseErr)
				assertLeadAndCustomerErased(t, f, lead.ID, customer.ID)
				assert.Equal(t, models.AutomationRunCancelled, runStatus(t, f.db, run.ID))
				return
			}
			assertRefusedForNonAdmin(t, eraseErr)
			assertConvertedUnchanged(t, f, lead, customer, run)
		})
	}
}

// TestLeadErasureVsConversionSQLite runs the outcomes in sequence on SQLite's
// single connection, on the real service and repositories.
func TestLeadErasureVsConversionSQLite(t *testing.T) {
	f := newLeadCompanyFixture(t, openAutomationSQLite(t))
	ctx := context.Background()

	t.Run("(a) conversion, then admin erasure erases the customer too", func(t *testing.T) {
		lead := f.lead(nil)
		customer, err := f.leadSvc.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
		require.NoError(t, err)
		require.NoError(t, f.leadSvc.Delete(lead.ID, models.RoleAdmin))
		assertLeadAndCustomerErased(t, f, lead.ID, customer.ID)
	})

	t.Run("(b) conversion, then non-admin erasure is refused with nothing changed", func(t *testing.T) {
		lead := f.lead(nil)
		run := seedPendingLeadRun(t, f.db, lead.ID)
		customer, err := f.leadSvc.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
		require.NoError(t, err)
		before := storedLeadState(f, lead.ID)
		assertRefusedForNonAdmin(t, f.leadSvc.Delete(lead.ID, models.RoleSales))
		assert.Equal(t, before, storedLeadState(f, lead.ID))
		assertConvertedUnchanged(t, f, lead, customer, run)
	})

	for _, tc := range leadErasureRoles {
		t.Run("(c) "+tc.name+" erasure, then conversion is not found", func(t *testing.T) {
			lead := f.lead(nil)
			require.NoError(t, f.leadSvc.Delete(lead.ID, tc.role))
			customersBefore := f.customerCount()
			_, err := f.leadSvc.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
			require.Error(t, err)
			assert.True(t, apperrors.IsNotFound(err), "got %v", err)
			assert.Equal(t, customersBefore, f.customerCount(), "no customer is inserted")
			assert.Empty(t, conversionEvents(f, lead.ID))
		})
	}

	for _, tc := range leadErasureRoles {
		t.Run("(d) conversion committed after the pre-read, "+tc.name+" erasure", func(t *testing.T) {
			lead := f.lead(nil)
			run := seedPendingLeadRun(t, f.db, lead.ID)
			var customer *models.Customer
			var convertErr error
			f.leadSvc.afterDeletePreRead = func(id uint) {
				if id == lead.ID {
					customer, convertErr = f.leadSvc.ConvertToCustomer(ctx, id, &models.Customer{})
				}
			}
			t.Cleanup(func() { f.leadSvc.afterDeletePreRead = nil })

			eraseErr := f.leadSvc.Delete(lead.ID, tc.role)
			require.NoError(t, convertErr, "the conversion committed between the pre-read and the erasure")
			require.NotNil(t, customer)
			if tc.admin {
				require.NoError(t, eraseErr)
				assertLeadAndCustomerErased(t, f, lead.ID, customer.ID)
				assert.Equal(t, models.AutomationRunCancelled, runStatus(t, f.db, run.ID))
				return
			}
			assertRefusedForNonAdmin(t, eraseErr)
			assertConvertedUnchanged(t, f, lead, customer, run)
		})
	}
}

// TestLeadErasureUnrelatedConversionNotBlockedMySQL holds an admin erasure of
// a converted lead open, with all its locks, in a caller transaction and
// converts another, unconverted lead meanwhile: the conversion never waits.
func TestLeadErasureUnrelatedConversionNotBlockedMySQL(t *testing.T) {
	f := newLeadCompanyFixture(t, openAutomationMySQL(t, 6))
	ctx := context.Background()

	first := f.lead(nil)
	firstCustomer, err := f.leadSvc.ConvertToCustomer(ctx, first.ID, &models.Customer{})
	require.NoError(t, err)
	second := f.lead(nil)

	tx := f.db.Begin()
	require.NoError(t, tx.Error)
	committed := false
	t.Cleanup(func() {
		if !committed {
			_ = tx.Rollback()
		}
	})
	require.NoError(t, repository.NewLeadRepository(f.db).WithTx(tx).Delete(first.ID), "the erasure returns inside the open caller transaction")

	// Watch PROCESSLIST while the conversion runs: none of its statements may
	// be seen executing on two consecutive polls.
	stop := make(chan struct{})
	waited := make(chan bool, 1)
	go func() {
		probe := &lockWaitProbe{target: conversionStatements}
		seen := false
		for {
			select {
			case <-stop:
				waited <- seen
				return
			default:
			}
			var rows []executingStatement
			if err := f.db.Raw(`SELECT ID AS id, INFO AS info FROM information_schema.PROCESSLIST
				WHERE DB = DATABASE() AND ID <> CONNECTION_ID() AND COMMAND IN ('Query', 'Execute')
				AND INFO IS NOT NULL`).Scan(&rows).Error; err == nil && probe.observe(rows) {
				seen = true
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()

	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	secondCustomer, convertErr := f.leadSvc.ConvertToCustomer(bounded, second.ID, &models.Customer{})
	cancel()
	close(stop)
	sawWait := <-waited

	assertNotDeadlock(t, convertErr)
	require.NoError(t, convertErr, "the unrelated conversion commits within 5 s")
	require.NotNil(t, secondCustomer)
	assert.False(t, sawWait, "the unrelated conversion never waited on a lock")

	require.NoError(t, tx.Commit().Error)
	committed = true

	assertLeadAndCustomerErased(t, f, first.ID, firstCustomer.ID)
	stored := f.storedLead(second.ID)
	assert.False(t, stored.DeletedAt.Valid)
	assert.Equal(t, models.LeadStatusConverted, stored.Status)
	require.NotNil(t, stored.CustomerID)
	assert.Equal(t, secondCustomer.ID, *stored.CustomerID)
	assert.False(t, f.storedCustomer(secondCustomer.ID).DeletedAt.Valid, "the second lead's customer is live")
}
