package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// RunSchedules makes progress on every enabled schedule rule whose pass for
// the current UTC day is due and not finished: one batch of at most
// models.AutomationScheduleBatchSize records per rule per tick.
func (e *AutomationEngine) RunSchedules(ctx context.Context) error {
	rules, err := e.repo.ListScheduleRules()
	if err != nil {
		return err
	}
	now := e.now().UTC()
	today := now.Format("2006-01-02")
	var errs []error
	for i := range rules {
		if ctx.Err() != nil {
			break
		}
		rule := rules[i]
		if now.Hour() < rule.Trigger.HourUTC {
			continue
		}
		if rule.ScheduleCompletedOn != nil && *rule.ScheduleCompletedOn == today {
			continue
		}
		if err := e.runScheduleBatch(rule, now, today); err != nil {
			if errors.Is(err, errScheduleCursorMoved) {
				continue
			}
			errs = append(errs, err)
			e.logger().WithField("rule_id", rule.ID).WithError(utils.LogError(err)).Error("Automation schedule batch failed")
		}
	}
	return errors.Join(errs...)
}

// runScheduleBatch inserts one run per record of the next batch and advances
// the cursor with a conditional update on (schedule_pass_on, schedule_cursor),
// in one transaction. A pass that started on an earlier day restarts from
// cursor 0 for today; an earlier day is never backfilled.
func (e *AutomationEngine) runScheduleBatch(rule models.AutomationRule, now time.Time, today string) error {
	expectedPassOn := rule.SchedulePassOn
	expectedCursor := rule.ScheduleCursor
	afterID := uint(0)
	if expectedPassOn != nil && *expectedPassOn == today {
		afterID = expectedCursor
	}
	query := repository.AutomationScheduleQuery{
		EntityType: rule.EntityType,
		AfterID:    afterID,
		Limit:      models.AutomationScheduleBatchSize,
	}
	if rule.Trigger.Filters != nil {
		query.Status = rule.Trigger.Filters.Status
		query.Stage = rule.Trigger.Filters.Stage
		query.TicketStatus = rule.Trigger.Filters.TicketStatus
	}
	if rule.Trigger.StaleDays > 0 {
		before := now.Add(-time.Duration(rule.Trigger.StaleDays) * 24 * time.Hour)
		query.StaleBefore = &before
	}
	started := 0
	err := e.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		r := e.repo.WithTx(tx)
		started = 0
		ids, err := r.ListScheduleCandidateIDs(query)
		if err != nil {
			return err
		}
		for _, id := range ids {
			run := &models.AutomationRun{
				RuleID:       rule.ID,
				RuleVersion:  rule.Version,
				EntityType:   rule.EntityType,
				EntityID:     id,
				DedupeKey:    fmt.Sprintf("sch:%d:%s:%d", rule.ID, today, id),
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
		cursor := afterID
		if len(ids) > 0 {
			cursor = ids[len(ids)-1]
		}
		var completedOn *string
		if len(ids) < models.AutomationScheduleBatchSize {
			day := today
			completedOn = &day
		}
		rows, err := r.AdvanceScheduleCursor(rule.ID, expectedPassOn, expectedCursor, today, cursor, completedOn, now)
		if err != nil {
			return err
		}
		if rows != 1 {
			return errScheduleCursorMoved
		}
		return nil
	}, automationTxRetries)
	if err != nil {
		return err
	}
	e.logger().WithFields(logrus.Fields{"rule_id": rule.ID, "pass": today, "runs": started}).Info("Automation schedule batch handed out")
	return nil
}
