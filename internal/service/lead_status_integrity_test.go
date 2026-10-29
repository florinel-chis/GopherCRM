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
// The races are forced, not hoped for: a hook runs right after the conversion
// has read the row in question and starts the competing write, then waits a
// bounded time for it. Without a row lock the competitor commits inside that
// window; with one it is still blocked when the window closes and runs after
// the conversion commits. Every outcome is asserted on committed rows. On
// SQLite the single pooled connection serialises the two anyway, so the
// roots pass there before and after; the MySQL roots are the ones that bite.

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

// competitorWindow is how long the hook lets the competing write run while
// the conversion holds its read. A competitor that is not blocked by a lock
// finishes well inside it; one that is blocked is still waiting when it ends.
const competitorWindow = 750 * time.Millisecond

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
	hook *onceHook
}

func (r hookedLeadRepo) WithTx(tx *gorm.DB) repository.LeadRepository {
	return hookedTxLeadRepo{LeadRepository: r.LeadRepository.WithTx(tx), hook: r.hook}
}

type hookedTxLeadRepo struct {
	repository.LeadRepository
	hook *onceHook
}

func (r hookedTxLeadRepo) GetByID(id uint) (*models.Lead, error) {
	lead, err := r.LeadRepository.GetByID(id)
	if err == nil {
		r.hook.fire()
	}
	return lead, err
}

// hookedCompanyRepo fires the hook after the conversion's first company read
// or lock on a transaction handle.
type hookedCompanyRepo struct {
	repository.CompanyRepository
	hook *onceHook
}

func (r hookedCompanyRepo) WithTx(tx *gorm.DB) repository.CompanyRepository {
	return hookedTxCompanyRepo{CompanyRepository: r.CompanyRepository.WithTx(tx), hook: r.hook}
}

type hookedTxCompanyRepo struct {
	repository.CompanyRepository
	hook *onceHook
}

func (r hookedTxCompanyRepo) GetByID(id uint) (*models.Company, error) {
	company, err := r.CompanyRepository.GetByID(id)
	r.hook.fire()
	return company, err
}

func (r hookedTxCompanyRepo) LockByID(id uint) error {
	err := r.CompanyRepository.LockByID(id)
	r.hook.fire()
	return err
}

// competitor runs one write in its own goroutine and reports whether it
// finished inside the window the hook gives it.
type competitor struct {
	done           chan error
	err            error
	finishedInHold bool
}

func startCompetitor(write func() error) *competitor {
	c := &competitor{done: make(chan error, 1)}
	go func() { c.done <- write() }()
	select {
	case c.err = <-c.done:
		c.finishedInHold = true
	case <-time.After(competitorWindow):
	}
	return c
}

// wait returns the competitor's error once it has finished, failing the test
// if it has not within the bounded wait.
func (c *competitor) wait(t *testing.T) error {
	t.Helper()
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
		rival = startCompetitor(func() error {
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
		rival = startCompetitor(func() error { return companyService.Delete(doomed.ID) })
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
