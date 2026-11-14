package main

// Production-wiring root for issue #229: setupDependencies booted a second time
// over the same SQLite file spares a run another instance is executing (fresh
// heartbeat) and fails an abandoned one (stale heartbeat) before the scheduler
// is armed.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// index returns the byte offset of the first occurrence of substring in the
// captured log, or -1.
func (b *lockedLogBuffer) index(substring string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Index(b.buf.String(), substring)
}

// seedForeignRun writes a running row owned by another instance whose last
// heartbeat is heartbeatAgo old and which holds the running lock.
func seedForeignRun(t *testing.T, heartbeatAgo time.Duration) models.AEORun {
	t.Helper()
	now := time.Now().UTC()
	owner := "0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f"
	heartbeat := now.Add(-heartbeatAgo)
	lock := uint8(1)
	run := models.AEORun{
		Trigger:       models.AEOTriggerManual,
		Status:        models.AEORunStatusRunning,
		StartedAt:     now.Add(-heartbeatAgo - time.Minute),
		TotalQueries:  1,
		OwnerInstance: &owner,
		HeartbeatAt:   &heartbeat,
		RunningLock:   &lock,
	}
	require.NoError(t, models.DB.Create(&run).Error)
	return run
}

func TestAEOReplicaProductionWiring(t *testing.T) {
	const (
		recoveredLine = `"msg":"Recovered AEO runs left behind by a previous process"`
		sweptLine     = `"msg":"Failed AEO runs whose owner stopped renewing its heartbeat"`
		armedLine     = `"msg":"AEO scheduler started"`
	)

	t.Run("a second boot leaves another instance's live run running", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "aeo-replica.db")

		// The first process boots and stops; the file now holds the schema.
		first := bootAEOAt(t, true, path, nil)
		require.NoError(t, first.stopWithin(t, aeoBootTimeout))
		require.NoError(t, models.CloseDatabase())

		// Another instance is executing a run, its heartbeat a few seconds old,
		// when this process boots over the same file.
		var live models.AEORun
		second := bootAEOAt(t, true, path, func(t *testing.T) {
			live = seedForeignRun(t, 5*time.Second)
		})

		var stored models.AEORun
		require.NoError(t, models.DB.First(&stored, live.ID).Error)
		assert.Equal(t, models.AEORunStatusRunning, stored.Status, "the boot reconciliation spares a live run")
		require.NotNil(t, stored.RunningLock)
		assert.EqualValues(t, 1, *stored.RunningLock)
		assert.Nil(t, stored.CompletedAt)
		assert.Equal(t, 0, second.logs.count(recoveredLine), "no recovered runs are logged")
		assert.Equal(t, 0, second.logs.count(sweptLine))
		assert.Equal(t, 1, second.logs.count(armedLine))
		assert.Equal(t, 0, second.logs.count(`"level":"error"`))

		require.NoError(t, second.stopWithin(t, aeoBootTimeout))
		require.NoError(t, models.DB.First(&stored, live.ID).Error)
		assert.Equal(t, models.AEORunStatusRunning, stored.Status, "stopping this process does not touch another instance's run")
	})

	t.Run("a boot fails a stale-heartbeat run before the scheduler is armed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "aeo-replica.db")
		first := bootAEOAt(t, true, path, nil)
		require.NoError(t, first.stopWithin(t, aeoBootTimeout))
		require.NoError(t, models.CloseDatabase())

		var stale models.AEORun
		second := bootAEOAt(t, true, path, func(t *testing.T) {
			stale = seedForeignRun(t, 4*time.Minute)
		})

		var stored models.AEORun
		require.NoError(t, models.DB.First(&stored, stale.ID).Error)
		assert.Equal(t, models.AEORunStatusFailed, stored.Status)
		assert.NotNil(t, stored.CompletedAt)
		assert.Nil(t, stored.RunningLock, "the abandoned run releases the running lock")

		assert.Equal(t, 1, second.logs.count(recoveredLine))
		assert.Equal(t, 1, second.logs.count(sweptLine))
		recovered, armed := second.logs.index(recoveredLine), second.logs.index(armedLine)
		require.GreaterOrEqual(t, recovered, 0)
		require.GreaterOrEqual(t, armed, 0)
		assert.Less(t, recovered, armed, "the recovery happens before the scheduler is armed")
		assert.Equal(t, 0, second.logs.count("0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f"), "the owner id is never logged")

		ctx, cancel := context.WithTimeout(context.Background(), aeoBootTimeout)
		defer cancel()
		require.NoError(t, second.stop(ctx))
	})
}
