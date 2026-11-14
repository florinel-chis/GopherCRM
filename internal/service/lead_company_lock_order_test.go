package service

// Regression roots of #182 that need no MySQL server: the restart decision,
// the restart bound on SQLite, the sequential outcomes of conversion, lead
// update and bulk lead status against a company delete in both orders, and
// the privacy of the lock-order log lines. The MySQL barrier roots are in
// lead_company_lock_order_mysql_test.go and bulk_lead_company_delete_test.go.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Canary values of the lock-order fixture's companies.
const leadCompanyCanaryName = "Canary Holdings Lock-Order"

// leadCompanyFixture is the real lead, company and bulk services over one
// database, built as instances whose test seams the roots set.
type leadCompanyFixture struct {
	t          *testing.T
	db         *gorm.DB
	env        *automationEnv
	companies  repository.CompanyRepository
	leadSvc    *leadService
	companySvc *companyService
	bulk       *bulkStatusService
}

func newLeadCompanyFixture(t *testing.T, db *gorm.DB) *leadCompanyFixture {
	t.Helper()
	env := newAutomationEnv(t, db)
	companies := repository.NewCompanyRepository(db)
	quiet := logrus.New()
	quiet.SetLevel(logrus.PanicLevel)
	leadSvc := NewLeadService(env.leads, env.customers, companies, env.users, env.txManager,
		WithLeadAutomationEvents(env.repo)).(*leadService)
	companySvc := NewCompanyService(companies, env.users, env.txManager).(*companyService)
	bulk := NewBulkStatusService(repository.NewBulkOperationRepository(db), repository.NewBulkStatusRepository(db),
		env.txManager, quiet, WithBulkAutomationEvents(env.repo)).(*bulkStatusService)
	return &leadCompanyFixture{
		t:          t,
		db:         db,
		env:        env,
		companies:  companies,
		leadSvc:    leadSvc,
		companySvc: companySvc,
		bulk:       bulk,
	}
}

func (f *leadCompanyFixture) company() *models.Company {
	f.t.Helper()
	seq := automationTestSeq.Add(1)
	company := &models.Company{Name: leadCompanyCanaryName, Domain: fmt.Sprintf("lock-order-%d.example", seq)}
	require.NoError(f.t, f.db.Create(company).Error)
	return company
}

// lead creates a lead of the sales user, linked to company when it is not nil.
func (f *leadCompanyFixture) lead(company *models.Company) *models.Lead {
	f.t.Helper()
	lead := f.env.createLead(f.env.sales, models.LeadStatusQualified)
	if company != nil {
		require.NoError(f.t, f.db.Model(&models.Lead{}).Where("id = ?", lead.ID).Update("company_id", company.ID).Error)
		lead.CompanyID = &company.ID
	}
	return lead
}

func (f *leadCompanyFixture) storedLead(id uint) *models.Lead { return f.env.lead(id) }

func (f *leadCompanyFixture) storedCustomer(id uint) *models.Customer {
	f.t.Helper()
	var customer models.Customer
	require.NoError(f.t, f.db.Unscoped().First(&customer, id).Error)
	return &customer
}

func (f *leadCompanyFixture) customerCount() int64 { return f.env.count(&models.Customer{}) }

// assertInvariant checks the property #182 is about: no live customer or
// lead names a soft-deleted company.
func (f *leadCompanyFixture) assertInvariant() {
	f.t.Helper()
	for _, table := range []string{"customers", "leads"} {
		var n int64
		require.NoError(f.t, f.db.Raw(`SELECT COUNT(*) FROM `+table+` r JOIN companies c ON c.id = r.company_id
			WHERE r.deleted_at IS NULL AND c.deleted_at IS NOT NULL`).Scan(&n).Error)
		assert.Zerof(f.t, n, "no live %s row references a soft-deleted company", table)
	}
}

// attemptLog counts the attempts of the lock-order path and keeps how each
// one ended.
type attemptLog struct {
	started atomic.Int32
	mu      sync.Mutex
	ended   []error
}

