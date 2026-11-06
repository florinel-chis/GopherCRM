package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #220: a submission serialises its six attribution fields as strings, empty
// when absent, never null and never omitted.
func TestFormSubmissionAttributionJSON(t *testing.T) {
	attributionKeys := []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "referrer_host"}

	t.Run("absent attribution is empty strings", func(t *testing.T) {
		encoded, err := json.Marshal(FormSubmission{FormID: 2, Status: FormSubmissionSpam})
		require.NoError(t, err)
		var body map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(encoded, &body))
		for _, key := range attributionKeys {
			raw, ok := body[key]
			require.True(t, ok, "%s is always present", key)
			assert.JSONEq(t, `""`, string(raw), "%s is an empty string when absent", key)
		}
	})

	t.Run("stored attribution round-trips", func(t *testing.T) {
		submission := FormSubmission{
			FormID:       2,
			Status:       FormSubmissionReceived,
			UTMSource:    "newsletter",
			UTMMedium:    "email",
			UTMCampaign:  "spring",
			UTMTerm:      "crm",
			UTMContent:   "cta",
			ReferrerHost: "www.example.com",
		}
		for name, value := range map[string]interface{}{"value": submission, "pointer": &submission} {
			encoded, err := json.Marshal(value)
			require.NoError(t, err, name)
			var body map[string]interface{}
			require.NoError(t, json.Unmarshal(encoded, &body))
			assert.Equal(t, "newsletter", body["utm_source"], name)
			assert.Equal(t, "email", body["utm_medium"], name)
			assert.Equal(t, "spring", body["utm_campaign"], name)
			assert.Equal(t, "crm", body["utm_term"], name)
			assert.Equal(t, "cta", body["utm_content"], name)
			assert.Equal(t, "www.example.com", body["referrer_host"], name)
			assert.JSONEq(t, `[]`, mustJSON(t, body["deliveries"]), "%s: deliveries stay []", name)
		}
	})
}

func mustJSON(t *testing.T, value interface{}) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

// TestFormAttributionSQLiteUpgrade is the SQLite half of #220: SQLite installs
// never run the SQL migration, so MigrateDatabase on a file written before
// this change adds the six submission columns and the nullable
// automation_events.form_id and submission_id, and keeps every row. The
// v1.2.0 fixture carries form_submissions; automation_events is created here
// with the DDL AutoMigrate wrote before the two columns existed.
func TestFormAttributionSQLiteUpgrade(t *testing.T) {
	openV120Fixture(t)
	for _, statement := range []string{
		"CREATE TABLE `automation_events` (`id` integer PRIMARY KEY AUTOINCREMENT,`entity_type` varchar(20) NOT NULL,`entity_id` integer NOT NULL,`event_type` varchar(40) NOT NULL,`from_value` varchar(50),`to_value` varchar(50),`origin` varchar(20) NOT NULL,`actor_id` integer,`created_at` datetime NOT NULL,`processed_at` datetime)",
		"CREATE INDEX `idx_automation_events_entity` ON `automation_events`(`entity_type`,`entity_id`)",
		"CREATE INDEX `idx_automation_events_processed_at` ON `automation_events`(`processed_at`)",
		"INSERT INTO form_submissions (form_id, data, email, status, referrer) VALUES (1, '{\"utm_source\":\"kept\"}', 'kept@example.com', 'received', 'https://www.example.com/?utm_source=kept')",
		"INSERT INTO automation_events (entity_type, entity_id, event_type, origin, created_at) VALUES ('lead', 1, 'lead_created', 'form', '2026-11-01 10:00:00')",
	} {
		require.NoError(t, DB.Exec(statement).Error, statement)
	}
	for _, column := range []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "referrer_host"} {
		require.False(t, DB.Migrator().HasColumn("form_submissions", column), "the fixture predates %s", column)
	}
	require.False(t, DB.Migrator().HasColumn("automation_events", "form_id"))
	require.False(t, DB.Migrator().HasColumn("automation_events", "submission_id"))

	require.NoError(t, MigrateDatabase())
	assertUpgradedV120(t)

	for _, column := range []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "referrer_host"} {
		assert.True(t, DB.Migrator().HasColumn("form_submissions", column), "form_submissions.%s", column)
	}
	assert.True(t, DB.Migrator().HasColumn("automation_events", "form_id"))
	assert.True(t, DB.Migrator().HasColumn("automation_events", "submission_id"))

	// The existing rows are kept, unattributed: no backfill from data or
	// referrer, and the event carries no form ids.
	var submissions []FormSubmission
	require.NoError(t, DB.Find(&submissions).Error)
	require.Len(t, submissions, 1)
	kept := submissions[0]
	assert.Equal(t, "kept@example.com", kept.Email)
	assert.Equal(t, "kept", kept.Data["utm_source"], "the stored values are untouched")
	assert.Empty(t, kept.UTMSource)
	assert.Empty(t, kept.UTMMedium)
	assert.Empty(t, kept.UTMCampaign)
	assert.Empty(t, kept.UTMTerm)
	assert.Empty(t, kept.UTMContent)
	assert.Empty(t, kept.ReferrerHost)

	var events []AutomationEvent
	require.NoError(t, DB.Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, AutomationEventLeadCreated, events[0].EventType)
	assert.Nil(t, events[0].FormID)
	assert.Nil(t, events[0].SubmissionID)

	// The new columns take writes.
	formID, submissionID := uint(1), kept.ID
	require.NoError(t, DB.Model(&FormSubmission{}).Where("id = ?", kept.ID).Update("utm_campaign", "spring").Error)
	require.NoError(t, DB.Create(&AutomationEvent{
		EntityType: AutomationEntityLead, EntityID: 1, EventType: AutomationEventLeadFormSubmitted,
		Origin: AutomationOriginForm, FormID: &formID, SubmissionID: &submissionID, CreatedAt: kept.CreatedAt,
	}).Error)
	var stored AutomationEvent
	require.NoError(t, DB.Where("event_type = ?", AutomationEventLeadFormSubmitted).First(&stored).Error)
	require.NotNil(t, stored.FormID)
	require.NotNil(t, stored.SubmissionID)
	assert.Equal(t, formID, *stored.FormID)
	assert.Equal(t, submissionID, *stored.SubmissionID)
}
