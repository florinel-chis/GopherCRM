package service

// #333, service half: AutomationService.CancelRun (manual), UpdateRule
// (rule_changed), SetEnabled(false) (rule_disabled) and DeleteRule
// (rule_deleted) each leave a run that held a real lease from ClaimRun
// cancelled with the matching reason and owner_instance and heartbeat_at
// NULL. The run JSON carries neither lease column, before or after, and the
// cancel adds only the omitempty keys of a finished run (finished_at,
// cancel_reason): the API shape is unchanged.

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutomationCancelPathsReleaseLeaseSQLite(t *testing.T) {
	runAutomationCancelPathsReleaseLease(t, newAutomationEnv(t, openAutomationSQLite(t)))
}

func TestAutomationCancelPathsReleaseLeaseMySQL(t *testing.T) {
	runAutomationCancelPathsReleaseLease(t, newAutomationEnv(t, openAutomationMySQL(t, 2)))
}

const automationCancelLeaseOwner = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

func automationRunJSONKeys(t *testing.T, run *models.AutomationRun) []string {
	t.Helper()
	raw, err := json.Marshal(run)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func runAutomationCancelPathsReleaseLease(t *testing.T, env *automationEnv) {
	// claimedRun starts a run of a fresh rule and claims it with the real
	// ClaimRun, so it holds a lease. It returns the rule, the run and the JSON
	// key set of the claimed run.
	claimedRun := func(t *testing.T) (*models.AutomationRule, *models.AutomationRun, []string) {
		t.Helper()
		rule := env.leadRule(models.AutomationEventLeadCreated, waitStep(30), notifyStep(models.AutomationRecipientOwner, 0))
		lead := env.createLead(env.sales, models.LeadStatusNew)
		run := env.startRun(rule, lead)
		rows, err := env.repo.ClaimRun(run.ID, models.AutomationRunPending, automationCancelLeaseOwner, env.clock.Now())
		require.NoError(t, err)
		require.EqualValues(t, 1, rows)
		stored := env.run(run.ID)
		require.Equal(t, models.AutomationRunRunning, stored.Status)
		require.NotNil(t, stored.OwnerInstance, "the claim set owner_instance")
		require.Equal(t, automationCancelLeaseOwner, *stored.OwnerInstance)
		require.NotNil(t, stored.HeartbeatAt, "the claim set heartbeat_at")
		return rule, stored, automationRunJSONKeys(t, stored)
	}

	assertReleased := func(t *testing.T, run *models.AutomationRun, before []string, reason string) {
		t.Helper()
		stored := env.run(run.ID)
		assert.Equal(t, models.AutomationRunCancelled, stored.Status)
		assert.Equal(t, reason, stored.CancelReason)
		assert.NotNil(t, stored.FinishedAt)
		assert.Nil(t, stored.OwnerInstance, "owner_instance is NULL")
		assert.Nil(t, stored.HeartbeatAt, "heartbeat_at is NULL")

		for _, candidate := range []*models.AutomationRun{run, stored} {
			keys := automationRunJSONKeys(t, candidate)
			assert.NotContains(t, keys, "owner_instance")
			assert.NotContains(t, keys, "heartbeat_at")
			// The claimed run's keys plus the two omitempty keys a cancelled
			// run fills; no key removed, none added beyond those.
			want := append([]string{}, before...)
			for _, key := range []string{"cancel_reason", "finished_at"} {
				if !containsString(want, key) {
					want = append(want, key)
				}
			}
			sort.Strings(want)
			assert.Equal(t, want, keys, "the run JSON key set is unchanged")
		}
		assert.NotContains(t, before, "owner_instance")
		assert.NotContains(t, before, "heartbeat_at")
	}

	t.Run("CancelRun (manual)", func(t *testing.T) {
		_, run, before := claimedRun(t)
		returned, err := env.svc.CancelRun(run.ID)
		require.NoError(t, err)
		require.NotNil(t, returned)
		assertReleased(t, returned, before, models.AutomationCancelManual)
	})

	t.Run("UpdateRule (rule_changed)", func(t *testing.T) {
		rule, run, before := claimedRun(t)
		replacement := &models.AutomationRule{Name: "Changed", EntityType: rule.EntityType, TriggerType: rule.TriggerType,
			Enabled: true, Steps: []models.AutomationStep{waitStep(15)}}
		_, err := env.svc.UpdateRule(rule.ID, replacement, nil)
		require.NoError(t, err)
		assertReleased(t, env.run(run.ID), before, models.AutomationCancelRuleChanged)
	})

	t.Run("SetEnabled(false) (rule_disabled)", func(t *testing.T) {
		rule, run, before := claimedRun(t)
		_, err := env.svc.SetEnabled(rule.ID, false)
		require.NoError(t, err)
		assertReleased(t, env.run(run.ID), before, models.AutomationCancelRuleDisabled)
	})

	t.Run("DeleteRule (rule_deleted)", func(t *testing.T) {
		rule, run, before := claimedRun(t)
		require.NoError(t, env.svc.DeleteRule(rule.ID))
		reread := env.run(run.ID)
		// rule_deleted becomes true on a re-read; its key is always present.
		assertReleased(t, reread, before, models.AutomationCancelRuleDeleted)
	})
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