func (l *attemptLog) hooks() leadCompanyLockHooks {
	return leadCompanyLockHooks{
		attemptStarted: func(string, uint) { l.started.Add(1) },
		attemptEnded: func(_ string, _ uint, err error) {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.ended = append(l.ended, err)
		},
	}
}

func (l *attemptLog) attempts() int { return int(l.started.Load()) }

// moved counts the attempts ended by errLeadCompanyMoved.
func (l *attemptLog) moved() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, err := range l.ended {
		if errors.Is(err, errLeadCompanyMoved) {
			n++
		}
	}
	return n
}

// retryable counts the attempts ended by a deadlock or a lock wait timeout.
func (l *attemptLog) retryable() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, err := range l.ended {
		if isLockRetryable(err) {
			n++
		}
	}
	return n
}

func isLockRetryable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, pattern := range []string{"deadlock", "lock wait timeout", "error 1213", "error 1205"} {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

func TestLeadCompanyRestartDecision(t *testing.T) {
	live := context.Background()
	done, cancel := context.WithCancel(context.Background())
	cancel()
	moved := fmt.Errorf("lead 7 (attempt 1): %w", errLeadCompanyMoved)

	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"clean errLeadCompanyMoved on a live context", live, moved, true},
		{"the bare sentinel", live, errLeadCompanyMoved, true},
		{"nil", live, nil, false},
		{"another error", live, errors.New("boom"), false},
		{"a not-found", live, gorm.ErrRecordNotFound, false},
		{"the contended sentinel", live, apperrors.ErrLeadCompanyContended, false},
		{"rollback failed", live, errors.Join(moved, utils.ErrRollbackFailed), false},
		{"poisoned", live, errors.Join(moved, utils.ErrTransactionPoisoned, utils.ErrRollbackFailed), false},
		{"commit failed", live, fmt.Errorf("%w: %w", utils.ErrCommitFailed, moved), false},
		{"begin failed", live, fmt.Errorf("%w: %w", utils.ErrBeginFailed, moved), false},
		{"done context", done, moved, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, shouldRestartLeadCompanyAttempt(tc.ctx, tc.err))
		})
	}
}

