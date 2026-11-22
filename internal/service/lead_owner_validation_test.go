package service

// Service-level regression roots of issue #255: a service wired without the
// owner check refuses instead of skipping it, a deactivation committed by
// another connection is seen by the check on MySQL, and an assignment is one
// atomic unit. The HTTP behaviour is covered in
// test/integration/lead_owner_validation_test.go.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

var leadOwnerAdmin = LeadViewer{UserID: 1, Scope: models.PermissionScopeAll}

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
		users.AssertNotCalled(t, "GetByIDWithCurrentRole", mock.Anything)
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

func (r beforeOwnerReadUsers) GetByIDWithCurrentRole(id uint) (*models.User, error) {
	r.hook.fire()
	return r.UserRepository.GetByIDWithCurrentRole(id)
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

// leadOwnerRoleWait bounds every wait of the custom-role roots below.
const leadOwnerRoleWait = 10 * time.Second

// newCustomRoleLeadOwner creates a custom role holding leads.view (own) and
// assignment.form_owner, and a live, active user holding it.
func newCustomRoleLeadOwner(t *testing.T, env *automationEnv, roles repository.RoleRepository) (*models.User, *models.Role) {
	t.Helper()
	role := &models.Role{Name: "Lead Owner Role " + strings.ReplaceAll(t.Name(), "/", " ")}
	require.NoError(t, roles.Create(role))
	require.NoError(t, roles.ReplacePermissions(role.ID, []models.RolePermission{
		{PermissionKey: permissions.LeadsView, Scope: models.PermissionScopeOwn},
		{PermissionKey: permissions.AssignmentFormOwner, Scope: models.PermissionScopeAll},
	}))
	roleID := role.ID
	owner := &models.User{Email: fmt.Sprintf("custom-owner-%d@automation.test", automationTestSeq.Add(1)), Password: "x",
		FirstName: "Custom", LastName: "Owner", Role: models.RoleCustom, RoleID: &roleID, IsActive: true}
	require.NoError(t, env.db.Create(owner).Error)
	return owner, role
}

// migrateCustomRoleTables adds the RBAC tables the automation schema lacks.
func migrateCustomRoleTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&models.Role{}, &models.RolePermission{}))
}

// #677: the owner check reads the owner's custom role and its permissions with
// a shared lock, so a permission change committed after the update's first
// plain read (which fixes the REPEATABLE READ snapshot) is seen and the lead
// write is refused.
func TestLeadOwnerSeesRolePermissionChangeMySQL(t *testing.T) {
	// One connection for the transaction under test, one for the role write,
	// one to read the outcome.
	db := openAutomationMySQL(t, 3)
	migrateCustomRoleTables(t, db)
	env := newAutomationEnv(t, db)
	companies := repository.NewCompanyRepository(db)
	company := models.Company{Name: "Live Company", Domain: "live-company.example"}
	require.NoError(t, db.Create(&company).Error)

	withoutOwner := []models.RolePermission{{PermissionKey: permissions.LeadsView, Scope: models.PermissionScopeOwn}}
	withOwner := []models.RolePermission{
		{PermissionKey: permissions.LeadsView, Scope: models.PermissionScopeOwn},
		{PermissionKey: permissions.AssignmentFormOwner, Scope: models.PermissionScopeAll},
		{PermissionKey: "deals.read", Scope: models.PermissionScopeAll},
	}
	// replaceFromOtherConnection commits the new permission set on a pooled
	// connection other than the lead transaction's, bounded by a deadline.
	replaceFromOtherConnection := func(t *testing.T, roleID uint, set []models.RolePermission) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), leadOwnerRoleWait)
		defer cancel()
		require.NoError(t, repository.NewRoleRepository(db.WithContext(ctx)).ReplacePermissions(roleID, set))
	}
	serviceWith := func(t *testing.T, afterPlainRead func()) LeadService {
		svc := NewLeadService(env.leads, env.customers, companies, env.users, env.txManager, WithLeadAutomationEvents(env.repo))
		if afterPlainRead != nil {
			hook := &onceHook{fn: afterPlainRead}
			svc.(*leadService).lockHooks = leadCompanyLockHooks{afterPlainRead: func(string, *gorm.DB, uint) { hook.fire() }}
		}
		return svc
	}
	update := func(svc LeadService, leadID, ownerID uint, withCompany bool) error {
		updates := map[string]interface{}{"owner_id": ownerID, "notes": "must not stick"}
		if withCompany {
			updates["company_id"] = &company.ID
		}
		_, err := svc.Update(leadID, updates)
		return err
	}
	assertUnchanged := func(t *testing.T, lead *models.Lead) {
		t.Helper()
		stored := env.lead(lead.ID)
		assert.Equal(t, env.sales.ID, stored.OwnerID)
		assert.Nil(t, stored.CompanyID)
		assert.Equal(t, lead.Notes, stored.Notes)
		assert.Empty(t, env.eventsOf(models.AutomationEntityLead, lead.ID))
	}

	for _, tc := range []struct {
		name        string
		withCompany bool
	}{
		{"update", false},
		{"update naming a live company", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roles := repository.NewRoleRepository(db)
			owner, role := newCustomRoleLeadOwner(t, env, roles)
			lead := env.createLead(env.sales, models.LeadStatusNew)
			fired := false
			svc := serviceWith(t, func() {
				fired = true
				replaceFromOtherConnection(t, role.ID, withoutOwner)
			})

			err := update(svc, lead.ID, owner.ID, tc.withCompany)
			require.True(t, fired, "the role write ran after the plain read")
			require.ErrorIs(t, err, apperrors.ErrInvalidLeadOwner)
			assertUnchanged(t, lead)
		})
	}

	t.Run("role keeps the owner permission", func(t *testing.T) {
		roles := repository.NewRoleRepository(db)
		owner, role := newCustomRoleLeadOwner(t, env, roles)
		lead := env.createLead(env.sales, models.LeadStatusNew)
		fired := false
		svc := serviceWith(t, func() {
			fired = true
			replaceFromOtherConnection(t, role.ID, withOwner)
		})

		require.NoError(t, update(svc, lead.ID, owner.ID, false))
		require.True(t, fired, "the role write ran after the plain read")
		stored := env.lead(lead.ID)
		assert.Equal(t, owner.ID, stored.OwnerID)
		assert.Equal(t, "must not stick", stored.Notes)
	})

	t.Run("role changed before the update", func(t *testing.T) {
		roles := repository.NewRoleRepository(db)
		owner, role := newCustomRoleLeadOwner(t, env, roles)
		lead := env.createLead(env.sales, models.LeadStatusNew)
		replaceFromOtherConnection(t, role.ID, withoutOwner)

		err := update(serviceWith(t, nil), lead.ID, owner.ID, false)
		require.ErrorIs(t, err, apperrors.ErrInvalidLeadOwner)
		assertUnchanged(t, lead)
	})
}

