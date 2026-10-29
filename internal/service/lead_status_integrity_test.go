package service

// Regression roots for the second round of issue #104:
//
//   - `converted` is reachable only through ConvertToCustomer: a lead update
//     and the bulk status endpoint refuse it as a target, so no lead is marked
//     converted without its customer and outside the conversion settings;
//   - the conversion's status check holds through its commit, against a
//     concurrent status change of the same lead;
//   - a conversion and a concurrent delete of the lead's company serialise, so
//     no live customer is left pointing at a deleted company.
//
//   - a conversion whose lead moved to another company before the lead lock
//     never locks that company under the lead lock, so it cannot deadlock
//     with a delete of the new company.
//
// The races are forced, not hoped for: a hook runs right after the conversion
// has read or locked the row in question and starts the competing write. On
// MySQL the hook releases the conversion only once the competitor has either
// committed (no lock stopped it — the defect) or been seen in INNODB_TRX
// waiting for a row lock in this schema (the fix). Every outcome is asserted
// on committed rows. On SQLite the single pooled connection serialises the
// two anyway, so the roots pass there before and after; the MySQL roots are
// the ones that bite.

import (
	"errors"
	"sync"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLeadConvertedStatusOnlyByConversionSQLite(t *testing.T) {
	runConvertedStatusScenarios(t, openQuoteTestSQLite(t))
}

func TestLeadConvertedStatusOnlyByConversionMySQL(t *testing.T) {
	runConvertedStatusScenarios(t, openQuoteTestMySQL(t, 4))
}

func TestLeadConversionStatusRaceSQLite(t *testing.T) {
	runConversionStatusRace(t, openQuoteTestSQLite(t))
}

func TestLeadConversionStatusRaceMySQL(t *testing.T) {
	runConversionStatusRace(t, openQuoteTestMySQL(t, 4))
}

func TestLeadConversionCompanyDeleteRaceSQLite(t *testing.T) {
	runConversionCompanyDeleteRace(t, openQuoteTestSQLite(t))
}

func TestLeadConversionCompanyDeleteRaceMySQL(t *testing.T) {
	runConversionCompanyDeleteRace(t, openQuoteTestMySQL(t, 4))
}

func assertConvertedRefusal(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrValidation), "got %v", err)
	var ruleErr *LeadConversionRuleError
	require.True(t, errors.As(err, &ruleErr), "got %v", err)
	assert.Equal(t, "status", ruleErr.Field)
	assert.Contains(t, ruleErr.Message, "/convert")
}

func runConvertedStatusScenarios(t *testing.T, db *gorm.DB) {
	root := newLeadConversionEnv(t, db)
	require.NoError(t, db.AutoMigrate(&models.BulkOperation{}, &models.BulkOperationItem{}, &models.Task{}, &models.Ticket{}))
	bulk := NewBulkOperationService(repository.NewBulkOperationRepository(db), repository.NewBulkRepository(db),
		repository.NewUserRepository(db), root.leads, repository.NewCustomerRepository(db), repository.NewTaskRepository(db),
		repository.NewTicketRepository(db), utils.NewTransactionManager(db), logrus.New())

	t.Run("an update cannot set converted", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		lead := env.newLead(models.LeadStatusQualified, nil)
		_, err := env.svc.Update(lead.ID, map[string]interface{}{"status": models.LeadStatusConverted, "notes": "skip the dialog"})
		assertConvertedRefusal(t, err)
		stored := env.committedLead(lead.ID)
		assert.Equal(t, models.LeadStatusQualified, stored.Status)
		assert.Empty(t, stored.Notes, "a refused update writes nothing")
		assert.Nil(t, stored.CustomerID)
		assert.Zero(t, env.customersWithEmail(lead.Email))
	})

	t.Run("a converted lead can still be edited and moved on", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		lead := env.newLead(models.LeadStatusQualified, nil)
		customer, err := env.svc.ConvertToCustomer(lead.ID, &models.Customer{})
		require.NoError(t, err)

		// The edit form sends the current status back with the other fields.
		_, err = env.svc.Update(lead.ID, map[string]interface{}{"status": models.LeadStatusConverted, "notes": "renewal due"})
		require.NoError(t, err)
		stored := env.committedLead(lead.ID)
		assert.Equal(t, models.LeadStatusConverted, stored.Status)
		assert.Equal(t, "renewal due", stored.Notes)
		require.NotNil(t, stored.CustomerID)
		assert.Equal(t, customer.ID, *stored.CustomerID)

		_, err = env.svc.Update(lead.ID, map[string]interface{}{"status": models.LeadStatusContacted})
		require.NoError(t, err, "leaving converted is unchanged behaviour")
		stored = env.committedLead(lead.ID)
		assert.Equal(t, models.LeadStatusContacted, stored.Status)
		require.NotNil(t, stored.CustomerID, "the link to the customer survives")
	})

	t.Run("the bulk status endpoint cannot set converted", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		first := env.newLead(models.LeadStatusQualified, nil)
		second := env.newLead(models.LeadStatusNew, nil)
		_, err := bulk.BulkSetLeadStatus(env.owner.ID, models.RoleSales, []uint{first.ID, second.ID}, models.LeadStatusConverted)
		assertConvertedRefusal(t, err)
		assert.Equal(t, models.LeadStatusQualified, env.committedLead(first.ID).Status)
		assert.Equal(t, models.LeadStatusNew, env.committedLead(second.ID).Status)

		_, err = bulk.BulkSetLeadStatus(env.owner.ID, models.RoleSales, []uint{first.ID, second.ID}, models.LeadStatusContacted)
		require.NoError(t, err, "every other status is unchanged")
		assert.Equal(t, models.LeadStatusContacted, env.committedLead(first.ID).Status)
		assert.Equal(t, models.LeadStatusContacted, env.committedLead(second.ID).Status)
	})
}