// TestLeadCompanyRestartBoundSQLite moves the lead's company_id inside each
// attempt, after the plain read and before the locks, through the
// instance-local afterPlainRead seam. The write is on the attempt's own
// transaction (SQLite has one connection), so it rolls back with the attempt.
func TestLeadCompanyRestartBoundSQLite(t *testing.T) {
	f := newLeadCompanyFixture(t, openAutomationSQLite(t))

	// moveOn returns a seam that points the lead at other in the attempts
	// whose number is in the set (every attempt when the set is nil).
	moveOn := func(t *testing.T, log *attemptLog, other *models.Company, attempts map[int]bool) leadCompanyLockHooks {
		hooks := log.hooks()
		hooks.afterPlainRead = func(_ string, tx *gorm.DB, leadID uint) {
			if attempts != nil && !attempts[log.attempts()] {
				return
			}
			assert.NoError(t, tx.Model(&models.Lead{}).Where("id = ?", leadID).Update("company_id", other.ID).Error)
		}
		return hooks
	}

	for _, op := range []string{leadCompanyOpConvert, leadCompanyOpUpdate} {
		run := func(lead *models.Lead) (uint, error) {
			if op == leadCompanyOpConvert {
				customer, err := f.leadSvc.ConvertToCustomer(context.Background(), lead.ID, &models.Customer{})
				if err != nil {
					return 0, err
				}
				return customer.ID, nil
			}
			_, err := f.leadSvc.Update(lead.ID, map[string]interface{}{"notes": "bound-" + op})
			return 0, err
		}

		t.Run(op+": persistent mismatch gives up after 4 attempts", func(t *testing.T) {
			home, other := f.company(), f.company()
			lead := f.lead(home)
			customersBefore := f.customerCount()
			log := &attemptLog{}
			f.leadSvc.lockHooks = moveOn(t, log, other, nil)
			t.Cleanup(func() { f.leadSvc.lockHooks = leadCompanyLockHooks{} })

			_, err := run(lead)
			require.ErrorIs(t, err, apperrors.ErrLeadCompanyContended)
			assert.NotErrorIs(t, err, apperrors.ErrNotFound)
			assert.Equal(t, 4, log.attempts(), "3 restarts, 4 attempts")
			assert.Equal(t, 4, log.moved())

			stored := f.storedLead(lead.ID)
			require.NotNil(t, stored.CompanyID)
			assert.Equal(t, home.ID, *stored.CompanyID, "every move rolled back")
			assert.Equal(t, models.LeadStatusQualified, stored.Status)
			assert.Nil(t, stored.CustomerID)
			assert.Equal(t, automationCanaryNotes, stored.Notes, "no lead change")
			assert.Equal(t, customersBefore, f.customerCount(), "no customer")
			assert.Empty(t, f.env.eventsOf(models.AutomationEntityLead, lead.ID), "no automation event")
		})

		t.Run(op+": one mismatch restarts once and writes on the fresh read", func(t *testing.T) {
			home, other := f.company(), f.company()
			lead := f.lead(home)
			log := &attemptLog{}
			f.leadSvc.lockHooks = moveOn(t, log, other, map[int]bool{1: true})
			t.Cleanup(func() { f.leadSvc.lockHooks = leadCompanyLockHooks{} })

			customerID, err := run(lead)
			require.NoError(t, err)
			assert.Equal(t, 2, log.attempts())
			assert.Equal(t, 1, log.moved())

			stored := f.storedLead(lead.ID)
			require.NotNil(t, stored.CompanyID)
			assert.Equal(t, home.ID, *stored.CompanyID, "the second attempt read the committed company")
			if op == leadCompanyOpConvert {
				customer := f.storedCustomer(customerID)
				require.NotNil(t, customer.CompanyID)
				assert.Equal(t, home.ID, *customer.CompanyID, "converted with the link the second attempt locked")
				assert.Equal(t, models.LeadStatusConverted, stored.Status)
			} else {
				assert.Equal(t, "bound-"+op, stored.Notes)
			}
			assert.NotEmpty(t, f.env.eventsOf(models.AutomationEntityLead, lead.ID), "the committed attempt's events")
		})
	}
}

