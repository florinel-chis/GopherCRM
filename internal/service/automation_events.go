package service

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"gorm.io/gorm"
)

// Outbox helpers shared by the lead, deal, form and bulk services. Each one
// records the events of one committed write through a transaction-bound
// repository, so the event lives or dies with the write that produced it. A
// nil repository means the service runs without automation (tests, tools)
// and records nothing.

func recordAutomationEvent(r repository.AutomationRepository, entityType string, entityID uint, eventType, from, to, origin string, actorID *uint) error {
	if r == nil {
		return nil
	}
	return r.RecordEvent(&models.AutomationEvent{
		EntityType: entityType,
		EntityID:   entityID,
		EventType:  eventType,
		FromValue:  from,
		ToValue:    to,
		Origin:     origin,
		ActorID:    actorID,
		CreatedAt:  time.Now().UTC(),
	})
}

// automationLeadCreated records lead_created.
func automationLeadCreated(r repository.AutomationRepository, leadID uint, origin string, actorID *uint) error {
	return recordAutomationEvent(r, models.AutomationEntityLead, leadID, models.AutomationEventLeadCreated, "", "", origin, actorID)
}

// automationLeadUpdated records lead_updated and, when the status differs,
// lead_status_changed with the previous and the new value.
func automationLeadUpdated(r repository.AutomationRepository, leadID uint, from, to models.LeadStatus, origin string, actorID *uint) error {
	if err := recordAutomationEvent(r, models.AutomationEntityLead, leadID, models.AutomationEventLeadUpdated, "", "", origin, actorID); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	return recordAutomationEvent(r, models.AutomationEntityLead, leadID, models.AutomationEventLeadStatusChanged, string(from), string(to), origin, actorID)
}

// automationDealCreated records deal_created with to_value set to the stage.
func automationDealCreated(r repository.AutomationRepository, dealID uint, stage models.DealStage, origin string, actorID *uint) error {
	return recordAutomationEvent(r, models.AutomationEntityDeal, dealID, models.AutomationEventDealCreated, "", string(stage), origin, actorID)
}

// automationDealUpdated records deal_updated and, when the stage differs,
// deal_stage_changed.
func automationDealUpdated(r repository.AutomationRepository, dealID uint, from, to models.DealStage, origin string, actorID *uint) error {
	if err := recordAutomationEvent(r, models.AutomationEntityDeal, dealID, models.AutomationEventDealUpdated, "", "", origin, actorID); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	return automationDealStageChanged(r, dealID, from, to, origin, actorID)
}

// automationDealStageChanged records deal_stage_changed alone, the event of
// the stage endpoint.
func automationDealStageChanged(r repository.AutomationRepository, dealID uint, from, to models.DealStage, origin string, actorID *uint) error {
	return recordAutomationEvent(r, models.AutomationEntityDeal, dealID, models.AutomationEventDealStageChanged, string(from), string(to), origin, actorID)
}

// automationTx binds the repository to the transaction, or stays nil when
// the service runs without automation.
func automationTx(r repository.AutomationRepository, tx *gorm.DB) repository.AutomationRepository {
	if r == nil {
		return nil
	}
	return r.WithTx(tx)
}
