package models

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #229: aeo_runs carries the run's owner, its heartbeat, the running lock and
// the daily schedule slot. SQLite gets them from MigrateDatabase only.

var aeoRunOwnershipColumns = []string{"owner_instance", "heartbeat_at", "running_lock", "schedule_slot"}

func openAEORunOwnershipDB(t *testing.T) {
	t.Helper()
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "aeo-run-ownership.db"),
	}))
}

// assertAEORunOwnershipSchemaSQLite checks the four nullable columns and the
// two unique indexes on the current DB.
func assertAEORunOwnershipSchemaSQLite(t *testing.T) {
	t.Helper()
	var columns []sqliteColumn
	require.NoError(t, DB.Raw("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info('aeo_runs')").Scan(&columns).Error)
	byName := map[string]sqliteColumn{}
	for _, column := range columns {
		byName[column.Name] = column
	}
	for _, name := range aeoRunOwnershipColumns {
		column, ok := byName[name]
		require.True(t, ok, "aeo_runs.%s exists", name)
		assert.Equal(t, 0, column.NotNull, "aeo_runs.%s is nullable", name)
		assert.Nil(t, column.Dflt, "aeo_runs.%s has no default", name)
	}
	assert.Equal(t, "varchar(64)", strings.ToLower(byName["owner_instance"].Type))
	assert.Equal(t, "varchar(20)", strings.ToLower(byName["schedule_slot"].Type))
	assert.Equal(t, "datetime", strings.ToLower(byName["heartbeat_at"].Type))
	assert.Equal(t, "integer", strings.ToLower(byName["running_lock"].Type))

	var indexes []sqliteIndex
	require.NoError(t, DB.Raw("SELECT name, \"unique\", origin FROM pragma_index_list('aeo_runs')").Scan(&indexes).Error)
	unique := []string{}
	for _, index := range indexes {
		if index.Unique == 1 && index.Origin != "pk" {
			unique = append(unique, index.Name)
		}
	}
	sort.Strings(unique)
	assert.Equal(t, []string{"idx_aeo_runs_running_lock", "idx_aeo_runs_schedule_slot"}, unique)
	assert.Equal(t, []string{"running_lock"}, customerExternalIDIndexColumns(t, "idx_aeo_runs_running_lock"))
	assert.Equal(t, []string{"schedule_slot"}, customerExternalIDIndexColumns(t, "idx_aeo_runs_schedule_slot"))
}