// onceHook runs fn the first time it is called, and never again.
type onceHook struct {
	once sync.Once
	fn   func()
}

func (h *onceHook) fire() {
	if h != nil && h.fn != nil {
		h.once.Do(h.fn)
	}
}

// hookedLeadRepo fires the hook after the first lead read made on a
// transaction handle — the conversion's in-transaction read of the lead.
type hookedLeadRepo struct {
	repository.LeadRepository
	hook  *onceHook
	order *lockOrder
}

func (r hookedLeadRepo) WithTx(tx *gorm.DB) repository.LeadRepository {
	return hookedTxLeadRepo{LeadRepository: r.LeadRepository.WithTx(tx), hook: r.hook, order: r.order, tx: tx}
}

type hookedTxLeadRepo struct {
	repository.LeadRepository
	hook  *onceHook
	order *lockOrder
	tx    *gorm.DB
}

func (r hookedTxLeadRepo) GetByID(id uint) (*models.Lead, error) {
	lead, err := r.LeadRepository.GetByID(id)
	r.order.leadLocked(r.tx)
	if err == nil {
		r.hook.fire()
	}
	return lead, err
}

// hookedCompanyRepo fires the hook after the conversion's first company read
// or lock on a transaction handle.
type hookedCompanyRepo struct {
	repository.CompanyRepository
	hook  *onceHook
	order *lockOrder
}

func (r hookedCompanyRepo) WithTx(tx *gorm.DB) repository.CompanyRepository {
	return hookedTxCompanyRepo{CompanyRepository: r.CompanyRepository.WithTx(tx), hook: r.hook, order: r.order, tx: tx}
}

type hookedTxCompanyRepo struct {
	repository.CompanyRepository
	hook  *onceHook
	order *lockOrder
	tx    *gorm.DB
}

func (r hookedTxCompanyRepo) GetByID(id uint) (*models.Company, error) {
	company, err := r.CompanyRepository.GetByID(id)
	r.order.companyLocked(r.tx)
	r.hook.fire()
	return company, err
}

func (r hookedTxCompanyRepo) LockByID(id uint) error {
	r.order.companyLocked(r.tx)
	err := r.CompanyRepository.LockByID(id)
	r.hook.fire()
	return err
}

// lockOrder records, per database transaction, whether the conversion asked
// for a company lock after it had read (locked) the lead — the order that
// can deadlock with a company delete, which locks the company and then the
// lead. A nil *lockOrder records nothing.
type lockOrder struct {
	mu         sync.Mutex
	leadTaken  map[gorm.ConnPool]bool
	violations int
}

func newLockOrder() *lockOrder { return &lockOrder{leadTaken: map[gorm.ConnPool]bool{}} }

func (o *lockOrder) leadLocked(tx *gorm.DB) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.leadTaken[tx.Statement.ConnPool] = true
}

func (o *lockOrder) companyLocked(tx *gorm.DB) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.leadTaken[tx.Statement.ConnPool] {
		o.violations++
	}
}

