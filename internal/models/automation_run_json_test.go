package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Runs are readable by sales users and rules only by admins. A run marshals
// the rule's name alone: even with the Rule association loaded — trigger,
// a condition and step texts included — none of it reaches the JSON, while
// the run's own step log stays.
func TestAutomationRunJSONNeverSerialisesTheRule(t *testing.T) {
	value, _ := json.Marshal("CONDITION-VALUE-CANARY")
	run := AutomationRun{
		ID: 7, RuleID: 3, RuleName: "Welcome call", RuleVersion: 2,
		EntityType: AutomationEntityLead, EntityID: 11, Status: AutomationRunCompleted,
		Rule: &AutomationRule{
			Name: "Welcome call", EntityType: AutomationEntityLead, TriggerType: AutomationEventLeadStatusChanged,
			TriggerConfig: `{"from":"new","to":"contacted"}`,
			Trigger:       AutomationTrigger{From: "new", To: "contacted"},
			Steps: []AutomationStep{
				{Position: 1, StepType: AutomationStepCondition, Config: AutomationStepConfig{Field: "source", Operator: "eq", Value: value}},
				{Position: 2, StepType: AutomationStepSendEmail, Config: AutomationStepConfig{Subject: "SUBJECT-CANARY", Body: "BODY-CANARY"}},
				{Position: 3, StepType: AutomationStepNotify, Config: AutomationStepConfig{Title: "TITLE-CANARY"}},
			},
		},
		Steps: []AutomationRunStep{{
			RunID: 7, Position: 2, StepType: AutomationStepSendEmail, Outcome: AutomationOutcomeSucceeded,
			ExecutedAt: time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC), Details: map[string]interface{}{"recipient_id": 4},
		}},
	}

	raw, err := json.Marshal(run)
	require.NoError(t, err)
	text := string(raw)
	for _, canary := range []string{
		`"rule":`, `"trigger"`, `"trigger_type"`, `"config"`, "CONDITION-VALUE-CANARY",
		"SUBJECT-CANARY", "BODY-CANARY", "TITLE-CANARY", "contacted",
	} {
		assert.NotContains(t, text, canary)
	}

	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, "Welcome call", decoded["rule_name"])
	assert.Equal(t, false, decoded["rule_deleted"])
	steps, ok := decoded["steps"].([]interface{})
	require.True(t, ok, "the run's own step log is kept")
	require.Len(t, steps, 1)
	assert.Equal(t, map[string]interface{}{"recipient_id": float64(4)}, steps[0].(map[string]interface{})["details"])
}