func TestAEORunOwnershipSchemaSQLite(t *testing.T) {
	openAEORunOwnershipDB(t)
	require.NoError(t, MigrateDatabase())
	assertAEORunOwnershipSchemaSQLite(t)

	started := time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC)
	// Several rows with NULL running_lock and NULL schedule_slot coexist.
	for i := 0; i < 3; i++ {
		run := AEORun{Trigger: AEOTriggerManual, Status: AEORunStatusCompleted, StartedAt: started}
		require.NoError(t, DB.Create(&run).Error)
	}

	one := uint8(1)
	slot := "2026-11-14T06:00Z"
	owner := "owner-a"
	heartbeat := started
	live := AEORun{Trigger: AEOTriggerScheduled, Status: AEORunStatusRunning, StartedAt: started,
		OwnerInstance: &owner, HeartbeatAt: &heartbeat, RunningLock: &one, ScheduleSlot: &slot}
	require.NoError(t, DB.Create(&live).Error)

	var reread AEORun
	require.NoError(t, DB.First(&reread, live.ID).Error)
	require.NotNil(t, reread.OwnerInstance)
	assert.Equal(t, owner, *reread.OwnerInstance)
	require.NotNil(t, reread.HeartbeatAt)
	assert.True(t, reread.HeartbeatAt.Equal(heartbeat))
	require.NotNil(t, reread.RunningLock)
	assert.EqualValues(t, 1, *reread.RunningLock)
	require.NotNil(t, reread.ScheduleSlot)
	assert.Equal(t, slot, *reread.ScheduleSlot)

	// A second running_lock = 1 and a duplicate slot are refused.
	other := uint8(1)
	err := DB.Create(&AEORun{Trigger: AEOTriggerManual, Status: AEORunStatusRunning, StartedAt: started, RunningLock: &other}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNIQUE constraint failed")
	sameSlot := slot
	err = DB.Create(&AEORun{Trigger: AEOTriggerScheduled, Status: AEORunStatusCompleted, StartedAt: started, ScheduleSlot: &sameSlot}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNIQUE constraint failed")

	var total int64
	require.NoError(t, DB.Model(&AEORun{}).Count(&total).Error)
	assert.EqualValues(t, 4, total)
}

// legacyAEORun is aeo_runs as it was before #229.
type legacyAEORun struct {
	BaseModel
	Trigger       string    `gorm:"not null;type:varchar(20);index"`
	Status        string    `gorm:"not null;type:varchar(20);index"`
	StartedAt     time.Time `gorm:"not null"`
	CompletedAt   *time.Time
	TotalQueries  int   `gorm:"not null;default:0"`
	FailedQueries int   `gorm:"not null;default:0"`
	TriggeredByID *uint `gorm:"index"`
}

func (legacyAEORun) TableName() string { return "aeo_runs" }

func TestAEORunOwnershipSQLiteUpgrade(t *testing.T) {
	openAEORunOwnershipDB(t)
	require.NoError(t, DB.AutoMigrate(&legacyAEORun{}))
	for _, name := range aeoRunOwnershipColumns {
		require.False(t, DB.Migrator().HasColumn("aeo_runs", name), "the fixture predates %s", name)
	}

	started := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	completed := started.Add(5 * time.Minute)
	rows := []legacyAEORun{
		{Trigger: AEOTriggerScheduled, Status: AEORunStatusCompleted, StartedAt: started, CompletedAt: &completed, TotalQueries: 6, FailedQueries: 1},
		{Trigger: AEOTriggerManual, Status: AEORunStatusRunning, StartedAt: started.Add(time.Hour), TotalQueries: 3},
	}
	for i := range rows {
		require.NoError(t, DB.Create(&rows[i]).Error)
	}

	require.NoError(t, MigrateDatabase())
	assertAEORunOwnershipSchemaSQLite(t)

	var upgraded []AEORun
	require.NoError(t, DB.Order("id").Find(&upgraded).Error)
	require.Len(t, upgraded, 2, "the upgrade keeps every row")
	assert.Equal(t, AEORunStatusCompleted, upgraded[0].Status)
	assert.Equal(t, 6, upgraded[0].TotalQueries)
	assert.Equal(t, 1, upgraded[0].FailedQueries)
	assert.Equal(t, AEORunStatusRunning, upgraded[1].Status, "a legacy running row survives the upgrade")
	for _, run := range upgraded {
		assert.Nil(t, run.OwnerInstance, "run %d", run.ID)
		assert.Nil(t, run.HeartbeatAt, "run %d: no backfill, so a legacy running row reads as abandoned", run.ID)
		assert.Nil(t, run.RunningLock, "run %d", run.ID)
		assert.Nil(t, run.ScheduleSlot, "run %d", run.ID)
	}

	// A second run changes nothing.
	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t))
}

func TestAEORunOwnershipJSONHidden(t *testing.T) {
	started := time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC)
	plain := AEORun{Trigger: AEOTriggerScheduled, Status: AEORunStatusRunning, StartedAt: started, TotalQueries: 4}
	plain.ID = 12

	owner := "0123456789abcdef0123456789abcdef"
	heartbeat := started.Add(30 * time.Second)
	one := uint8(1)
	slot := "2026-11-14T06:00Z"
	owned := plain
	owned.OwnerInstance = &owner
	owned.HeartbeatAt = &heartbeat
	owned.RunningLock = &one
	owned.ScheduleSlot = &slot

	plainJSON, err := json.Marshal(plain)
	require.NoError(t, err)
	ownedJSON, err := json.Marshal(owned)
	require.NoError(t, err)
	assert.JSONEq(t, string(plainJSON), string(ownedJSON), "the ownership fields change nothing in the JSON")

	text := string(ownedJSON)
	for _, forbidden := range []string{
		"owner_instance", "heartbeat_at", "running_lock", "schedule_slot",
		"OwnerInstance", "HeartbeatAt", "RunningLock", "ScheduleSlot",
		owner, slot, "06:00:30",
	} {
		assert.NotContains(t, text, forbidden)
	}

	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(ownedJSON, &keys))
	for _, key := range []string{"id", "trigger", "status", "started_at", "total_queries", "failed_queries"} {
		assert.Contains(t, keys, key, "existing key %s is unchanged", key)
	}

	// A client cannot set them through a decoded body either.
	var decoded AEORun
	require.NoError(t, json.Unmarshal([]byte(`{"owner_instance":"x","heartbeat_at":"2026-11-14T06:00:00Z","running_lock":1,"schedule_slot":"y"}`), &decoded))
	assert.Nil(t, decoded.OwnerInstance)
	assert.Nil(t, decoded.HeartbeatAt)
	assert.Nil(t, decoded.RunningLock)
	assert.Nil(t, decoded.ScheduleSlot)
	assert.Equal(t, "2006-01-02T15:04Z", AEOScheduleSlotFormat)
}
