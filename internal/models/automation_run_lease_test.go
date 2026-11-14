package models

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #237: automation_runs carries the lease of the engine executing a run,
// owner_instance and heartbeat_at, both hidden from the run API. SQLite gets
// them from MigrateDatabase only.

var automationRunLeaseColumns = []string{"owner_instance", "heartbeat_at"}

func TestAutomationRunLeaseJSONHidden(t *testing.T) {
	created := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	claimed := created.Add(time.Second)
	plain := AutomationRun{
		ID: 88, CreatedAt: created, UpdatedAt: created, RuleID: 12, RuleVersion: 3,
		EntityType: AutomationEntityLead, EntityID: 7, DedupeKey: "evt:1:rule:12",
		Status: AutomationRunRunning, NextPosition: 2, ClaimedAt: &claimed,
	}
	owner := "0123456789abcdef0123456789abcdef"
	heartbeat := created.Add(47 * time.Second)
	leased := plain
	leased.OwnerInstance = &owner
	leased.HeartbeatAt = &heartbeat

	plainJSON, err := json.Marshal(plain)
	require.NoError(t, err)
	leasedJSON, err := json.Marshal(leased)
	require.NoError(t, err)
	assert.JSONEq(t, string(plainJSON), string(leasedJSON), "the lease fields change nothing in the JSON")

	text := string(leasedJSON)
	for _, forbidden := range []string{"owner_instance", "heartbeat_at", "OwnerInstance", "HeartbeatAt", owner, "08:00:47"} {
		assert.NotContains(t, text, forbidden)
	}
	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(leasedJSON, &keys))
	require.Contains(t, keys, "claimed_at", "claimed_at is still serialised")
	var claimedOut time.Time
	require.NoError(t, json.Unmarshal(keys["claimed_at"], &claimedOut))
	assert.True(t, claimedOut.Equal(claimed), "claimed_at is unchanged")
	for _, key := range []string{"id", "rule_id", "status", "next_position", "attempts", "dedupe_key"} {
		assert.Contains(t, keys, key, "existing key %s is unchanged", key)
	}

	// A decoded body cannot set them either.
	var decoded AutomationRun
	require.NoError(t, json.Unmarshal([]byte(`{"owner_instance":"x","heartbeat_at":"2026-11-16T08:00:00Z"}`), &decoded))
	assert.Nil(t, decoded.OwnerInstance)
	assert.Nil(t, decoded.HeartbeatAt)
}

// legacyAutomationRun is automation_runs as it was before #237.
type legacyAutomationRun struct {
	ID uint `gorm:"primarykey"`

	CreatedAt time.Time

	UpdatedAt time.Time

	RuleID uint `gorm:"not null;index"`

	RuleVersion int `gorm:"not null"`

	EntityType string `gorm:"not null;type:varchar(20);index:idx_automation_runs_entity"`

	EntityID uint `gorm:"not null;index:idx_automation_runs_entity"`

	EventID *uint

	DedupeKey string `gorm:"not null;type:varchar(100);uniqueIndex"`

	Status string `gorm:"not null;type:varchar(20);default:'pending';index"`

	NextPosition int `gorm:"not null;default:1"`

	Attempts int `gorm:"not null;default:0"`

	ResumeAt *time.Time `gorm:"index"`

	ClaimedAt *time.Time

	FinishedAt *time.Time

	CancelReason string `gorm:"type:varchar(30)"`

	ErrorCode string `gorm:"type:varchar(40)"`
}

func (legacyAutomationRun) TableName() string { return "automation_runs" }

func TestAutomationRunLeaseSQLiteUpgrade(t *testing.T) {
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "automation-run-lease.db"),
	}))
	require.NoError(t, DB.AutoMigrate(&legacyAutomationRun{}))
	for _, name := range automationRunLeaseColumns {
		require.False(t, DB.Migrator().HasColumn("automation_runs", name), "the fixture predates %s", name)
	}

	claimed := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	finished := claimed.Add(time.Minute)
	rows := []legacyAutomationRun{
		{RuleID: 1, RuleVersion: 2, EntityType: AutomationEntityLead, EntityID: 5, DedupeKey: "legacy-running", Status: AutomationRunRunning, NextPosition: 3, Attempts: 1, ClaimedAt: &claimed},
		{RuleID: 1, RuleVersion: 2, EntityType: AutomationEntityDeal, EntityID: 6, DedupeKey: "legacy-done", Status: AutomationRunCompleted, NextPosition: 4, ClaimedAt: &claimed, FinishedAt: &finished},
	}
	for i := range rows {
		require.NoError(t, DB.Create(&rows[i]).Error)
	}

	require.NoError(t, MigrateDatabase())

	var columns []sqliteColumn
	require.NoError(t, DB.Raw("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info('automation_runs')").Scan(&columns).Error)
	byName := map[string]sqliteColumn{}
	for _, column := range columns {
		byName[column.Name] = column
	}
	for _, name := range automationRunLeaseColumns {
		column, ok := byName[name]
		require.True(t, ok, "automation_runs.%s exists", name)
		assert.Equal(t, 0, column.NotNull, "automation_runs.%s is nullable", name)
		assert.Nil(t, column.Dflt, "automation_runs.%s has no default", name)
	}
	assert.Equal(t, "varchar(64)", strings.ToLower(byName["owner_instance"].Type))
	assert.True(t, strings.HasPrefix(strings.ToLower(byName["heartbeat_at"].Type), "datetime"))

	var upgraded []AutomationRun
	require.NoError(t, DB.Order("id").Find(&upgraded).Error)
	require.Len(t, upgraded, 2, "the upgrade keeps every row")
	assert.Equal(t, AutomationRunRunning, upgraded[0].Status, "a legacy running row survives the upgrade")
	assert.Equal(t, 3, upgraded[0].NextPosition)
	assert.Equal(t, 1, upgraded[0].Attempts)
	require.NotNil(t, upgraded[0].ClaimedAt)
	assert.True(t, upgraded[0].ClaimedAt.Equal(claimed), "claimed_at keeps its value")
	assert.Equal(t, AutomationRunCompleted, upgraded[1].Status)
	assert.Equal(t, "legacy-done", upgraded[1].DedupeKey)
	for _, run := range upgraded {
		assert.Nil(t, run.OwnerInstance, "run %d reads NULL", run.ID)
		assert.Nil(t, run.HeartbeatAt, "run %d: no backfill, so a legacy running row reads as stale", run.ID)
	}

	// A second run changes nothing.
	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t))
}
