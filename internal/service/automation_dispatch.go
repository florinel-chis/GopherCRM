package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// Dispatch claims unprocessed events and starts one run per enabled,
// matching rule. It returns how many runs it started.
func (e *AutomationEngine) Dispatch(ctx context.Context) (int, error) {
	events, err := e.repo.ListUnprocessedEvents(automationDispatchBatch)
	if err != nil {
		return 0, err
	}
	started := 0
	var errs []error
	for _, event := range events {
		if ctx.Err() != nil {
			break
		}
		n, err := e.DispatchEvent(event)
		started += n
		if err != nil {
			errs = append(errs, err)
			e.logger().WithFields(logrus.Fields{"event_id": event.ID, "event_type": event.EventType}).
				WithError(utils.LogError(err)).Error("Automation event dispatch failed")
		}
	}
	return started, errors.Join(errs...)
}

// DispatchEvent claims one event with a conditional update and, in the same
// transaction, creates exactly one run per enabled matching rule. An event
// another dispatcher claimed first starts nothing and is not an error. An
// event of origin automation is claimed and starts nothing: that is the loop
// guard.
func (e *AutomationEngine) DispatchEvent(event models.AutomationEvent) (int, error) {
	started := 0
	err := e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		started = 0
		claimed, err := r.ClaimEvent(event.ID, e.now())
		if err != nil {
			return err
		}
		if claimed != 1 {
			return errEventClaimed
		}
		if event.Origin == models.AutomationOriginAutomation {
			return nil
		}
		rules, err := r.ListMatchingRules(event.EntityType, event.EventType)
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if !triggerMatches(rule.Trigger, event) {
				continue
			}
			eventID := event.ID
			run := &models.AutomationRun{
				RuleID:       rule.ID,
				RuleVersion:  rule.Version,
				EntityType:   event.EntityType,
				EntityID:     event.EntityID,
				EventID:      &eventID,
				DedupeKey:    fmt.Sprintf("evt:%d:rule:%d", event.ID, rule.ID),
				Status:       models.AutomationRunPending,
				NextPosition: 1,
			}
			inserted, err := r.CreateRunIgnoringDuplicate(run)
			if err != nil {
				return err
			}
			if inserted {
				started++
			}
		}
		return nil
	}, automationTxRetries)
	if errors.Is(err, errEventClaimed) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if started > 0 {
		e.logger().WithFields(logrus.Fields{
			"event_id": event.ID, "event_type": event.EventType, "entity_type": event.EntityType,
			"entity_id": event.EntityID, "runs": started,
		}).Info("Automation runs started")
	}
	return started, nil
}

// triggerMatches applies the from/to filter of a status or stage change
// trigger and the form_id filter of lead_form_submitted (0 means every form);
// the other event triggers match every event of their type.
func triggerMatches(trigger models.AutomationTrigger, event models.AutomationEvent) bool {
	switch event.EventType {
	case models.AutomationEventLeadStatusChanged, models.AutomationEventDealStageChanged, models.AutomationEventTicketStatusChanged:
		if trigger.From != "" && trigger.From != event.FromValue {
			return false
		}
		if trigger.To != "" && trigger.To != event.ToValue {
			return false
		}
	case models.AutomationEventLeadFormSubmitted:
		if trigger.FormID != 0 && (event.FormID == nil || *event.FormID != trigger.FormID) {
			return false
		}
	}
	return true
}