// TestLeadCompanyLockOrderOutcomesSQLite runs each writer and a company delete
// one after the other in both orders (SQLite's single connection serialises
// them; the MySQL roots force the overlap) and checks the exact outcome and
// the invariant.
func TestLeadCompanyLockOrderOutcomesSQLite(t *testing.T) {
	f := newLeadCompanyFixture(t, openAutomationSQLite(t))
	ctx := context.Background()

	deleteCompany := func(t *testing.T, company *models.Company) {
		t.Helper()
		require.NoError(t, f.companySvc.Delete(company.ID), "the delete succeeds")
	}

	t.Run("conversion: delete first converts without the link", func(t *testing.T) {
		company := f.company()
		lead := f.lead(company)
		deleteCompany(t, company)
		customer, err := f.leadSvc.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
		require.NoError(t, err)
		assert.Nil(t, f.storedCustomer(customer.ID).CompanyID)
		assert.Equal(t, models.LeadStatusConverted, f.storedLead(lead.ID).Status)
		f.assertInvariant()
	})

	t.Run("conversion: convert first links, then the delete unlinks", func(t *testing.T) {
		company := f.company()
		lead := f.lead(company)
		customer, err := f.leadSvc.ConvertToCustomer(ctx, lead.ID, &models.Customer{})
		require.NoError(t, err)
		require.NotNil(t, f.storedCustomer(customer.ID).CompanyID)
		assert.Equal(t, company.ID, *f.storedCustomer(customer.ID).CompanyID)
		deleteCompany(t, company)
		assert.Nil(t, f.storedCustomer(customer.ID).CompanyID)
		assert.Nil(t, f.storedLead(lead.ID).CompanyID)
		f.assertInvariant()
	})

	t.Run("update setting company_id: delete first is ErrCompanyNotFound", func(t *testing.T) {
		company := f.company()
		lead := f.lead(nil)
		deleteCompany(t, company)
		_, err := f.leadSvc.Update(lead.ID, map[string]interface{}{"company_id": &company.ID, "notes": "must not stick"})
		require.ErrorIs(t, err, apperrors.ErrCompanyNotFound)
		stored := f.storedLead(lead.ID)
		assert.Nil(t, stored.CompanyID)
		assert.Equal(t, automationCanaryNotes, stored.Notes, "the lead is unchanged")
		f.assertInvariant()
	})

	t.Run("update setting company_id: update first commits, then the delete unlinks", func(t *testing.T) {
		company := f.company()
		lead := f.lead(nil)
		_, err := f.leadSvc.Update(lead.ID, map[string]interface{}{"company_id": &company.ID})
		require.NoError(t, err)
		require.NotNil(t, f.storedLead(lead.ID).CompanyID)
		deleteCompany(t, company)
		assert.Nil(t, f.storedLead(lead.ID).CompanyID)
		f.assertInvariant()
	})

	for _, deleteFirst := range []bool{true, false} {
		order := map[bool]string{true: "delete first", false: "update first"}[deleteFirst]
		t.Run("update without company_id: "+order, func(t *testing.T) {
			company := f.company()
			lead := f.lead(company)
			if deleteFirst {
				deleteCompany(t, company)
			}
			_, err := f.leadSvc.Update(lead.ID, map[string]interface{}{"notes": "edited " + order})
			require.NoError(t, err)
			if !deleteFirst {
				deleteCompany(t, company)
			}
			stored := f.storedLead(lead.ID)
			assert.Nil(t, stored.CompanyID)
			assert.Equal(t, "edited "+order, stored.Notes)
			f.assertInvariant()
		})
	}

	for _, deleteFirst := range []bool{true, false} {
		order := map[bool]string{true: "delete first", false: "bulk first"}[deleteFirst]
		t.Run("bulk lead status: "+order, func(t *testing.T) {
			company := f.company()
			first, second := f.lead(company), f.lead(company)
			if deleteFirst {
				deleteCompany(t, company)
			}
			_, err := f.bulk.BulkSetLeadStatus(ctx, f.env.admin.ID, models.RoleAdmin, []uint{second.ID, first.ID}, models.LeadStatusContacted)
			require.NoError(t, err)
			if !deleteFirst {
				deleteCompany(t, company)
			}
			for _, id := range []uint{first.ID, second.ID} {
				stored := f.storedLead(id)
				assert.Equal(t, models.LeadStatusContacted, stored.Status)
				assert.Nil(t, stored.CompanyID)
			}
			f.assertInvariant()
		})
	}
}

// failingLockCompanies fails LockLive for one company id with an error that
// is not a not-found.
type failingLockCompanies struct {
	repository.CompanyRepository
	failID uint
}

func (r failingLockCompanies) WithTx(tx *gorm.DB) repository.CompanyRepository {
	return failingLockCompanies{CompanyRepository: r.CompanyRepository.WithTx(tx), failID: r.failID}
}

func (r failingLockCompanies) LockLive(id uint) error {
	if id == r.failID {
		return errors.New("injected company lock failure")
	}
	return r.CompanyRepository.LockLive(id)
}

