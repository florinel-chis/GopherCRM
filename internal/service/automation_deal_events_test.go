package service

import (
	"errors"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AC-4: deal writes record events in the same transaction as the deal row
// and its stage-history row. Create records deal_created with to_value set
// to the stage; PUT records deal_updated plus deal_stage_changed when the
// stage differs; the stage endpoint records deal_stage_changed, won and lost
// included; the same stage records nothing; a rolled-back write records
// nothing.

func TestAutomationDealEventsSQLite(t *testing.T) {
	runAutomationDealEvents(t, openAutomationSQLite(t))
}

func TestAutomationDealEventsMySQL(t *testing.T) {
	runAutomationDealEvents(t, openAutomationMySQL(t, 2))
}

// failingDealRepo makes CreateStageChange fail after the deal row was
// written, so the whole write rolls back.
type failingDealRepo struct {
	repository.DealRepository
	fail *bool
}

func (r *failingDealRepo) CreateStageChange(change *models.DealStageChange) error {
	if err := r.DealRepository.CreateStageChange(change); err != nil {
		return err
	}
	if *r.fail {
		return errors.New("injected stage history fault")
	}
	return nil
}

func (r *failingDealRepo) WithTx(tx *gorm.DB) repository.DealRepository {
	return &failingDealRepo{DealRepository: r.DealRepository.WithTx(tx), fail: r.fail}
}

func runAutomationDealEvents(t *testing.T, db *gorm.DB) {
	env := newAutomationEnv(t, db)
	fail := false
	dealRepo := &failingDealRepo{DealRepository: env.deals, fail: &fail}
	svc := NewDealService(dealRepo, repository.NewCompanyRepository(db), env.customers, env.leads, env.users,
		&stubCurrency{value: "EUR"}, env.txManager, WithDealAutomationEvents(env.repo))
	actor := env.sales.ID

	deal := &models.Deal{Title: "Engine", OwnerID: env.sales.ID}
	require.NoError(t, svc.Create(deal, nil, actor))
	events := env.eventsOf(models.AutomationEntityDeal, deal.ID)
	require.Equal(t, []string{models.AutomationEventDealCreated}, eventTypes(events))
	assert.Equal(t, string(models.DealStageQualification), events[0].ToValue)
	assert.Equal(t, models.AutomationOriginUser, events[0].Origin)
	require.NotNil(t, events[0].ActorID)
	assert.Equal(t, actor, *events[0].ActorID)
	assert.Len(t, historyOf(t, db, deal.ID), 1)

	// PUT with the same stage: deal_updated only.
	stored, err := svc.GetByID(deal.ID)
	require.NoError(t, err)
	stored.Title = "Engine v2"
	require.NoError(t, svc.Update(stored, nil, false, actor))
	events = env.eventsOf(models.AutomationEntityDeal, deal.ID)
	require.Equal(t, []string{models.AutomationEventDealCreated, models.AutomationEventDealUpdated}, eventTypes(events))

	// PUT into another stage: deal_updated plus deal_stage_changed.
	stored, err = svc.GetByID(deal.ID)
	require.NoError(t, err)
	stored.Stage = models.DealStageProposal
	require.NoError(t, svc.Update(stored, nil, false, actor))
	events = env.eventsOf(models.AutomationEntityDeal, deal.ID)
	require.Len(t, events, 4)
	assert.Equal(t, models.AutomationEventDealUpdated, events[2].EventType)
	assert.Equal(t, models.AutomationEventDealStageChanged, events[3].EventType)
	assert.Equal(t, string(models.DealStageQualification), events[3].FromValue)
	assert.Equal(t, string(models.DealStageProposal), events[3].ToValue)
	assert.Len(t, historyOf(t, db, deal.ID), 2, "the history row and the event commit together")

	// The stage endpoint: won, then lost, then the same stage again.
	_, err = svc.ChangeStage(deal.ID, models.DealStageWon, nil, nil, actor)
	require.NoError(t, err)
	reason := "price"
	_, err = svc.ChangeStage(deal.ID, models.DealStageLost, nil, &reason, actor)
	require.NoError(t, err)
	events = env.eventsOf(models.AutomationEntityDeal, deal.ID)
	require.Len(t, events, 6)
	assert.Equal(t, models.AutomationEventDealStageChanged, events[4].EventType)
	assert.Equal(t, string(models.DealStageProposal), events[4].FromValue)
	assert.Equal(t, string(models.DealStageWon), events[4].ToValue)
	assert.Equal(t, models.AutomationEventDealStageChanged, events[5].EventType)
	assert.Equal(t, string(models.DealStageWon), events[5].FromValue)
	assert.Equal(t, string(models.DealStageLost), events[5].ToValue)

	_, err = svc.ChangeStage(deal.ID, models.DealStageLost, nil, nil, actor)
	require.NoError(t, err)
	assert.Len(t, env.eventsOf(models.AutomationEntityDeal, deal.ID), 6, "the same stage again records nothing")
	assert.Len(t, historyOf(t, db, deal.ID), 4)

	// A rolled-back write records nothing: the history insert fails after
	// the deal row was written, and the event with it.
	fail = true
	_, err = svc.ChangeStage(deal.ID, models.DealStageNegotiation, nil, nil, actor)
	require.Error(t, err)
	rolledBack := &models.Deal{Title: "Never", OwnerID: env.sales.ID}
	require.Error(t, svc.Create(rolledBack, nil, actor))
	fail = false
	assert.Len(t, env.eventsOf(models.AutomationEntityDeal, deal.ID), 6, "a rolled-back stage change records no event")
	assert.Len(t, historyOf(t, db, deal.ID), 4, "and no history row")
	var deals int64
	require.NoError(t, db.Model(&models.Deal{}).Count(&deals).Error)
	assert.Equal(t, int64(1), deals, "the rolled-back create left no deal row")
	var all int64
	require.NoError(t, db.Model(&models.AutomationEvent{}).Count(&all).Error)
	assert.Equal(t, int64(6), all, "and no event")
	current, err := svc.GetByID(deal.ID)
	require.NoError(t, err)
	assert.Equal(t, models.DealStageLost, current.Stage)
}
