package service

import (
	"errors"
	"sync/atomic"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingAutomationRepo counts the run reads the service makes, so the
// role refusals can be shown to happen before any query.
type countingAutomationRepo struct {
	repository.AutomationRepository
	queries atomic.Int64
}

func (r *countingAutomationRepo) ListRuns(offset, limit int, filter repository.AutomationRunListFilter, scope repository.AutomationRunScope) ([]models.AutomationRun, int64, error) {
	r.queries.Add(1)
	return r.AutomationRepository.ListRuns(offset, limit, filter, scope)
}

func (r *countingAutomationRepo) GetRunByID(id uint) (*models.AutomationRun, error) {
	r.queries.Add(1)
	return r.AutomationRepository.GetRunByID(id)
}

func (r *countingAutomationRepo) RunInScope(id uint, scope repository.AutomationRunScope) (bool, error) {
	r.queries.Add(1)
	return r.AutomationRepository.RunInScope(id, scope)
}

// AC-11 (service half): the run service refuses support and customer
// callers with ErrForbidden before any query, gives the admin every run and
// narrows a sales caller to the runs on the leads and deals it owns.
func TestAutomationRunServiceRoles(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))
	counting := &countingAutomationRepo{AutomationRepository: env.repo}
	svc := NewAutomationService(counting, env.users, env.engine, env.txManager)

	rule := env.leadRule(models.AutomationEventLeadCreated, waitStep(5))
	mine := env.createLead(env.sales, models.LeadStatusNew)
	theirs := env.createLead(env.other, models.LeadStatusNew)
	myRun := env.startRun(rule, mine)
	theirRun := env.startRun(rule, theirs)
	dealRule := env.createRule(models.AutomationEntityDeal, models.AutomationEventDealCreated, models.AutomationTrigger{}, waitStep(5))
	myDeal := env.createDeal(env.sales, models.DealStageProposal, 1, "EUR", nil, nil)
	env.recordEvent(models.AutomationEntityDeal, myDeal.ID, models.AutomationEventDealCreated, "", "proposal", models.AutomationOriginUser)
	env.dispatch()
	myDealRun := env.runsOf(dealRule.ID)[0]

	for _, role := range []models.UserRole{models.RoleSupport, models.RoleCustomer, ""} {
		counting.queries.Store(0)
		viewer := LeadViewer{UserID: env.admin.ID, Role: role}
		_, _, err := svc.ListRuns(viewer, 0, 20, repository.AutomationRunListFilter{})
		assert.Truef(t, errors.Is(err, apperrors.ErrForbidden), "role %q list: %v", role, err)
		_, err = svc.GetRun(viewer, myRun.ID)
		assert.Truef(t, errors.Is(err, apperrors.ErrForbidden), "role %q get: %v", role, err)
		assert.Zerof(t, counting.queries.Load(), "role %q was refused before any query", role)
	}

	admin := LeadViewer{UserID: env.admin.ID, Role: models.RoleAdmin}
	runs, total, err := svc.ListRuns(admin, 0, 20, repository.AutomationRunListFilter{})
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	assert.Len(t, runs, 3)
	got, err := svc.GetRun(admin, theirRun.ID)
	require.NoError(t, err)
	assert.Equal(t, theirRun.ID, got.ID)

	sales := LeadViewer{UserID: env.sales.ID, Role: models.RoleSales}
	runs, total, err = svc.ListRuns(sales, 0, 20, repository.AutomationRunListFilter{})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	ids := map[uint]bool{}
	for _, run := range runs {
		ids[run.ID] = true
	}
	assert.True(t, ids[myRun.ID])
	assert.True(t, ids[myDealRun.ID])
	assert.False(t, ids[theirRun.ID])
	_, err = svc.GetRun(sales, theirRun.ID)
	assert.True(t, errors.Is(err, apperrors.ErrAutomationRunNotFound), "somebody else's run is not found, got %v", err)
	got, err = svc.GetRun(sales, myDealRun.ID)
	require.NoError(t, err)
	assert.Equal(t, myDealRun.ID, got.ID)

	// Cancel: active runs cancel with manual; a finished run is refused.
	cancelled, err := svc.CancelRun(myRun.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AutomationRunCancelled, cancelled.Status)
	assert.Equal(t, models.AutomationCancelManual, cancelled.CancelReason)
	_, err = svc.CancelRun(myRun.ID)
	assert.True(t, errors.Is(err, apperrors.ErrAutomationRunFinished), "got %v", err)
	_, err = svc.CancelRun(999999)
	assert.True(t, errors.Is(err, apperrors.ErrAutomationRunNotFound), "got %v", err)
}