// competitor runs one write in its own goroutine while the conversion is held
// inside its hook, and records what happened to it during that hold:
// finished (no lock stopped it) or blocked on a row lock.
type competitor struct {
	done           chan error
	err            error
	finishedInHold bool
	blocked        bool
	// probeFailure is set when neither outcome was observed in time; the
	// test fails on it after the conversion returns (the hook runs inside
	// the conversion's transaction, where failing the test is not safe).
	probeFailure string
}

// sqliteHoldWindow is the hold on SQLite. There the competitor cannot reach
// the database at all while the conversion runs — the pool has one
// connection, which the conversion's transaction holds — so there is no lock
// wait to observe; the two are serialised by construction and the window
// only has to let an unblocked competitor (there is none) finish.
const sqliteHoldWindow = 200 * time.Millisecond

// lockWaitProbe reports whether a connection of this test's MySQL schema is
// waiting for a row lock: a performance_schema.data_lock_waits row whose
// requesting thread belongs to the connection's own database (each root runs
// in a uniquely named schema). It returns nil on any other driver.
func lockWaitProbe(db *gorm.DB) func() (bool, error) {
	if db.Dialector.Name() != "mysql" {
		return nil
	}
	return func() (bool, error) {
		var waiting int64
		err := db.Raw(`SELECT COUNT(*) FROM performance_schema.data_lock_waits w
			JOIN performance_schema.threads th ON th.THREAD_ID = w.REQUESTING_THREAD_ID
			WHERE th.PROCESSLIST_DB = DATABASE()`).Scan(&waiting).Error
		return waiting > 0, err
	}
}

// startCompetitor starts write and returns once it has either finished or —
// on MySQL — been seen waiting for a row lock in the database. The
// conversion is released only then, so "blocked" is an observation, not an
// inference from a timeout.
func startCompetitor(db *gorm.DB, write func() error) *competitor {
	c := &competitor{done: make(chan error, 1)}
	go func() { c.done <- write() }()

	probe := lockWaitProbe(db)
	if probe == nil {
		select {
		case c.err = <-c.done:
			c.finishedInHold = true
		case <-time.After(sqliteHoldWindow):
		}
		return c
	}

	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(quoteTestTimeout)
	for {
		select {
		case c.err = <-c.done:
			c.finishedInHold = true
			return c
		case <-ticker.C:
			waiting, err := probe()
			if err != nil {
				c.probeFailure = "lock-wait probe failed: " + err.Error()
				return c
			}
			if waiting {
				c.blocked = true
				return c
			}
		case <-deadline:
			c.probeFailure = "the competing write neither finished nor waited on a lock in time"
			return c
		}
	}
}

// wait returns the competitor's error once it has finished, failing the test
// if it has not within the bounded wait.
func (c *competitor) wait(t *testing.T) error {
	t.Helper()
	require.Empty(t, c.probeFailure)
	if c.finishedInHold {
		return c.err
	}
	select {
	case c.err = <-c.done:
	case <-time.After(quoteTestTimeout):
		t.Fatal("the competing write did not finish in time")
	}
	return c.err
}

func runConversionStatusRace(t *testing.T, db *gorm.DB) {
	env := newLeadConversionEnv(t, db)
	env.reset()
	txManager := utils.NewTransactionManager(db)
	customers := repository.NewCustomerRepository(db)
	companies := repository.NewCompanyRepository(db)
	plain := NewLeadService(env.leads, customers, companies, txManager, WithLeadConversionRules(env.config))

	lead := env.newLead(models.LeadStatusQualified, nil)
	var rival *competitor
	hook := &onceHook{fn: func() {
		// "new" is not an allowed conversion status.
		rival = startCompetitor(db, func() error {
			_, err := plain.Update(lead.ID, map[string]interface{}{"status": models.LeadStatusNew})
			return err
		})
	}}
	converting := NewLeadService(hookedLeadRepo{LeadRepository: env.leads, hook: hook}, customers, companies, txManager,
		WithLeadConversionRules(env.config))

	customer, convertErr := converting.ConvertToCustomer(lead.ID, &models.Customer{})
	require.NotNil(t, rival, "the hook must have run")
	rivalErr := rival.wait(t)
	require.NoError(t, rivalErr, "the status change is legal either way")

	stored := env.committedLead(lead.ID)
	if convertErr != nil {
		// Serialised before: the conversion saw "new" and refused.
		var ruleErr *LeadConversionRuleError
		require.True(t, errors.As(convertErr, &ruleErr), "got %v", convertErr)
		assert.Equal(t, models.LeadStatusNew, stored.Status)
		assert.Nil(t, stored.CustomerID)
		assert.Zero(t, env.customersWithEmail(lead.Email))
		return
	}
	// Serialised after: the status change waited for the conversion's
	// commit and then applied to the converted lead, keeping its customer.
	assert.False(t, rival.finishedInHold,
		"the status change committed while the conversion held a qualified read, and the conversion still created a customer")
	if lockWaitProbe(db) != nil {
		assert.True(t, rival.blocked, "on MySQL the status change was seen waiting for the conversion's lock")
	}
	assert.Equal(t, models.LeadStatusNew, stored.Status, "the later status change applies")
	require.NotNil(t, stored.CustomerID, "the later write must not drop the conversion's link")
	assert.Equal(t, customer.ID, *stored.CustomerID)
	assert.Equal(t, int64(1), env.customersWithEmail(lead.Email))
}