// TestLeadCompanyLockLogsOmitPersonalData drives the restart, exhaustion,
// missing-company and company-lock paths and checks that their log lines
// carry ids, attempt counts and error codes, and none of the lead's or the
// company's name, email or phone.
func TestLeadCompanyLockLogsOmitPersonalData(t *testing.T) {
	output, hook := capturePrivacyLogs(t)
	f := newLeadCompanyFixture(t, openAutomationSQLite(t))
	ctx := context.Background()

	// Restart and exhaustion.
	home, other := f.company(), f.company()
	contended := f.lead(home)
	log := &attemptLog{}
	hooks := log.hooks()
	hooks.afterPlainRead = func(_ string, tx *gorm.DB, leadID uint) {
		require.NoError(t, tx.Model(&models.Lead{}).Where("id = ?", leadID).Update("company_id", other.ID).Error)
	}
	f.leadSvc.lockHooks = hooks
	_, err := f.leadSvc.ConvertToCustomer(ctx, contended.ID, &models.Customer{})
	require.ErrorIs(t, err, apperrors.ErrLeadCompanyContended)
	_, err = f.leadSvc.Update(contended.ID, map[string]interface{}{"notes": "x"})
	require.ErrorIs(t, err, apperrors.ErrLeadCompanyContended)
	f.leadSvc.lockHooks = leadCompanyLockHooks{}

	// Missing company: the lead's company deleted before a conversion, and a
	// sent company deleted before an update. The delete unlinked the first
	// lead, so seed the dangling link by hand to reach the missing-company
	// line of the conversion.
	gone := f.company()
	dangling := f.lead(gone)
	require.NoError(t, f.db.Delete(&models.Company{}, gone.ID).Error)
	_, err = f.leadSvc.ConvertToCustomer(ctx, dangling.ID, &models.Customer{})
	require.NoError(t, err)
	plain := f.lead(nil)
	_, err = f.leadSvc.Update(plain.ID, map[string]interface{}{"company_id": &gone.ID})
	require.ErrorIs(t, err, apperrors.ErrCompanyNotFound)

	// Company lock failure.
	broken := f.company()
	lockFailing := f.lead(broken)
	failing := NewLeadService(f.env.leads, f.env.customers, failingLockCompanies{CompanyRepository: f.companies, failID: broken.ID},
		f.env.users, f.env.txManager).(*leadService)
	_, err = failing.ConvertToCustomer(ctx, lockFailing.ID, &models.Customer{})
	require.Error(t, err)

	text := output.String()
	for _, lead := range []*models.Lead{contended, dangling, plain, lockFailing} {
		stored := f.storedLead(lead.ID)
		assert.NotContains(t, text, stored.Email)
	}
	for _, canary := range []string{automationCanaryFirstName, automationCanaryLastName, automationCanaryPhone,
		automationCanaryNotes, leadCompanyCanaryName, "Canary Works"} {
		assert.NotContains(t, text, canary)
	}

	messages := map[string]bool{}
	for _, entry := range hook.AllEntries() {
		switch entry.Message {
		case "Restarting after the lead's company changed":
			messages[entry.Message] = true
			assert.Contains(t, entry.Data, "lead_id")
			assert.Contains(t, entry.Data, "attempt")
			assert.Equal(t, "lead_company_moved", entry.Data["error_code"])
		case "Lead's company changed before its row lock; the attempt rolls back":
			messages[entry.Message] = true
			assert.Contains(t, entry.Data, "lead_id")
			assert.Contains(t, entry.Data, "company_id")
			assert.Contains(t, entry.Data, "attempt")
		case "Lead's company kept changing; giving up":
			messages[entry.Message] = true
			assert.Equal(t, 4, entry.Data["attempts"])
			assert.Equal(t, "lead_company_contended", entry.Data["error_code"])
			assert.Contains(t, entry.Data, "lead_id")
		case "Company lock found no live company":
			messages[entry.Message] = true
			assert.Contains(t, entry.Data, "lead_id")
			assert.Contains(t, entry.Data, "company_id")
		case "Lead links a company that no longer exists; converting without the link":
			messages[entry.Message] = true
			assert.Equal(t, "company_not_found", entry.Data["error_code"])
			assert.Contains(t, entry.Data, "company_id")
		case "Failed to lock company":
			messages[entry.Message] = true
			assert.Equal(t, broken.ID, entry.Data["company_id"])
			assert.Contains(t, entry.Data, "lead_id")
		}
	}
	for _, message := range []string{
		"Restarting after the lead's company changed",
		"Lead's company changed before its row lock; the attempt rolls back",
		"Lead's company kept changing; giving up",
		"Company lock found no live company",
		"Lead links a company that no longer exists; converting without the link",
		"Failed to lock company",
	} {
		assert.Truef(t, messages[message], "the path logged %q", message)
	}
}
