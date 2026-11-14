package main

// AC-11 of #237 through the real wiring: setupRouter (setupDependencies) over
// one SQLite file and the real clock. Three running runs are seeded with
// direct SQL before boot — a fresh foreign lease, a NULL heartbeat, and a
// stale lease whose last step row is a send_email in sending. A worker
// replica's boot sweep spares the first, sends the second back to pending and
// fails the third with email_unconfirmed; a lease that goes stale after boot
// is swept by a later tick without a restart. A replica with the worker off
// sweeps nothing.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
)

const foreignLeaseOwner = "ffffffffffffffffffffffffffffffff"

type seededLeaseRuns struct {
	fresh, null, sending uint
	freshHeartbeat       time.Time
}

// seedLeaseRuns writes the three running runs with direct SQL, as other
// replicas (live or dead) would have left them.
func seedLeaseRuns(t *testing.T, rule *models.AutomationRule, leadID uint) seededLeaseRuns {
	t.Helper()
	now := time.Now().UTC()
	insert := func(key string, owner interface{}, heartbeat interface{}) uint {
		require.NoError(t, models.DB.Exec(`INSERT INTO automation_runs
			(created_at, updated_at, rule_id, rule_version, entity_type, entity_id, dedupe_key, status, next_position, attempts, claimed_at, owner_instance, heartbeat_at)
			VALUES (?, ?, ?, ?, 'lead', ?, ?, 'running', 1, 0, ?, ?, ?)`,
			now, now, rule.ID, rule.Version, leadID, key, now, owner, heartbeat).Error)
		var id uint
		require.NoError(t, models.DB.Raw("SELECT id FROM automation_runs WHERE dedupe_key = ?", key).Scan(&id).Error)
		require.NotZero(t, id)
		return id
	}
	seeded := seededLeaseRuns{freshHeartbeat: now}
	seeded.fresh = insert("lease-fresh", foreignLeaseOwner, now)
	seeded.null = insert("lease-null", nil, nil)
	seeded.sending = insert("lease-sending", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", now.Add(-4*time.Minute))
	require.NoError(t, models.DB.Exec(`INSERT INTO automation_run_steps (run_id, position, step_type, outcome, error_code, detail, executed_at)
		VALUES (?, 1, 'send_email', 'sending', '', '{}', ?)`, seeded.sending, now.Add(-4*time.Minute)).Error)
	return seeded
}

type leaseSnapshot struct {
	runs  []models.AutomationRun
	steps []models.AutomationRunStep
}

func snapshotLeaseRows(t *testing.T) leaseSnapshot {
	t.Helper()
	var snapshot leaseSnapshot
	require.NoError(t, models.DB.Order("id ASC").Find(&snapshot.runs).Error)
	require.NoError(t, models.DB.Order("id ASC").Find(&snapshot.steps).Error)
	return snapshot
}

func TestAutomationRunLeaseProductionWiring(t *testing.T) {
	t.Run("worker on: the boot sweep and the ticks recover stale leases only", func(t *testing.T) {
		boot := bootAutomation(t, true)
		rule, lead := seedAutomationFixture(t, boot.repo)
		seeded := seedLeaseRuns(t, rule, lead.ID)

		// The hook hands over the runtime before the boot sweep; the observer
		// reads each recovered row the moment its recovery committed, before
		// the worker can claim it.
		var mu sync.Mutex
		var worker *service.AutomationRuntime
		observed := map[uint][]models.AutomationRun{}
		automationRuntimeBuilt = func(rt *service.AutomationRuntime) {
			worker = rt
			rt.Engine().SetRecoveryObserver(func(runID uint, _ string) {
				var row models.AutomationRun
				_ = models.DB.First(&row, runID).Error
				mu.Lock()
				defer mu.Unlock()
				observed[runID] = append(observed[runID], row)
			})
		}
		t.Cleanup(func() { automationRuntimeBuilt = nil })
		observedOf := func(id uint) []models.AutomationRun {
			mu.Lock()
			defer mu.Unlock()
			return append([]models.AutomationRun(nil), observed[id]...)
		}

		backgroundCtx, stopBackground := context.WithCancel(context.Background())
		t.Cleanup(stopBackground)
		_, stop := setupRouter(backgroundCtx, boot.cfg)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
			defer cancel()
			_ = stop(ctx)
		})
		require.NotNil(t, worker, "the runtime was built")

		// The fresh foreign lease is untouched.
		fresh := automationRun(t, seeded.fresh)
		assert.Equal(t, models.AutomationRunRunning, fresh.Status)
		require.NotNil(t, fresh.OwnerInstance)
		assert.Equal(t, foreignLeaseOwner, *fresh.OwnerInstance)
		require.NotNil(t, fresh.HeartbeatAt)
		assert.WithinDuration(t, seeded.freshHeartbeat, fresh.HeartbeatAt.UTC(), time.Millisecond, "the heartbeat is unchanged")
		assert.Equal(t, 1, fresh.NextPosition)
		assert.Zero(t, fresh.Attempts)
		assert.Empty(t, observedOf(seeded.fresh))

		// The NULL heartbeat went back to pending at boot.
		nullRecoveries := observedOf(seeded.null)
		require.Len(t, nullRecoveries, 1, "recovered once, at boot")
		assert.Equal(t, models.AutomationRunPending, nullRecoveries[0].Status)
		assert.Equal(t, 1, nullRecoveries[0].NextPosition)
		assert.Zero(t, nullRecoveries[0].Attempts)
		assert.Nil(t, nullRecoveries[0].OwnerInstance)

		// The stale sending email failed with email_unconfirmed, row and run.
		failed := automationRun(t, seeded.sending)
		assert.Equal(t, models.AutomationRunFailed, failed.Status)
		assert.Equal(t, models.AutomationErrorEmailUnconfirmed, failed.ErrorCode)
		assert.Nil(t, failed.OwnerInstance)
		assert.Nil(t, failed.HeartbeatAt)
		var row models.AutomationRunStep
		require.NoError(t, models.DB.Where("run_id = ? AND position = 1", seeded.sending).First(&row).Error)
		assert.Equal(t, models.AutomationOutcomeFailed, row.Outcome)
		assert.Equal(t, models.AutomationErrorEmailUnconfirmed, row.ErrorCode)
		assert.Equal(t, 1, boot.logs.count(`"msg":"Recovered automation runs left behind by a previous process"`))

		// The recovered run is executed by the worker.
		require.Eventually(t, func() bool {
			return automationRun(t, seeded.null).Status == models.AutomationRunCompleted
		}, automationBootTimeout, 50*time.Millisecond)

		// A lease that goes stale after boot is swept by a tick, without a
		// restart.
		require.NoError(t, models.DB.Exec("UPDATE automation_runs SET heartbeat_at = ? WHERE id = ?",
			time.Now().UTC().Add(-4*time.Minute), seeded.fresh).Error)
		worker.Wake()
		deadline := time.Now().Add(automationBootTimeout)
		for len(observedOf(seeded.fresh)) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("no tick swept the stale lease")
			}
			time.Sleep(20 * time.Millisecond)
		}
		swept := observedOf(seeded.fresh)[0]
		assert.Equal(t, models.AutomationRunPending, swept.Status, "the run went back to pending")
		assert.Nil(t, swept.OwnerInstance)
		assert.Nil(t, swept.HeartbeatAt)
		assert.Equal(t, 1, swept.NextPosition)

		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		require.NoError(t, stop(ctx))
	})

	t.Run("worker off: nothing is swept", func(t *testing.T) {
		boot := bootAutomation(t, false)
		rule, lead := seedAutomationFixture(t, boot.repo)
		seedLeaseRuns(t, rule, lead.ID)
		before := snapshotLeaseRows(t)

		called := false
		automationRuntimeBuilt = func(*service.AutomationRuntime) { called = true }
		t.Cleanup(func() { automationRuntimeBuilt = nil })
		backgroundCtx, stopBackground := context.WithCancel(context.Background())
		t.Cleanup(stopBackground)
		_, stop := setupRouter(backgroundCtx, boot.cfg)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
			defer cancel()
			_ = stop(ctx)
		})
		assert.False(t, called, "no runtime is built with the worker off")
		time.Sleep(1500 * time.Millisecond)

		after := snapshotLeaseRows(t)
		assert.Equal(t, before, after, "every seeded row is unchanged")
		assert.Equal(t, 0, boot.logs.count(`"msg":"Recovered automation runs left behind by a previous process"`))
		assert.Equal(t, 0, boot.logs.count(`"msg":"Automation run recovered from a stale lease"`))
	})
}
