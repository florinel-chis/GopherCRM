package service

// MySQL/MariaDB barrier roots of #182: lead conversion and lead update against
// a company delete, in both orderings, on a disposable TEST_MYSQL_DSN schema
// (skipped without the variable, failing when it is unreachable). The first
// party is parked through an instance-local seam while it holds its locks; the
// second party's wait on the companies row lock is seen in
// information_schema.PROCESSLIST on two consecutive polls (MySQL 8 and
// MariaDB 10.11 alike) before the first is released. An instance-local
// attempt counter on the lead service's transaction callback gives the exact
// number of attempts and how each ended.

import (
	"context"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// companyLockLiveRead is CompanyRepository.LockLive as PROCESSLIST shows it.
var companyLockLiveRead = regexp.MustCompile("(?s)^SELECT `id` FROM `companies` WHERE .*FOR UPDATE$")

// awaitLeadCompanyLockWait returns once a statement matching target is seen
// executing on the same connection on two consecutive PROCESSLIST polls.
func awaitLeadCompanyLockWait(t *testing.T, db *gorm.DB, target *regexp.Regexp) {
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

// parkGate parks the first caller of park until release is closed; later
// callers pass through.
type parkGate struct {
	armed   atomic.Bool
	reached chan struct{}
	release chan struct{}
}

func newParkGate() *parkGate {
	g := &parkGate{reached: make(chan struct{}), release: make(chan struct{})}
	g.armed.Store(true)
	return g
}

func (g *parkGate) park() {
	if !g.armed.CompareAndSwap(true, false) {
		return
	}
	close(g.reached)
	<-g.release
}

func (g *parkGate) awaitReached(t *testing.T, who string) {
	t.Helper()
	select {
	case <-g.reached:
	case <-time.After(automationTestTimeout):
		t.Fatalf("the %s never reached its park point", who)
	}
}

func awaitDone(t *testing.T, who string, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(automationTestTimeout):
		t.Fatalf("the %s never finished", who)
		return nil
	}
}

// raceLeadWriterAgainstDelete parks the first party, starts the second, waits
// until PROCESSLIST shows the second blocked on the company lock, releases the
// first and returns both errors. write runs the lead write; the lead service's
// beforeCommit seam parks it when it goes first.
func raceLeadWriterAgainstDelete(t *testing.T, f *leadCompanyFixture, company *models.Company, deleteFirst bool, log *attemptLog, write func() error) (writeErr, deleteErr error) {
	t.Helper()
	gate := newParkGate()
	hooks := log.hooks()
	writeDone := make(chan error, 1)
	deleteDone := make(chan error, 1)
	if deleteFirst {
		f.companySvc.afterDeleteUnlink = func(uint) { gate.park() }
		f.leadSvc.lockHooks = hooks
		go func() { deleteDone <- f.companySvc.Delete(company.ID) }()
		gate.awaitReached(t, "delete")
		go func() { writeDone <- write() }()
	} else {
		hooks.beforeCommit = func(string, uint) { gate.park() }
		f.leadSvc.lockHooks = hooks
		go func() { writeDone <- write() }()
		gate.awaitReached(t, "lead write")
		go func() { deleteDone <- f.companySvc.Delete(company.ID) }()
	}
	t.Cleanup(func() {
		f.companySvc.afterDeleteUnlink = nil
		f.leadSvc.lockHooks = leadCompanyLockHooks{}
	})

	awaitLeadCompanyLockWait(t, f.db, companyLockLiveRead)
	close(gate.release)
	writeErr = awaitDone(t, "lead write", writeDone)
	deleteErr = awaitDone(t, "delete", deleteDone)
	assertNotDeadlock(t, writeErr)
	assertNotDeadlock(t, deleteErr)
	return writeErr, deleteErr
}

func TestLeadConversionVsCompanyDeleteMySQL(t *testing.T) {
	for _, deleteFirst := range []bool{false, true} {
		name := map[bool]string{false: "convert first", true: "delete first"}[deleteFirst]
		t.Run(name, func(t *testing.T) {
			// Conversion, delete, probe and verification connections.
			f := newLeadCompanyFixture(t, openAutomationMySQL(t, 6))
			company := f.company()
			lead := f.lead(company)
			log := &attemptLog{}

			var customer *models.Customer
			convertErr, deleteErr := raceLeadWriterAgainstDelete(t, f, company, deleteFirst, log, func() error {
				var err error
				customer, err = f.leadSvc.ConvertToCustomer(context.Background(), lead.ID, &models.Customer{})
				return err
			})
			require.NoError(t, deleteErr, "the delete succeeds")
			require.NoError(t, convertErr, "the conversion succeeds")
			require.NotNil(t, customer)

			if deleteFirst {
				assert.Equal(t, 2, log.attempts(), "one restart after the delete unlinked the lead")
				assert.Equal(t, 1, log.moved(), "exactly one attempt ended by errLeadCompanyMoved")
			} else {
				assert.Equal(t, 1, log.attempts())
				assert.Equal(t, 0, log.moved())
			}
			assert.Equal(t, 0, log.retryable(), "no attempt ended by error 1213 or 1205")

			assert.Nil(t, f.storedCustomer(customer.ID).CompanyID, "the customer is not linked to the deleted company")
			stored := f.storedLead(lead.ID)
			assert.Nil(t, stored.CompanyID)
			assert.Equal(t, models.LeadStatusConverted, stored.Status)
			var gone models.Company
			require.NoError(t, f.db.Unscoped().First(&gone, company.ID).Error)
			assert.True(t, gone.DeletedAt.Valid)
			f.assertInvariant()
		})
	}
}

func TestLeadUpdateVsCompanyDeleteMySQL(t *testing.T) {
	t.Run("update setting company_id", func(t *testing.T) {
		for _, deleteFirst := range []bool{true, false} {
			name := map[bool]string{false: "update first", true: "delete first"}[deleteFirst]
			t.Run(name, func(t *testing.T) {
				f := newLeadCompanyFixture(t, openAutomationMySQL(t, 6))
				company := f.company()
				lead := f.lead(nil)
				log := &attemptLog{}

				updateErr, deleteErr := raceLeadWriterAgainstDelete(t, f, company, deleteFirst, log, func() error {
					_, err := f.leadSvc.Update(lead.ID, map[string]interface{}{"company_id": &company.ID, "notes": "linked " + name})
					return err
				})
				require.NoError(t, deleteErr, "the delete succeeds")
				assert.Equal(t, 1, log.attempts())
				assert.Equal(t, 0, log.retryable())

				stored := f.storedLead(lead.ID)
				if deleteFirst {
					require.ErrorIs(t, updateErr, apperrors.ErrCompanyNotFound)
					assert.Equal(t, automationCanaryNotes, stored.Notes, "the lead is unchanged")
				} else {
					require.NoError(t, updateErr)
					assert.Equal(t, "linked "+name, stored.Notes, "the update committed")
				}
				assert.Nil(t, stored.CompanyID)
				f.assertInvariant()
			})
		}
	})

	t.Run("update without company_id on a lead of the company", func(t *testing.T) {
		for _, deleteFirst := range []bool{true, false} {
			name := map[bool]string{false: "update first", true: "delete first"}[deleteFirst]
			t.Run(name, func(t *testing.T) {
				f := newLeadCompanyFixture(t, openAutomationMySQL(t, 6))
				company := f.company()
				lead := f.lead(company)
				log := &attemptLog{}

				updateErr, deleteErr := raceLeadWriterAgainstDelete(t, f, company, deleteFirst, log, func() error {
					_, err := f.leadSvc.Update(lead.ID, map[string]interface{}{"notes": "edited " + name})
					return err
				})
				require.NoError(t, deleteErr, "the delete succeeds")
				require.NoError(t, updateErr, "the update commits")
				if deleteFirst {
					assert.Equal(t, 2, log.attempts(), "one restart after the delete unlinked the lead")
					assert.Equal(t, 1, log.moved())
				} else {
					assert.Equal(t, 1, log.attempts())
					assert.Equal(t, 0, log.moved())
				}
				assert.Equal(t, 0, log.retryable())

				stored := f.storedLead(lead.ID)
				assert.Equal(t, "edited "+name, stored.Notes)
				assert.Nil(t, stored.CompanyID, "the lead ends unlinked")
				f.assertInvariant()
			})
		}
	})
}
