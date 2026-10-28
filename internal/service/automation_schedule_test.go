package service

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

// AC-9: a schedule rule makes one pass per UTC day, starting at the first
// tick at or after hour_utc (so a process that was down at the hour catches
// up later the same day); a day missed entirely is not backfilled; each tick
// handles at most 500 matching live records above the cursor in id order,
// inserts one run per record with dedupe key sch:<rule_id>:<day>:<entity_id>
// and advances the cursor conditionally; a batch shorter than 500 completes
// the day; 1,201 records get exactly one run each over three ticks; a racing
// worker or a repeat tick adds nothing.

func TestAutomationScheduleTriggerSQLite(t *testing.T) {
	runAutomationScheduleTrigger(t, openAutomationSQLite(t))
}

func TestAutomationScheduleTriggerMySQL(t *testing.T) {
	runAutomationScheduleTrigger(t, openAutomationMySQL(t, 4))
}

func runAutomationScheduleTrigger(t *testing.T, db *gorm.DB) {
	env := newAutomationEnv(t, db)
	const matchingLeads = 1201
	day := time.Date(2026, time.November, 2, 0, 0, 0, 0, time.UTC)

	// 1,201 new leads, a few that do not match the filter and one erased.
	leads := make([]models.Lead, 0, matchingLeads)
	for i := 0; i < matchingLeads; i++ {
		leads = append(leads, models.Lead{FirstName: "Batch", LastName: fmt.Sprintf("Lead %d", i), Email: fmt.Sprintf("batch-%d@example.com", i),
			Status: models.LeadStatusNew, OwnerID: env.sales.ID})
	}
	require.NoError(t, env.db.CreateInBatches(&leads, 200).Error)
	contacted := env.createLead(env.sales, models.LeadStatusContacted)
	erased := env.createLead(env.sales, models.LeadStatusNew)
	require.NoError(t, env.db.Delete(&models.Lead{}, erased.ID).Error)

	rule := env.createRule(models.AutomationEntityLead, models.AutomationTriggerSchedule,
		models.AutomationTrigger{HourUTC: 9, Filters: &models.AutomationScheduleFilter{Status: string(models.LeadStatusNew)}},
		notifyStep(models.AutomationRecipientOwner, 0))
	passKeys := func(dayKey string) map[uint]int {
		counts := map[uint]int{}
		for _, run := range env.runsOf(rule.ID) {
			if run.DedupeKey == fmt.Sprintf("sch:%d:%s:%d", rule.ID, dayKey, run.EntityID) {
				counts[run.EntityID]++
			}
		}
		return counts
	}
	schedules := func() {
		t.Helper()
		require.NoError(t, env.engine.RunSchedules(context.Background()))
	}
	ruleRow := func() *models.AutomationRule {
		t.Helper()
		stored, err := env.repo.GetRuleByID(rule.ID)
		require.NoError(t, err)
		return stored
	}

	t.Run("before hour_utc nothing starts", func(t *testing.T) {
		env.clock.Set(day.Add(8*time.Hour + 30*time.Minute))
		schedules()
		assert.Empty(t, env.runsOf(rule.ID))
		assert.Nil(t, ruleRow().SchedulePassOn)
	})

	t.Run("three ticks hand out 500, 500 and 201 runs and complete the day", func(t *testing.T) {
		env.clock.Set(day.Add(9*time.Hour + 5*time.Minute))
		schedules()
		runs := env.runsOf(rule.ID)
		require.Len(t, runs, 500)
		assert.Equal(t, leads[499].ID, ruleRow().ScheduleCursor, "the cursor is the last id handed out")
		require.NotNil(t, ruleRow().SchedulePassOn)
		assert.Equal(t, "2026-11-02", *ruleRow().SchedulePassOn)
		assert.Nil(t, ruleRow().ScheduleCompletedOn)
		for i, run := range runs {
			assert.Equal(t, leads[i].ID, run.EntityID, "id order")
			assert.Equal(t, fmt.Sprintf("sch:%d:2026-11-02:%d", rule.ID, run.EntityID), run.DedupeKey)
			assert.Equal(t, models.AutomationRunPending, run.Status)
			assert.Equal(t, rule.Version, run.RuleVersion)
			assert.Nil(t, run.EventID)
		}

		env.clock.Advance(30 * time.Minute)
		schedules()
		assert.Len(t, env.runsOf(rule.ID), 1000)
		assert.Nil(t, ruleRow().ScheduleCompletedOn)

		env.clock.Advance(30 * time.Minute)
		schedules()
		assert.Len(t, env.runsOf(rule.ID), 1201)
		require.NotNil(t, ruleRow().ScheduleCompletedOn)
		assert.Equal(t, "2026-11-02", *ruleRow().ScheduleCompletedOn)

		counts := passKeys("2026-11-02")
		assert.Len(t, counts, matchingLeads, "every matching record got a run")
		for id, n := range counts {
			assert.Equalf(t, 1, n, "lead %d got %d runs", id, n)
		}
		_, started := counts[contacted.ID]
		assert.False(t, started, "a lead outside the filter is not visited")
		_, started = counts[erased.ID]
		assert.False(t, started, "a deleted lead is not visited")

		// A repeat tick after the pass is complete adds nothing.
		env.clock.Advance(time.Hour)
		schedules()
		assert.Len(t, env.runsOf(rule.ID), 1201)

		// A duplicate dedupe insert is ignored on both engines.
		first := env.runsOf(rule.ID)[0]
		inserted, err := env.repo.CreateRunIgnoringDuplicate(&models.AutomationRun{RuleID: rule.ID, RuleVersion: rule.Version, EntityType: first.EntityType,
			EntityID: first.EntityID, DedupeKey: first.DedupeKey, Status: models.AutomationRunPending, NextPosition: 1})
		require.NoError(t, err)
		assert.False(t, inserted)
		assert.Len(t, env.runsOf(rule.ID), 1201)
	})

	t.Run("a day missed entirely is not backfilled and a late tick catches up the same day", func(t *testing.T) {
		// The process was down on the 3rd; on the 4th it comes back at 15:00.
		env.clock.Set(day.AddDate(0, 0, 2).Add(15 * time.Hour))
		schedules()
		assert.Empty(t, passKeys("2026-11-03"), "no pass for the day the process was down")
		assert.Len(t, passKeys("2026-11-04"), 500, "the pass for today started at the first tick after hour_utc")
		assert.Equal(t, "2026-11-04", *ruleRow().SchedulePassOn)
		assert.Nil(t, ruleRow().ScheduleCompletedOn)
	})

	t.Run("a racing worker creates no additional runs", func(t *testing.T) {
		// Both workers read the same rule row, including its cursor, and hand
		// out the same batch; the conditional cursor update lets one commit.
		snapshot := *ruleRow()
		other := NewAutomationEngine(env.repo, env.notifications, env.tasks, env.users, env.mailer, env.txManager)
		other.SetClock(env.clock.Now)
		now := env.clock.Now().UTC()
		today := now.Format("2006-01-02")
		before := len(env.runsOf(rule.ID))
		errA, errB := raceTwo(t,
			func() error { return env.engine.runScheduleBatch(snapshot, now, today) },
			func() error { return other.runScheduleBatch(snapshot, now, today) },
		)
		winners := 0
		for _, err := range []error{errA, errB} {
			if err == nil {
				winners++
			} else {
				assert.ErrorIs(t, err, errScheduleCursorMoved)
			}
		}
		assert.Equal(t, 1, winners)
		assert.Len(t, env.runsOf(rule.ID), before+500)
		for id, n := range passKeys(today) {
			assert.Equalf(t, 1, n, "lead %d got %d runs", id, n)
		}
	})

	t.Run("stale_days narrows the pass to records not updated since", func(t *testing.T) {
		stale := env.createRule(models.AutomationEntityLead, models.AutomationTriggerSchedule,
			models.AutomationTrigger{HourUTC: 0, StaleDays: 3, Filters: &models.AutomationScheduleFilter{Status: string(models.LeadStatusContacted)}},
			notifyStep(models.AutomationRecipientOwner, 0))
		old := env.createLead(env.sales, models.LeadStatusContacted)
		require.NoError(t, env.db.Model(&models.Lead{}).Where("id = ?", old.ID).Update("updated_at", env.clock.Now().UTC().Add(-5*24*time.Hour)).Error)
		fresh := env.createLead(env.sales, models.LeadStatusContacted)
		require.NoError(t, env.db.Model(&models.Lead{}).Where("id = ?", fresh.ID).Update("updated_at", env.clock.Now().UTC().Add(-time.Hour)).Error)
		require.NoError(t, env.db.Model(&models.Lead{}).Where("id = ?", contacted.ID).Update("updated_at", env.clock.Now().UTC().Add(-time.Hour)).Error)
		schedules()
		runs := env.runsOf(stale.ID)
		require.Len(t, runs, 1)
		assert.Equal(t, old.ID, runs[0].EntityID)
	})
}
