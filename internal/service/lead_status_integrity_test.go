package service

// Regression roots for the second round of issue #104:
//
//   - `converted` is reachable only through ConvertToCustomer: a lead update
//     and the bulk status endpoint refuse it as a target, so no lead is marked
//     converted without its customer and outside the conversion settings;
//   - the conversion's status check holds through its commit, against a
//     concurrent status change of the same lead (the conversion reads the
//     lead with a row lock, and a lead update re-reads it under the same
//     lock).
//
// The race is forced, not hoped for: a hook runs right after the
// conversion's in-transaction read of the lead and starts the competing
// update, then gives it a bounded window. Without the row lock the update
// commits inside that window and the conversion still converts; with it the
// update is still waiting when the window closes and applies after the
// commit. Limitation: the window is a bound, not an observation — on a host
// slow enough that an unblocked update takes longer than the window, the
// root would pass without exercising the race (it can never fail falsely).
// On SQLite the single pooled connection serialises the two by construction,
// so that root passes before and after; the MySQL/MariaDB root is the one
// that bites. Every outcome is asserted on committed rows.

import (
	"context"
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
	bulk := NewBulkStatusService(repository.NewBulkOperationRepository(db), repository.NewBulkStatusRepository(db),
		utils.NewTransactionManager(db), logrus.New())

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
		customer, err := env.svc.ConvertToCustomer(context.Background(), lead.ID, &models.Customer{})
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
		_, err := bulk.BulkSetLeadStatus(context.Background(), env.owner.ID, models.RoleSales, []uint{first.ID, second.ID}, models.LeadStatusConverted)
		assertConvertedRefusal(t, err)
		assert.Equal(t, models.LeadStatusQualified, env.committedLead(first.ID).Status)
		assert.Equal(t, models.LeadStatusNew, env.committedLead(second.ID).Status)

		_, err = bulk.BulkSetLeadStatus(context.Background(), env.owner.ID, models.RoleSales, []uint{first.ID, second.ID}, models.LeadStatusContacted)
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

// competitor runs one write in its own goroutine while the conversion is held
// inside its hook, and records whether it finished during that hold.
type competitor struct {
	done           chan error
	err            error
	finishedInHold bool
}

// holdWindow is how long the hook holds the conversion for the competitor.
// An update that no lock stops finishes in milliseconds; one waiting for the
// conversion's row lock is still waiting when the window ends.
const holdWindow = 500 * time.Millisecond

func startCompetitor(write func() error) *competitor {
	c := &competitor{done: make(chan error, 1)}
	go func() { c.done <- write() }()
	select {
	case c.err = <-c.done:
		c.finishedInHold = true
	case <-time.After(holdWindow):
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

	customer, convertErr := converting.ConvertToCustomer(context.Background(), lead.ID, &models.Customer{})
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
