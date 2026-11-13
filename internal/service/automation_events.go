package service

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"gorm.io/gorm"
)

// Outbox helpers shared by the lead, deal, ticket, form and bulk services. Each one
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

// automationLeadFormSubmitted records lead_form_submitted (#220), origin
// form, with the ids of the form and of the finalised submission linked to
// the lead. It carries ids only: the attribution stays on the submission row,
// where the step transaction reads it.
func automationLeadFormSubmitted(r repository.AutomationRepository, leadID, formID, submissionID uint) error {
	if r == nil {
		return nil
	}
	return r.RecordEvent(&models.AutomationEvent{
		EntityType:   models.AutomationEntityLead,
		EntityID:     leadID,
		EventType:    models.AutomationEventLeadFormSubmitted,
		Origin:       models.AutomationOriginForm,
		FormID:       &formID,
		SubmissionID: &submissionID,
		CreatedAt:    time.Now().UTC(),
	})
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

// automationTicketCreated records ticket_created (#242) with to_value set to
// the stored status.
func automationTicketCreated(r repository.AutomationRepository, ticketID uint, status models.TicketStatus, origin string, actorID *uint) error {
	return recordAutomationEvent(r, models.AutomationEntityTicket, ticketID, models.AutomationEventTicketCreated, "", string(status), origin, actorID)
}

// automationTicketUpdated records ticket_updated and, when the status
// differs, ticket_status_changed with the previous and the new status (#242).
// A priority, assignee or due_at change alone records ticket_updated only.
func automationTicketUpdated(r repository.AutomationRepository, ticketID uint, from, to models.TicketStatus, origin string, actorID *uint) error {
	if err := recordAutomationEvent(r, models.AutomationEntityTicket, ticketID, models.AutomationEventTicketUpdated, "", "", origin, actorID); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	return recordAutomationEvent(r, models.AutomationEntityTicket, ticketID, models.AutomationEventTicketStatusChanged, string(from), string(to), origin, actorID)
}

// automationTx binds the repository to the transaction, or stays nil when
// the service runs without automation.
func automationTx(r repository.AutomationRepository, tx *gorm.DB) repository.AutomationRepository {
	if r == nil {
		return nil
	}
	return r.WithTx(tx)
}
