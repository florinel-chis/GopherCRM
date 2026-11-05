package service

// Service-level regression roots of issue #255: a service wired without the
// owner check refuses instead of skipping it, a deactivation committed by
// another connection is seen by the check on MySQL, and an assignment is one
// atomic unit. The HTTP behaviour is covered in
// test/integration/lead_owner_validation_test.go.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

var leadOwnerAdmin = LeadViewer{UserID: 1, Role: models.RoleAdmin}

// AC-12: an unwired check is a refusal, never a skipped check.
func TestLeadOwnerCheckRefusedWhenUnwired(t *testing.T) {
	capturePrivacyLogs(t) // a logger of its own, so the root also runs alone
	txManager := utils.NewTransactionManager(openQuoteTestSQLite(t))
	companies := new(mocks.CompanyRepository)
	companies.On("WithTx", mock.Anything).Return(companies).Maybe()

	newLeadRepo := func(current *models.Lead) *mocks.LeadRepository {
		repo := new(mocks.LeadRepository)
		repo.On("WithTx", mock.Anything).Return(repo).Maybe()
		if current != nil {
			repo.On("GetByID", current.ID).Return(current, nil).Maybe()
		}
		return repo
	}
	assertNoLeadWrite := func(t *testing.T, repo *mocks.LeadRepository) {
		t.Helper()
		repo.AssertNotCalled(t, "Create", mock.Anything)
		repo.AssertNotCalled(t, "Update", mock.Anything)
	}

	t.Run("no user repository: create", func(t *testing.T) {
		for _, manager := range []*utils.TransactionManager{txManager, nil} {
			repo := newLeadRepo(nil)
			err := NewLeadService(repo, nil, nil, nil, manager).Create(&models.Lead{OwnerID: 5})
			assert.ErrorIs(t, err, apperrors.ErrLeadOwnerCheckUnavailable)
			assertNoLeadWrite(t, repo)
		}
	})

	t.Run("no user repository: update changing the owner", func(t *testing.T) {
		for _, manager := range []*utils.TransactionManager{txManager, nil} {
			repo := newLeadRepo(&models.Lead{BaseModel: models.BaseModel{ID: 3}, OwnerID: 5})
			_, err := NewLeadService(repo, nil, companies, nil, manager).Update(3, map[string]interface{}{"owner_id": uint(6)})
			assert.ErrorIs(t, err, apperrors.ErrLeadOwnerCheckUnavailable)
			assertNoLeadWrite(t, repo)
		}
	})

	t.Run("no user repository: update that leaves the owner alone", func(t *testing.T) {
		for _, updates := range []map[string]interface{}{
			{"notes": "no owner in the body"},
			{"owner_id": uint(5), "notes": "the current owner re-sent"},
		} {
			repo := newLeadRepo(&models.Lead{BaseModel: models.BaseModel{ID: 3}, OwnerID: 5})
			repo.On("Update", mock.MatchedBy(func(lead *models.Lead) bool {
				return lead.OwnerID == 5 && lead.Notes == updates["notes"]
			})).Return(nil).Once()
			updated, err := NewLeadService(repo, nil, companies, nil, txManager).Update(3, updates)
			require.NoError(t, err)
			assert.Equal(t, uint(5), updated.OwnerID)
			repo.AssertExpectations(t)
		}
	})

	t.Run("no user repository: assign", func(t *testing.T) {
		repo := newLeadRepo(nil)
		_, err := NewLeadService(repo, nil, nil, nil, txManager).Assign(context.Background(), 3, 6, leadOwnerAdmin)
		assert.ErrorIs(t, err, apperrors.ErrLeadOwnerCheckUnavailable)
		assertNoLeadWrite(t, repo)
		repo.AssertNotCalled(t, "GetByID", mock.Anything)
	})

	t.Run("no transaction manager: assign has no other path", func(t *testing.T) {
		repo := newLeadRepo(nil)
		users := new(mocks.UserRepository)
		_, err := NewLeadService(repo, nil, nil, users, nil).Assign(context.Background(), 3, 6, leadOwnerAdmin)
		assert.ErrorIs(t, err, apperrors.ErrLeadOwnerCheckUnavailable)
		assertNoLeadWrite(t, repo)
		users.AssertNotCalled(t, "GetByID", mock.Anything)
	})
}

// beforeOwnerReadUsers fires the hook right before the first owner read made on
// a transaction handle, after the lead's locking read.
type beforeOwnerReadUsers struct {
	repository.UserRepository
	hook *onceHook
}

func (r beforeOwnerReadUsers) WithTx(tx *gorm.DB) repository.UserRepository {
	return beforeOwnerReadUsers{UserRepository: r.UserRepository.WithTx(tx), hook: r.hook}
}

func (r beforeOwnerReadUsers) GetByID(id uint) (*models.User, error) {
	r.hook.fire()
	return r.UserRepository.GetByID(id)
}