func runConversionCompanyDeleteRace(t *testing.T, db *gorm.DB) {
	env := newLeadConversionEnv(t, db)
	env.reset()
	require.NoError(t, db.AutoMigrate(&models.Deal{}))
	txManager := utils.NewTransactionManager(db)
	customers := repository.NewCustomerRepository(db)
	companies := repository.NewCompanyRepository(db)
	companyService := NewCompanyService(companies, repository.NewUserRepository(db), txManager)

	doomed := models.Company{Name: "Doomed Ltd", Domain: "doomed.example"}
	require.NoError(t, db.Create(&doomed).Error)
	lead := env.newLead(models.LeadStatusQualified, &doomed.ID)

	var rival *competitor
	hook := &onceHook{fn: func() {
		rival = startCompetitor(db, func() error { return companyService.Delete(doomed.ID) })
	}}
	converting := NewLeadService(env.leads, customers, hookedCompanyRepo{CompanyRepository: companies, hook: hook}, txManager,
		WithLeadConversionRules(env.config))

	customer, convertErr := converting.ConvertToCustomer(lead.ID, &models.Customer{})
	require.NotNil(t, rival, "the hook must have run")
	require.NoError(t, rival.wait(t), "the company delete succeeds either way")
	require.NoError(t, convertErr, "the conversion succeeds either way, with or without the link")

	var deleted models.Company
	require.NoError(t, db.Unscoped().First(&deleted, doomed.ID).Error)
	require.True(t, deleted.DeletedAt.Valid, "the company is soft-deleted")

	stored := env.committedCustomer(customer.ID)
	assert.Nil(t, stored.CompanyID, "no live customer may reference the deleted company")
	var dangling int64
	require.NoError(t, db.Model(&models.Customer{}).Where("company_id = ?", doomed.ID).Count(&dangling).Error)
	assert.Zero(t, dangling)
	assert.Nil(t, env.committedLead(lead.ID).CompanyID, "the delete unlinked the lead too")
}

func TestLeadConversionCompanyMovedDeleteRaceSQLite(t *testing.T) {
	runConversionCompanyMovedDeleteRace(t, openQuoteTestSQLite(t))
}

func TestLeadConversionCompanyMovedDeleteRaceMySQL(t *testing.T) {
	runConversionCompanyMovedDeleteRace(t, openQuoteTestMySQL(t, 4))
}