// #677: the owner check's role read is shared, not exclusive. While another
// transaction holds shared locks on the owner's roles row and its
// role_permissions rows, a lead update naming that owner still commits.
func TestLeadOwnerRoleReadIsSharedMySQL(t *testing.T) {
	// The holder, the transaction under test, and one to read the outcome.
	db := openAutomationMySQL(t, 4)
	migrateCustomRoleTables(t, db)
	env := newAutomationEnv(t, db)
	companies := repository.NewCompanyRepository(db)
	svc := NewLeadService(env.leads, env.customers, companies, env.users, env.txManager, WithLeadAutomationEvents(env.repo))

	owner, role := newCustomRoleLeadOwner(t, env, repository.NewRoleRepository(db))
	lead := env.createLead(env.sales, models.LeadStatusNew)

	ctx, cancel := context.WithTimeout(context.Background(), 3*leadOwnerRoleWait)
	defer cancel()
	holder := db.WithContext(ctx).Begin()
	require.NoError(t, holder.Error)
	shared := holder.Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Session(&gorm.Session{})
	var heldRoles []models.Role
	if err := shared.Where("id = ?", role.ID).Find(&heldRoles).Error; err != nil {
		holder.Rollback()
		require.NoError(t, err)
	}
	var heldPermissions []models.RolePermission
	if err := shared.Where("role_id = ?", role.ID).Find(&heldPermissions).Error; err != nil {
		holder.Rollback()
		require.NoError(t, err)
	}
	// Both SELECTs have returned: the shared locks are held before the update
	// starts.
	if len(heldRoles) != 1 || len(heldPermissions) != 2 {
		holder.Rollback()
		require.Len(t, heldRoles, 1, "the holder shares the roles row")
		require.Len(t, heldPermissions, 2, "the holder shares the role_permissions rows")
	}

	done := make(chan error, 1)
	go func() {
		_, err := svc.Update(lead.ID, map[string]interface{}{"owner_id": owner.ID})
		done <- err
	}()
	finished := false
	defer func() {
		// Release the locks first, then wait for the update, so nothing is
		// left running when the root ends.
		holder.Rollback()
		if !finished {
			select {
			case <-done:
			case <-time.After(2 * leadOwnerRoleWait):
				t.Errorf("the update did not finish after the holder rolled back")
			}
		}
	}()

	timer := time.NewTimer(leadOwnerRoleWait)
	defer timer.Stop()
	var err error
	select {
	case err = <-done:
		finished = true
	case <-timer.C:
		t.Fatalf("the update waited for the shared role locks for %s", leadOwnerRoleWait)
	}
	require.NoError(t, err)
	assert.Equal(t, owner.ID, env.lead(lead.ID).OwnerID)
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