// AC-13: the owner check is a locking read, so it sees a deactivation committed
// after the lead's lock even when an earlier plain read in the same transaction
// (the company check) has already fixed the REPEATABLE READ snapshot.
func TestLeadOwnerSeesDeactivationCommittedAfterLeadLockMySQL(t *testing.T) {
	// One connection for the transaction under test, one for the deactivation,
	// one to read the outcome.
	db := openAutomationMySQL(t, 3)
	env := newAutomationEnv(t, db)
	companies := repository.NewCompanyRepository(db)
	company := models.Company{Name: "Live Company", Domain: "live-company.example"}
	require.NoError(t, db.Create(&company).Error)

	serviceWith := func(hook *onceHook) LeadService {
		return NewLeadService(env.leads, env.customers, companies, beforeOwnerReadUsers{UserRepository: env.users, hook: hook},
			env.txManager, WithLeadAutomationEvents(env.repo))
	}
	deactivateFromOtherConnection := func(t *testing.T, id uint) *onceHook {
		return &onceHook{fn: func() {
			require.NoError(t, db.Model(&models.User{}).Where("id = ?", id).Update("is_active", false).Error)
		}}
	}

	for _, tc := range []struct {
		name  string
		write func(svc LeadService, leadID, ownerID uint) error
	}{
		{"update", func(svc LeadService, leadID, ownerID uint) error {
			_, err := svc.Update(leadID, map[string]interface{}{"owner_id": ownerID, "notes": "must not stick"})
			return err
		}},
		{"update naming a live company", func(svc LeadService, leadID, ownerID uint) error {
			_, err := svc.Update(leadID, map[string]interface{}{"owner_id": ownerID, "notes": "must not stick", "company_id": &company.ID})
			return err
		}},
		{"assign", func(svc LeadService, leadID, ownerID uint) error {
			_, err := svc.Assign(context.Background(), leadID, ownerID, leadOwnerAdmin)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lead := env.createLead(env.sales, models.LeadStatusNew)
			target := env.createUser("leaving", models.RoleSales, true)
			hook := deactivateFromOtherConnection(t, target.ID)

			err := tc.write(serviceWith(hook), lead.ID, target.ID)
			require.ErrorIs(t, err, apperrors.ErrInvalidLeadOwner)

			stored := env.lead(lead.ID)
			assert.Equal(t, env.sales.ID, stored.OwnerID)
			assert.Nil(t, stored.CompanyID)
			assert.NotEqual(t, "must not stick", stored.Notes)
			assert.Empty(t, env.eventsOf(models.AutomationEntityLead, lead.ID))
		})
	}
}

// failingEventRepo fails the event write inside the assignment's transaction.
type failingEventRepo struct {
	repository.AutomationRepository
	err error
}

func (r failingEventRepo) WithTx(tx *gorm.DB) repository.AutomationRepository {
	return failingEventRepo{AutomationRepository: r.AutomationRepository.WithTx(tx), err: r.err}
}

func (r failingEventRepo) RecordEvent(*models.AutomationEvent) error { return r.err }

// AC-15: a failure at either write leaves the committed row and the event table
// as they were.
func TestLeadAssignIsAtomicSQLite(t *testing.T) {
	db := openAutomationSQLite(t)
	env := newAutomationEnv(t, db)
	companies := repository.NewCompanyRepository(db)

	t.Run("the lead write fails", func(t *testing.T) {
		// failingLeadRepo performs the write and then fails, so the rollback
		// has to discard a row that really was changed.
		fail := true
		leads := &failingLeadRepo{LeadRepository: env.leads, fail: &fail}
		svc := NewLeadService(leads, env.customers, companies, env.users, env.txManager, WithLeadAutomationEvents(env.repo))
		lead := env.createLead(env.sales, models.LeadStatusNew)

		_, err := svc.Assign(context.Background(), lead.ID, env.other.ID, leadOwnerAdmin)
		require.Error(t, err)
		assert.Equal(t, env.sales.ID, env.lead(lead.ID).OwnerID)
		assert.Empty(t, env.eventsOf(models.AutomationEntityLead, lead.ID))
	})

	t.Run("the event write fails", func(t *testing.T) {
		fault := errors.New("injected automation event fault")
		svc := NewLeadService(env.leads, env.customers, companies, env.users, env.txManager,
			WithLeadAutomationEvents(failingEventRepo{AutomationRepository: env.repo, err: fault}))
		lead := env.createLead(env.sales, models.LeadStatusNew)

		_, err := svc.Assign(context.Background(), lead.ID, env.other.ID, leadOwnerAdmin)
		require.ErrorIs(t, err, fault)
		assert.Equal(t, env.sales.ID, env.lead(lead.ID).OwnerID)
		assert.Empty(t, env.eventsOf(models.AutomationEntityLead, lead.ID))
	})

	t.Run("with no fault the assignment commits with its event", func(t *testing.T) {
		svc := NewLeadService(env.leads, env.customers, companies, env.users, env.txManager, WithLeadAutomationEvents(env.repo))
		lead := env.createLead(env.sales, models.LeadStatusNew)

		_, err := svc.Assign(context.Background(), lead.ID, env.other.ID, leadOwnerAdmin)
		require.NoError(t, err)
		assert.Equal(t, env.other.ID, env.lead(lead.ID).OwnerID)
		assert.Equal(t, []string{models.AutomationEventLeadUpdated}, eventTypes(env.eventsOf(models.AutomationEntityLead, lead.ID)))
	})
}
