package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AC-5: the dispatcher claims an unprocessed event with a conditional update
// and, in the same transaction, creates exactly one run per enabled,
// matching rule with the rule version and the dedupe key
// evt:<event_id>:rule:<rule_id>. Runs are inserted with
// clause.OnConflict{DoNothing: true}; a duplicate dedupe key is neither an
// error nor a row on either engine. Two dispatchers racing for one event
// produce one set of runs. Disabled or deleted rules, non-matching from/to
// filters and events of origin automation start no run.

func TestAutomationDispatchSQLite(t *testing.T) {
	runAutomationDispatch(t, openAutomationSQLite(t))
}

func TestAutomationDispatchMySQL(t *testing.T) {
	runAutomationDispatch(t, openAutomationMySQL(t, 4))
}

func runAutomationDispatch(t *testing.T, db *gorm.DB) {
	env := newAutomationEnv(t, db)
	lead := env.createLead(env.sales, models.LeadStatusNew)

	matching := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadStatusChanged,
		models.AutomationTrigger{To: string(models.LeadStatusQualified)}, notifyStep(models.AutomationRecipientOwner, 0))
	anyChange := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadStatusChanged,
		models.AutomationTrigger{}, notifyStep(models.AutomationRecipientOwner, 0))
	created := env.leadRule(models.AutomationEventLeadCreated, notifyStep(models.AutomationRecipientOwner, 0))
	wrongFrom := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadStatusChanged,
		models.AutomationTrigger{From: string(models.LeadStatusContacted)}, notifyStep(models.AutomationRecipientOwner, 0))
	disabled := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadStatusChanged,
		models.AutomationTrigger{}, notifyStep(models.AutomationRecipientOwner, 0))
	_, err := env.svc.SetEnabled(disabled.ID, false)
	require.NoError(t, err)
	deleted := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadStatusChanged,
		models.AutomationTrigger{}, notifyStep(models.AutomationRecipientOwner, 0))
	require.NoError(t, env.svc.DeleteRule(deleted.ID))

	t.Run("the SQL of the dedupe insert", func(t *testing.T) {
		run := models.AutomationRun{RuleID: 1, EntityType: "lead", EntityID: 1, DedupeKey: "probe", Status: "pending", NextPosition: 1}
		stmt := db.Session(&gorm.Session{DryRun: true}).Clauses(clause.OnConflict{DoNothing: true}).Omit("Rule", "Steps").Create(&run).Statement
		sql := strings.ToUpper(stmt.SQL.String())
		if isMySQL(db) {
			assert.Contains(t, sql, "ON DUPLICATE KEY UPDATE `ID`=`ID`")
		} else {
			assert.Contains(t, sql, "ON CONFLICT DO NOTHING")
		}
	})

	t.Run("one run per enabled matching rule, claimed exactly once", func(t *testing.T) {
		event := env.recordEvent(models.AutomationEntityLead, lead.ID, models.AutomationEventLeadStatusChanged,
			string(models.LeadStatusNew), string(models.LeadStatusQualified), models.AutomationOriginUser)
		assert.Equal(t, 2, env.dispatch())

		runs := env.allRuns()
		require.Len(t, runs, 2)
		byRule := map[uint]models.AutomationRun{}
		for _, run := range runs {
			byRule[run.RuleID] = run
		}
		for _, rule := range []*models.AutomationRule{matching, anyChange} {
			run, ok := byRule[rule.ID]
			require.Truef(t, ok, "rule %d started a run", rule.ID)
			assert.Equal(t, rule.Version, run.RuleVersion)
			assert.Equal(t, models.AutomationRunPending, run.Status)
			assert.Equal(t, 1, run.NextPosition)
			assert.Equal(t, lead.ID, run.EntityID)
			require.NotNil(t, run.EventID)
			assert.Equal(t, event.ID, *run.EventID)
			assert.Equal(t, fmt.Sprintf("evt:%d:rule:%d", event.ID, rule.ID), run.DedupeKey)
		}
		for _, rule := range []*models.AutomationRule{created, wrongFrom, disabled, deleted} {
			_, started := byRule[rule.ID]
			assert.Falsef(t, started, "rule %d must not start a run", rule.ID)
		}

		var stored models.AutomationEvent
		require.NoError(t, db.First(&stored, event.ID).Error)
		assert.NotNil(t, stored.ProcessedAt, "the claim stamped processed_at")

		assert.Equal(t, 0, env.dispatch(), "a claimed event is not dispatched again")
		assert.Len(t, env.allRuns(), 2)
	})

	t.Run("a duplicate dedupe key is no error and no row", func(t *testing.T) {
		before := len(env.allRuns())
		existing := env.allRuns()[0]
		duplicate := &models.AutomationRun{RuleID: existing.RuleID, RuleVersion: existing.RuleVersion, EntityType: existing.EntityType,
			EntityID: existing.EntityID, DedupeKey: existing.DedupeKey, Status: models.AutomationRunPending, NextPosition: 1}
		inserted, err := env.repo.CreateRunIgnoringDuplicate(duplicate)
		require.NoError(t, err)
		assert.False(t, inserted)
		assert.Len(t, env.allRuns(), before)
	})

	t.Run("an automation-originated event is claimed and starts nothing", func(t *testing.T) {
		before := len(env.allRuns())
		event := env.recordEvent(models.AutomationEntityLead, lead.ID, models.AutomationEventLeadStatusChanged,
			string(models.LeadStatusNew), string(models.LeadStatusQualified), models.AutomationOriginAutomation)
		assert.Equal(t, 0, env.dispatch())
		assert.Len(t, env.allRuns(), before)
		var stored models.AutomationEvent
		require.NoError(t, db.First(&stored, event.ID).Error)
		assert.NotNil(t, stored.ProcessedAt)
	})

	t.Run("two dispatchers racing for one event produce one set of runs", func(t *testing.T) {
		for round := 0; round < 4; round++ {
			before := len(env.allRuns())
			event := env.recordEvent(models.AutomationEntityLead, lead.ID, models.AutomationEventLeadStatusChanged,
				string(models.LeadStatusNew), string(models.LeadStatusQualified), models.AutomationOriginUser)
			other := NewAutomationEngine(env.repo, env.notifications, env.tasks, env.users, env.mailer, env.txManager)
			var first, second int
			errA, errB := raceTwo(t,
				func() error {
					var err error
					first, err = env.engine.DispatchEvent(*event)
					return err
				},
				func() error {
					var err error
					second, err = other.DispatchEvent(*event)
					return err
				},
			)
			require.NoError(t, errA)
			require.NoError(t, errB)
			assert.Equal(t, 2, first+second, "exactly one dispatcher started the two runs")
			assert.Len(t, env.allRuns(), before+2)
		}
	})
}
