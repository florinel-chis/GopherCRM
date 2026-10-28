package service

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AC-6: each step other than send_email runs in one transaction — locking
// read of the record, conditional advance of the run, the effect, the
// run-step row — and the effects are as specified: a task assigned to the
// resolved staff user and linked to the lead, or to the deal's customer else
// its lead; an owner change that records an automation-origin event which
// starts no run; one notification. A fault after the effect rolls everything
// back and the next execution performs the step exactly once. On MySQL a
// cancel, a rule change or an erasure committed from a second connection
// after the step's first read leaves no task, notification, owner change or
// run-step row. An unavailable recipient is skipped with
// recipient_unavailable. send_email is two transactions around one
// Mailer.Send: a sending row first, the send, then the finalized row and the
// conditional advance; a fault after the send never sends again; a second
// executor cannot insert another sending row; a cancel between the halves
// stays cancelled with the row finalized.

func TestAutomationStepExecutionSQLite(t *testing.T) {
	runAutomationStepExecution(t, openAutomationSQLite(t))
}

func TestAutomationStepExecutionMySQL(t *testing.T) {
	runAutomationStepExecution(t, openAutomationMySQL(t, 4))
}

func runAutomationStepExecution(t *testing.T, db *gorm.DB) {
	env := newAutomationEnv(t, db)

	t.Run("a lead run performs every effect once in order", func(t *testing.T) {
		rule := env.leadRule(models.AutomationEventLeadCreated,
			taskStep(models.AutomationRecipientOwner, 0, 2),
			assignStep(env.other.ID),
			notifyStep(models.AutomationRecipientUser, env.other.ID),
			emailStep(models.AutomationRecipientOwner, 0))
		lead := env.createLead(env.sales, models.LeadStatusNew)
		run := env.startRun(rule, lead)
		eventsBefore := len(env.events())
		env.executeDue()

		stored := env.run(run.ID)
		assert.Equal(t, models.AutomationRunCompleted, stored.Status)
		assert.Equal(t, 5, stored.NextPosition)
		require.NotNil(t, stored.FinishedAt)
		steps := env.stepsOf(run.ID)
		require.Len(t, steps, 4)
		for i, step := range steps {
			assert.Equal(t, i+1, step.Position)
			assert.Equal(t, models.AutomationOutcomeSucceeded, step.Outcome, "step %d", i+1)
		}

		tasks := env.tasksList()
		require.Len(t, tasks, 1)
		task := tasks[0]
		assert.Equal(t, env.sales.ID, task.AssignedToID, "the owner at execution time")
		require.NotNil(t, task.LeadID)
		assert.Equal(t, lead.ID, *task.LeadID)
		assert.Nil(t, task.CustomerID)
		require.NotNil(t, task.DueDate)
		assert.WithinDuration(t, env.clock.Now().UTC().Add(48*time.Hour), *task.DueDate, time.Second, "due due_in_days after execution, UTC")
		assert.Equal(t, fmt.Sprintf("Follow up with lead %d", lead.ID), task.Title)
		assert.Equal(t, "Status new, source trade_show", task.Description)
		for _, personal := range []string{automationCanaryFirstName, automationCanaryLastName, automationCanaryPhone, lead.Email} {
			assert.NotContains(t, task.Title+" "+task.Description, personal, "a task carries no personal data of the lead")
		}
		assert.Equal(t, models.TaskStatusPending, task.Status)
		assert.EqualValues(t, task.ID, steps[0].Details["task_id"])
		assert.EqualValues(t, lead.ID, steps[0].Details["lead_id"])

		assert.Equal(t, env.other.ID, env.lead(lead.ID).OwnerID, "assign_owner changed the owner")
		assert.EqualValues(t, env.sales.ID, steps[1].Details["from_owner_id"])
		assert.EqualValues(t, env.other.ID, steps[1].Details["to_owner_id"])
		events := env.events()
		require.Len(t, events, eventsBefore+1, "the owner change recorded one event")
		assert.Equal(t, models.AutomationOriginAutomation, events[len(events)-1].Origin)
		assert.Equal(t, models.AutomationEventLeadUpdated, events[len(events)-1].EventType)
		runsBefore := len(env.allRuns())
		env.dispatch()
		assert.Len(t, env.allRuns(), runsBefore, "an automation-originated event starts no run")

		notifications := env.notificationsList()
		require.Len(t, notifications, 1)
		assert.Equal(t, env.other.ID, notifications[0].UserID)
		assert.Equal(t, "NOTIFY-TITLE-CANARY "+automationCanaryLastName, notifications[0].Title)
		assert.Equal(t, models.AutomationEntityLead, notifications[0].EntityType)
		assert.Equal(t, lead.ID, notifications[0].EntityID)
		require.NotNil(t, notifications[0].RunID)
		assert.Equal(t, run.ID, *notifications[0].RunID)

		sent := env.mailer.messages()
		require.Len(t, sent, 1, "Mailer.Send was called exactly once")
		assert.Equal(t, env.other.Email, sent[0].To, "the staff owner's address at the time of the step, after the assign")
		assert.Equal(t, "EMAIL-SUBJECT-CANARY "+automationCanaryFirstName, sent[0].Subject)
		assert.EqualValues(t, env.other.ID, steps[3].Details["recipient_id"])
	})

	t.Run("a deal run links the task to the customer, else the lead, never both", func(t *testing.T) {
		rule := env.createRule(models.AutomationEntityDeal, models.AutomationEventDealCreated, models.AutomationTrigger{},
			dealTaskStep(models.AutomationRecipientUser, env.other.ID, 0))
		customer := env.createCustomer()
		lead := env.createLead(env.sales, models.LeadStatusQualified)
		withCustomer := env.createDeal(env.sales, models.DealStageProposal, 100, "EUR", &customer.ID, &lead.ID)
		withLead := env.createDeal(env.sales, models.DealStageProposal, 100, "EUR", nil, &lead.ID)
		bare := env.createDeal(env.sales, models.DealStageProposal, 100, "EUR", nil, nil)
		for _, deal := range []*models.Deal{withCustomer, withLead, bare} {
			env.recordEvent(models.AutomationEntityDeal, deal.ID, models.AutomationEventDealCreated, "", "proposal", models.AutomationOriginUser)
		}
		tasksBefore := len(env.tasksList())
		env.dispatch()
		env.executeDue()
		tasks := env.tasksList()[tasksBefore:]
		require.Len(t, tasks, 3)
		for _, task := range tasks {
			assert.Equal(t, env.other.ID, task.AssignedToID)
			require.NotNil(t, task.DueDate)
			assert.WithinDuration(t, env.clock.Now().UTC(), *task.DueDate, time.Second, "due_in_days 0 is due at execution")
		}
		assert.NotNil(t, tasks[0].CustomerID)
		assert.Nil(t, tasks[0].LeadID, "a deal with a customer links the customer only")
		assert.Nil(t, tasks[1].CustomerID)
		require.NotNil(t, tasks[1].LeadID)
		assert.Equal(t, lead.ID, *tasks[1].LeadID)
		assert.Nil(t, tasks[2].CustomerID)
		assert.Nil(t, tasks[2].LeadID)
		for _, run := range env.runsOf(rule.ID) {
			assert.Equal(t, models.AutomationRunCompleted, env.run(run.ID).Status)
		}
	})

	t.Run("a fault after the effect rolls the step back and the retry performs it once", func(t *testing.T) {
		rule := env.leadRule(models.AutomationEventLeadCreated, taskStep(models.AutomationRecipientOwner, 0, 1), notifyStep(models.AutomationRecipientOwner, 0))
		lead := env.createLead(env.sales, models.LeadStatusNew)
		run := env.startRun(rule, lead)
		tasksBefore, notificationsBefore := env.count(&models.Task{}), env.count(&models.Notification{})

		faults := 0
		env.engine.afterEffect = func(runID uint, position int) error {
			if runID == run.ID && position == 1 && faults == 0 {
				faults++
				return errors.New("injected fault after the effect")
			}
			return nil
		}
		env.executeDue()
		env.engine.afterEffect = nil
		assert.Equal(t, 1, faults)
		assert.Equal(t, tasksBefore, env.count(&models.Task{}), "the task was rolled back with the step")
		assert.Empty(t, env.stepsOf(run.ID), "and the log row")
		released := env.run(run.ID)
		assert.Equal(t, models.AutomationRunPending, released.Status, "the run was handed back")
		assert.Equal(t, 1, released.NextPosition, "at the same position")
		assert.Equal(t, 1, released.Attempts)

		env.executeDue()
		assert.Equal(t, tasksBefore+1, env.count(&models.Task{}), "exactly one task on the next execution")
		assert.Equal(t, notificationsBefore+1, env.count(&models.Notification{}))
		assert.Equal(t, models.AutomationRunCompleted, env.run(run.ID).Status)
		assert.Len(t, env.stepsOf(run.ID), 2)
	})

	t.Run("an inactive or erased recipient is skipped with recipient_unavailable", func(t *testing.T) {
		recipient := env.createUser("leaver", models.RoleSales, true)
		rule := env.leadRule(models.AutomationEventLeadCreated,
			notifyStep(models.AutomationRecipientUser, recipient.ID),
			taskStep(models.AutomationRecipientUser, recipient.ID, 1),
			emailStep(models.AutomationRecipientUser, recipient.ID),
			notifyStep(models.AutomationRecipientOwner, 0))
		require.NoError(t, env.users.Delete(recipient.ID), "the recipient is erased after the rule was saved")
		lead := env.createLead(env.sales, models.LeadStatusNew)
		run := env.startRun(rule, lead)
		tasksBefore, notificationsBefore, mailsBefore := env.count(&models.Task{}), env.count(&models.Notification{}), len(env.mailer.messages())
		env.executeDue()
		assert.Equal(t, models.AutomationRunCompleted, env.run(run.ID).Status)
		steps := env.stepsOf(run.ID)
		require.Len(t, steps, 4)
		for _, step := range steps[:3] {
			assert.Equal(t, models.AutomationOutcomeSkipped, step.Outcome, "position %d", step.Position)
			assert.Equal(t, models.AutomationErrorRecipientUnavailable, step.ErrorCode, "position %d", step.Position)
		}
		assert.Equal(t, models.AutomationOutcomeSucceeded, steps[3].Outcome, "the owner notification still goes out")
		assert.Equal(t, tasksBefore, env.count(&models.Task{}))
		assert.Equal(t, notificationsBefore+1, env.count(&models.Notification{}))
		assert.Len(t, env.mailer.messages(), mailsBefore)

		// The assignee is active when the rule is saved and deactivated
		// afterwards.
		inactive := env.createUser("inactive", models.RoleSales, true)
		rule = env.leadRule(models.AutomationEventLeadCreated, assignStep(inactive.ID))
		require.NoError(t, env.db.Model(&models.User{}).Where("id = ?", inactive.ID).Update("is_active", false).Error)
		run = env.startRun(rule, lead)
		env.executeDue()
		steps = env.stepsOf(run.ID)
		require.Len(t, steps, 1)
		assert.Equal(t, models.AutomationOutcomeSkipped, steps[0].Outcome)
		assert.Equal(t, models.AutomationErrorRecipientUnavailable, steps[0].ErrorCode)
		assert.Equal(t, env.sales.ID, env.lead(lead.ID).OwnerID, "no owner change")
	})

	t.Run("send_email is two transactions around one send", func(t *testing.T) {
		rule := env.leadRule(models.AutomationEventLeadCreated, emailStep(models.AutomationRecipientOwner, 0), notifyStep(models.AutomationRecipientOwner, 0))
		lead := env.createLead(env.sales, models.LeadStatusNew)

		t.Run("a mailer failure is recorded as mail_failed and the run goes on", func(t *testing.T) {
			env.mailer.reset(errors.New("smtp down"), nil)
			defer env.mailer.reset(nil, nil)
			run := env.startRun(rule, lead)
			env.executeDue()
			steps := env.stepsOf(run.ID)
			require.Len(t, steps, 2)
			assert.Equal(t, models.AutomationOutcomeFailed, steps[0].Outcome)
			assert.Equal(t, models.AutomationErrorMailFailed, steps[0].ErrorCode)
			assert.Equal(t, models.AutomationOutcomeSucceeded, steps[1].Outcome)
			assert.Equal(t, models.AutomationRunCompleted, env.run(run.ID).Status)
		})

		t.Run("the first half sees the committed sending row before the send", func(t *testing.T) {
			env.mailer.reset(nil, nil)
			run := env.startRun(rule, lead)
			var seen *models.AutomationRunStep
			env.engine.beforeSend = func(runID uint, position int) {
				if runID == run.ID {
					seen, _ = env.repo.GetRunStep(run.ID, position)
				}
			}
			env.executeDue()
			env.engine.beforeSend = nil
			require.NotNil(t, seen, "the sending row was committed before Mailer.Send")
			assert.Equal(t, models.AutomationOutcomeSending, seen.Outcome)
			assert.Len(t, env.mailer.messages(), 1)
			assert.Equal(t, env.sales.Email, env.mailer.messages()[0].To)
			steps := env.stepsOf(run.ID)
			assert.Equal(t, models.AutomationOutcomeSucceeded, steps[0].Outcome)
			assert.Equal(t, models.AutomationRunCompleted, env.run(run.ID).Status)

			// A second executor cannot record a second sending row at the
			// position: the (run_id, position) key is unique.
			err := env.repo.CreateRunStep(&models.AutomationRunStep{RunID: run.ID, Position: 1, StepType: models.AutomationStepSendEmail, Outcome: models.AutomationOutcomeSending, ExecutedAt: env.clock.Now()})
			require.Error(t, err)
			assert.Len(t, env.stepsOf(run.ID), 2)
		})

		t.Run("a fault after the send never sends again for that position", func(t *testing.T) {
			env.mailer.reset(nil, nil)
			run := env.startRun(rule, lead)
			faulted := false
			env.engine.afterSend = func(runID uint, position int) error {
				if runID == run.ID && !faulted {
					faulted = true
					return errors.New("injected fault after the send")
				}
				return nil
			}
			env.executeDue()
			env.engine.afterSend = nil
			require.True(t, faulted)
			require.Len(t, env.mailer.messages(), 1)
			steps := env.stepsOf(run.ID)
			require.Len(t, steps, 1)
			assert.Equal(t, models.AutomationOutcomeSending, steps[0].Outcome, "the row is left in sending")
			assert.Equal(t, models.AutomationRunPending, env.run(run.ID).Status, "the run was handed back")

			env.executeDue()
			assert.Len(t, env.mailer.messages(), 1, "the mail is not sent a second time")
			steps = env.stepsOf(run.ID)
			require.Len(t, steps, 1)
			assert.Equal(t, models.AutomationOutcomeFailed, steps[0].Outcome)
			assert.Equal(t, models.AutomationErrorEmailUnconfirmed, steps[0].ErrorCode)
			failed := env.run(run.ID)
			assert.Equal(t, models.AutomationRunFailed, failed.Status)
			assert.Equal(t, models.AutomationErrorEmailUnconfirmed, failed.ErrorCode)
		})

		t.Run("a cancel between the two halves stays cancelled with the row finalized", func(t *testing.T) {
			env.mailer.reset(nil, nil)
			run := env.startRun(rule, lead)
			env.engine.beforeSend = func(runID uint, position int) {
				if runID == run.ID {
					_, err := env.svc.CancelRun(run.ID)
					require.NoError(t, err)
				}
			}
			env.executeDue()
			env.engine.beforeSend = nil
			assert.Len(t, env.mailer.messages(), 1, "the send already in flight went out")
			steps := env.stepsOf(run.ID)
			require.Len(t, steps, 1, "no further step ran")
			assert.Equal(t, models.AutomationOutcomeSucceeded, steps[0].Outcome, "the row is finalized")
			cancelled := env.run(run.ID)
			assert.Equal(t, models.AutomationRunCancelled, cancelled.Status)
			assert.Equal(t, models.AutomationCancelManual, cancelled.CancelReason)
			assert.Equal(t, 1, cancelled.NextPosition, "the advance matched nothing")
		})
	})

	if !isMySQL(db) {
		return
	}

	// The paused-step races need a second connection, so they are MySQL
	// only: a write committed after the step's first read must be seen by
	// the conditional advance, which a plain read under REPEATABLE READ
	// would not.
	t.Run("a cancel, a rule change or an erasure committed after the first read leaves no effect", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			step   models.AutomationStep
			commit func(t *testing.T, env *automationEnv, rule *models.AutomationRule, lead *models.Lead, runID uint) func()
			reason string
		}{
			{"manual cancel", taskStep(models.AutomationRecipientOwner, 0, 1),
				func(t *testing.T, env *automationEnv, _ *models.AutomationRule, _ *models.Lead, runID uint) func() {
					_, err := env.svc.CancelRun(runID)
					require.NoError(t, err)
					return nil
				}, models.AutomationCancelManual},
			{"rule change", notifyStep(models.AutomationRecipientOwner, 0),
				func(t *testing.T, env *automationEnv, rule *models.AutomationRule, _ *models.Lead, _ uint) func() {
					_, err := env.svc.UpdateRule(rule.ID, &models.AutomationRule{Name: "Changed", EntityType: rule.EntityType, TriggerType: rule.TriggerType,
						Enabled: true, Steps: []models.AutomationStep{waitStep(5)}})
					require.NoError(t, err)
					return nil
				}, models.AutomationCancelRuleChanged},
			{"lead erasure", assignStep(env.other.ID),
				func(t *testing.T, env *automationEnv, _ *models.AutomationRule, lead *models.Lead, _ uint) func() {
					// The erasure blocks on the lead row the step holds under
					// FOR UPDATE; it is released only when the step's own
					// advance deadlocks against the cancelled run row, so it
					// runs on its own goroutine and is joined afterwards.
					done := make(chan error, 1)
					go func() { done <- env.leads.Delete(lead.ID) }()
					time.Sleep(500 * time.Millisecond)
					return func() {
						select {
						case err := <-done:
							require.NoError(t, err, "the erasure commits")
						case <-time.After(automationTestTimeout):
							t.Fatal("the erasure did not finish")
						}
					}
				}, models.AutomationCancelRecordDeleted},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rule := env.leadRule(models.AutomationEventLeadCreated, tc.step, notifyStep(models.AutomationRecipientOwner, 0))
				lead := env.createLead(env.sales, models.LeadStatusNew)
				run := env.startRun(rule, lead)
				tasksBefore, notificationsBefore := env.count(&models.Task{}), env.count(&models.Notification{})

				var once sync.Once
				var join func()
				env.engine.afterRecordRead = func(runID uint, position int) {
					if runID != run.ID {
						return
					}
					once.Do(func() { join = tc.commit(t, env, rule, lead, run.ID) })
				}
				env.executeDue()
				env.engine.afterRecordRead = nil
				if join != nil {
					join()
				}

				stored := env.run(run.ID)
				assert.Equal(t, models.AutomationRunCancelled, stored.Status)
				assert.Equal(t, tc.reason, stored.CancelReason)
				assert.Empty(t, env.stepsOf(run.ID), "no run-step row")
				assert.Equal(t, tasksBefore, env.count(&models.Task{}), "no task")
				assert.Equal(t, notificationsBefore, env.count(&models.Notification{}), "no notification")
				if tc.name != "lead erasure" {
					assert.Equal(t, env.sales.ID, env.lead(lead.ID).OwnerID, "no owner change")
				} else {
					erased := env.lead(lead.ID)
					assert.True(t, erased.DeletedAt.Valid)
					assert.Equal(t, env.sales.ID, erased.OwnerID, "no owner change")
				}
			})
		}
	})
}