// The lead moves from company A to company B after the conversion's first,
// unlocked read; B is then deleted while the conversion holds the lead lock.
// The delete holds B and waits for the lead (to unlink it). A conversion that
// then locked B under the lead lock would deadlock with it; the conversion
// must instead give the attempt up and start over in company-then-lead order.
// Both calls succeed, no deadlock error surfaces, and no live row references
// the deleted company.
func runConversionCompanyMovedDeleteRace(t *testing.T, db *gorm.DB) {
	env := newLeadConversionEnv(t, db)
	env.reset()
	txManager := utils.NewTransactionManager(db)
	customers := repository.NewCustomerRepository(db)
	companies := repository.NewCompanyRepository(db)
	companyService := NewCompanyService(companies, repository.NewUserRepository(db), txManager)

	first := models.Company{Name: "First Ltd", Domain: "first.example"}
	require.NoError(t, db.Create(&first).Error)
	second := models.Company{Name: "Second Ltd", Domain: "second.example"}
	require.NoError(t, db.Create(&second).Error)
	lead := env.newLead(models.LeadStatusQualified, &first.ID)

	// After the conversion locked A and before it locks the lead: move the
	// lead to B, committed on another connection (on SQLite the conversion's
	// own transaction holds the only connection, so the move goes through it).
	moveHook := &onceHook{fn: func() {}}
	var rival *competitor
	leadHook := &onceHook{fn: func() {
		rival = startCompetitor(db, func() error { return companyService.Delete(second.ID) })
	}}
	var moveErr error
	if lockWaitProbe(db) != nil {
		moveHook.fn = func() {
			moveErr = db.Model(&models.Lead{}).Where("id = ?", lead.ID).Update("company_id", second.ID).Error
		}
	} else {
		// SQLite: the lead is moved before the conversion starts; the pool
		// serialises everything that follows, so there is no deadlock to
		// provoke, only the outcome to check.
		require.NoError(t, db.Model(&models.Lead{}).Where("id = ?", lead.ID).Update("company_id", second.ID).Error)
	}
	order := newLockOrder()
	converting := NewLeadService(
		hookedLeadRepo{LeadRepository: env.leads, hook: leadHook, order: order},
		customers,
		hookedCompanyRepo{CompanyRepository: companies, hook: moveHook, order: order},
		txManager, WithLeadConversionRules(env.config))

	customer, convertErr := converting.ConvertToCustomer(lead.ID, &models.Customer{})
	require.NoError(t, moveErr)
	require.NotNil(t, rival, "the hook must have run")
	deleteErr := rival.wait(t)
	require.NoError(t, deleteErr, "the company delete must not be a deadlock victim")
	require.NoError(t, convertErr, "the conversion succeeds")
	// Whichever side a deadlock detector would pick, a company lock taken
	// under the lead lock is the defect: the conversion must restart instead.
	assert.Zero(t, order.violations, "the conversion locked a company while holding the lead lock")

	var deleted models.Company
	require.NoError(t, db.Unscoped().First(&deleted, second.ID).Error)
	require.True(t, deleted.DeletedAt.Valid)
	assert.Nil(t, env.committedCustomer(customer.ID).CompanyID, "no live customer may reference the deleted company")
	var dangling int64
	require.NoError(t, db.Model(&models.Customer{}).Where("company_id = ?", second.ID).Count(&dangling).Error)
	assert.Zero(t, dangling)
	assert.Nil(t, env.committedLead(lead.ID).CompanyID)
	assert.Equal(t, models.LeadStatusConverted, env.committedLead(lead.ID).Status)
}

// flappingLeadRepo reports a different company link on every in-transaction
// read of the lead, as if it moved between each attempt's lock and read.
type flappingLeadRepo struct {
	repository.LeadRepository
	next *uint
}

func (r flappingLeadRepo) WithTx(tx *gorm.DB) repository.LeadRepository {
	return flappingTxLeadRepo{LeadRepository: r.LeadRepository.WithTx(tx), next: r.next}
}

type flappingTxLeadRepo struct {
	repository.LeadRepository
	next *uint
}

func (r flappingTxLeadRepo) GetByID(id uint) (*models.Lead, error) {
	lead, err := r.LeadRepository.GetByID(id)
	if err == nil {
		*r.next++
		companyID := 1000 + *r.next
		lead.CompanyID = &companyID
	}
	return lead, err
}

// The restart for a moved company link is bounded: a lead whose link keeps
// changing is given up with ErrLeadChangedDuringConversion (409) and nothing
// is written.
func TestLeadConversionGivesUpWhenTheCompanyKeepsMoving(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newLeadConversionEnv(t, db)
	env.reset()
	reads := new(uint)
	svc := NewLeadService(flappingLeadRepo{LeadRepository: env.leads, next: reads}, repository.NewCustomerRepository(db),
		repository.NewCompanyRepository(db), utils.NewTransactionManager(db), WithLeadConversionRules(env.config))
	lead := env.newLead(models.LeadStatusQualified, nil)

	_, err := svc.ConvertToCustomer(lead.ID, &models.Customer{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLeadChangedDuringConversion), "got %v", err)
	assert.Equal(t, uint(1+maxConversionRestarts), *reads, "one first attempt plus the bounded restarts")
	assert.Zero(t, env.customersWithEmail(lead.Email))
	assert.Equal(t, models.LeadStatusQualified, env.committedLead(lead.ID).Status)
}
