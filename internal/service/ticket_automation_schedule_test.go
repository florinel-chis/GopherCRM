package service

// AC-8 of #242: a ticket schedule rule makes one pass per UTC day over the
// live tickets matching filters.ticket_status and stale_days, at most 500 per
// tick from the persisted cursor, with dedupe key
// sch:<rule_id>:<YYYY-MM-DD>:<ticket_id>, starting each ticket at most once
// per day.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutomationTicketScheduleSQLite(t *testing.T) {
	runAutomationTicketSchedule(t, openAutomationSQLite(t))
}

func TestAutomationTicketScheduleMySQL(t *testing.T) {
	runAutomationTicketSchedule(t, openAutomationMySQL(t, 4))
}

func runAutomationTicketSchedule(t *testing.T, db *gorm.DB) {
	env := newTicketAutomationEnv(t, db)
	const matching = 503
	day := time.Date(2026, time.November, 2, 0, 0, 0, 0, time.UTC)
	stale := day.Add(-10 * 24 * time.Hour)

	// 503 resolved tickets untouched for ten days, and the ones that must not
	// match: another status, recently updated, deleted.
	tickets := make([]models.Ticket, 0, matching)
	for i := 0; i < matching; i++ {
		ticket := models.Ticket{Title: fmt.Sprintf("Batch %d", i), Description: "d", Status: models.TicketStatusResolved,
			Priority: models.TicketPriorityMedium, CustomerID: env.customer.ID}
		ticket.CreatedAt, ticket.UpdatedAt = stale, stale
		tickets = append(tickets, ticket)
	}
	require.NoError(t, env.db.CreateInBatches(&tickets, 200).Error)
	matchingIDs := map[uint]bool{}
	for _, ticket := range tickets {
		matchingIDs[ticket.ID] = true
	}
	other := models.Ticket{Title: "Open", Description: "d", Status: models.TicketStatusOpen, Priority: models.TicketPriorityLow, CustomerID: env.customer.ID}
	other.CreatedAt, other.UpdatedAt = stale, stale
	recent := models.Ticket{Title: "Recent", Description: "d", Status: models.TicketStatusResolved, Priority: models.TicketPriorityLow, CustomerID: env.customer.ID}
	recent.CreatedAt, recent.UpdatedAt = day, day
	deleted := models.Ticket{Title: "Deleted", Description: "d", Status: models.TicketStatusResolved, Priority: models.TicketPriorityLow, CustomerID: env.customer.ID}
	deleted.CreatedAt, deleted.UpdatedAt = stale, stale
	require.NoError(t, env.db.Create(&other).Error)
	require.NoError(t, env.db.Create(&recent).Error)
	require.NoError(t, env.db.Create(&deleted).Error)
	require.NoError(t, env.db.Delete(&models.Ticket{}, deleted.ID).Error)

	rule := env.ticketRule(models.AutomationTriggerSchedule, models.AutomationTrigger{HourUTC: 9, StaleDays: 3,
		Filters: &models.AutomationScheduleFilter{TicketStatus: string(models.TicketStatusResolved)}},
		ticketNotifyStep(models.AutomationRecipientOwner, 0))
	schedules := func() {
		t.Helper()
		require.NoError(t, env.engine.RunSchedules(context.Background()))
	}
	passRuns := func(dayKey string) map[uint]int {
		counts := map[uint]int{}
		for _, run := range env.runsOf(rule.ID) {
			if run.DedupeKey == fmt.Sprintf("sch:%d:%s:%d", rule.ID, dayKey, run.EntityID) {
				counts[run.EntityID]++
			}
		}
		return counts
	}

	t.Run("before hour_utc nothing starts", func(t *testing.T) {
		env.clock.Set(day.Add(8 * time.Hour))
		schedules()
		assert.Empty(t, env.runsOf(rule.ID))
	})

	t.Run("two ticks hand out 500 and 3 runs and complete the day", func(t *testing.T) {
		env.clock.Set(day.Add(9*time.Hour + 1*time.Minute))
		schedules()
		first := env.runsOf(rule.ID)
		require.Len(t, first, models.AutomationScheduleBatchSize, "at most 500 per tick")
		stored, err := env.repo.GetRuleByID(rule.ID)
		require.NoError(t, err)
		require.NotNil(t, stored.SchedulePassOn)
		assert.Equal(t, "2026-11-02", *stored.SchedulePassOn)
		assert.Equal(t, first[len(first)-1].EntityID, stored.ScheduleCursor, "the cursor is the last id handed out")
		assert.Nil(t, stored.ScheduleCompletedOn)

		schedules()
		counts := passRuns("2026-11-02")
		assert.Len(t, counts, matching)
		for id, n := range counts {
			assert.True(t, matchingIDs[id], "ticket %d matches the filter", id)
			assert.Equal(t, 1, n)
		}
		for _, run := range env.runsOf(rule.ID) {
			assert.Equal(t, models.AutomationEntityTicket, run.EntityType)
			assert.Equal(t, models.AutomationRunPending, run.Status)
			assert.Nil(t, run.EventID)
		}
		stored, err = env.repo.GetRuleByID(rule.ID)
		require.NoError(t, err)
		require.NotNil(t, stored.ScheduleCompletedOn)
		assert.Equal(t, "2026-11-02", *stored.ScheduleCompletedOn)
	})

	t.Run("a repeated tick the same day starts nothing", func(t *testing.T) {
		before := len(env.runsOf(rule.ID))
		env.clock.Set(day.Add(15 * time.Hour))
		schedules()
		assert.Len(t, env.runsOf(rule.ID), before)
		for _, excluded := range []uint{other.ID, recent.ID, deleted.ID} {
			assert.Zero(t, passRuns("2026-11-02")[excluded])
		}
	})

	t.Run("the next UTC day makes a new pass with its own keys", func(t *testing.T) {
		env.clock.Set(day.Add(24*time.Hour + 9*time.Hour))
		schedules()
		schedules()
		next := passRuns("2026-11-03")
		assert.Len(t, next, matching)
		for _, n := range next {
			assert.Equal(t, 1, n)
		}
		assert.Len(t, env.runsOf(rule.ID), 2*matching)
	})

	t.Run("a scheduled run executes against the ticket", func(t *testing.T) {
		// Assign one matching ticket so its notification has a recipient.
		var target models.Ticket
		require.NoError(t, env.db.Where("id IN ?", ticketScheduleIDs(matchingIDs)).Order("id ASC").First(&target).Error)
		require.NoError(t, env.db.Model(&models.Ticket{}).Where("id = ?", target.ID).
			UpdateColumn("assigned_to_id", env.support.ID).Error)
		var run models.AutomationRun
		require.NoError(t, env.db.Where("rule_id = ? AND entity_id = ? AND dedupe_key = ?", rule.ID, target.ID,
			fmt.Sprintf("sch:%d:2026-11-03:%d", rule.ID, target.ID)).First(&run).Error)
		// Only this run is due: the others are moved out of the way.
		require.NoError(t, env.db.Model(&models.AutomationRun{}).Where("id <> ?", run.ID).
			Updates(map[string]interface{}{"status": models.AutomationRunCancelled, "cancel_reason": models.AutomationCancelManual}).Error)
		env.executeDue()
		assert.Equal(t, models.AutomationRunCompleted, env.run(run.ID).Status)
		notifications := env.notificationsList()
		require.Len(t, notifications, 1)
		assert.Equal(t, env.support.ID, notifications[0].UserID)
		assert.Equal(t, models.AutomationEntityTicket, notifications[0].EntityType)
		assert.Equal(t, target.ID, notifications[0].EntityID)
	})
}

func ticketScheduleIDs(set map[uint]bool) []uint {
	out := make([]uint, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}
