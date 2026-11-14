package repository

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #237: the repository half of the run leases. The claim stamps the owner and
// the heartbeat; the renewal, the advance and the release are fenced on the
// owner; every cancel clears the lease; the stale sweep reads with a plain
// query and recovers by primary key, re-checking the observed owner and the
// staleness; a send_email row is stamped only while it is still sending; and
// the owner-blind writers are gone.

func TestAutomationRunLeaseRepositorySQLite(t *testing.T) {
	runAutomationRunLeaseRepository(t, openAutomationRepoDB(t, false, 1))
}

func TestAutomationRunLeaseRepositoryMySQL(t *testing.T) {
	runAutomationRunLeaseRepository(t, openAutomationRepoDB(t, true, 2))
}

const (
	leaseOwnerA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	leaseOwnerB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func leaseRow(t *testing.T, db *gorm.DB, id uint) models.AutomationRun {
	t.Helper()
	var run models.AutomationRun
	require.NoError(t, db.First(&run, id).Error)
	return run
}

func assertLeaseCleared(t *testing.T, run models.AutomationRun, msg string) {
	t.Helper()
	assert.Nil(t, run.OwnerInstance, "%s: owner_instance cleared", msg)
	assert.Nil(t, run.HeartbeatAt, "%s: heartbeat_at cleared", msg)
}

func assertLeaseHeld(t *testing.T, run models.AutomationRun, owner string, heartbeat time.Time, msg string) {
	t.Helper()
	require.NotNil(t, run.OwnerInstance, msg)
	assert.Equal(t, owner, *run.OwnerInstance, msg)
	require.NotNil(t, run.HeartbeatAt, msg)
	assert.True(t, run.HeartbeatAt.UTC().Equal(heartbeat.UTC()), "%s: heartbeat %v, want %v", msg, run.HeartbeatAt, heartbeat)
}

func runAutomationRunLeaseRepository(t *testing.T, db *gorm.DB) {
	repo := NewAutomationRepository(db)
	rule := seedAutomationRule(t, db, models.AutomationEntityLead)
	now := time.Date(2026, time.November, 16, 9, 0, 0, 0, time.UTC)

	claimed := func(t *testing.T, entityID uint, owner string, at time.Time) *models.AutomationRun {
		t.Helper()
		run := seedAutomationRun(t, db, rule, entityID, models.AutomationRunPending)
		rows, err := repo.ClaimRun(run.ID, models.AutomationRunPending, owner, at)
		require.NoError(t, err)
		require.EqualValues(t, 1, rows)
		return run
	}

	t.Run("(a) the claim writes running, the owner, the heartbeat and claimed_at in one update", func(t *testing.T) {
		run := seedAutomationRun(t, db, rule, 1, models.AutomationRunPending)
		rows, err := repo.ClaimRun(run.ID, models.AutomationRunPending, leaseOwnerA, now)
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)
		stored := leaseRow(t, db, run.ID)
		assert.Equal(t, models.AutomationRunRunning, stored.Status)
		assertLeaseHeld(t, stored, leaseOwnerA, now, "claim")
		require.NotNil(t, stored.ClaimedAt)
		assert.True(t, stored.ClaimedAt.UTC().Equal(now))

		rows, err = repo.ClaimRun(run.ID, models.AutomationRunPending, leaseOwnerB, now)
		require.NoError(t, err)
		assert.Zero(t, rows, "a running run is not claimed again")
		assertLeaseHeld(t, leaseRow(t, db, run.ID), leaseOwnerA, now, "the second claim wrote nothing")
	})

	t.Run("(b) the renewal is fenced on the owner and re-checks a zero count", func(t *testing.T) {
		run := claimed(t, 2, leaseOwnerA, now)
		before := leaseRow(t, db, run.ID)
		renewed := now.Add(30 * time.Second)
		require.NoError(t, repo.RenewRunLease(run.ID, leaseOwnerA, renewed))
		after := leaseRow(t, db, run.ID)
		assertLeaseHeld(t, after, leaseOwnerA, renewed, "renewal")
		assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "the renewal writes heartbeat_at only")
		assert.Equal(t, before.Status, after.Status)
		assert.True(t, before.ClaimedAt.Equal(*after.ClaimedAt), "claimed_at is unchanged")

		// The same value again: MySQL reports zero changed rows, and the
		// re-check read still finds the run owned.
		require.NoError(t, repo.RenewRunLease(run.ID, leaseOwnerA, renewed))

		err := repo.RenewRunLease(run.ID, leaseOwnerB, renewed.Add(time.Second))
		assert.True(t, errors.Is(err, ErrAutomationRunNotOwned), "another owner is refused, got %v", err)
		assertLeaseHeld(t, leaseRow(t, db, run.ID), leaseOwnerA, renewed, "the refused renewal wrote nothing")

		_, err = repo.CancelRun(run.ID, models.AutomationCancelManual, now)
		require.NoError(t, err)
		err = repo.RenewRunLease(run.ID, leaseOwnerA, renewed.Add(time.Minute))
		assert.True(t, errors.Is(err, ErrAutomationRunNotOwned), "a cancelled run is no longer owned, got %v", err)
		err = repo.RenewRunLease(999999, leaseOwnerA, renewed)
		assert.True(t, errors.Is(err, ErrAutomationRunNotOwned), "an unknown run is not owned, got %v", err)
	})

	t.Run("(c) advance and release are fenced on the owner and clear the lease when leaving running", func(t *testing.T) {
		run := claimed(t, 3, leaseOwnerA, now)
		rows, err := repo.AdvanceRun(run.ID, 1, leaseOwnerB, AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: 2}, now)
		require.NoError(t, err)
		assert.Zero(t, rows, "another owner's advance matches nothing")
		stored := leaseRow(t, db, run.ID)
		assert.Equal(t, 1, stored.NextPosition, "and writes nothing")
		assertLeaseHeld(t, stored, leaseOwnerA, now, "foreign advance")

		rows, err = repo.AdvanceRun(run.ID, 1, leaseOwnerA, AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: 2}, now.Add(time.Second))
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)
		stored = leaseRow(t, db, run.ID)
		assert.Equal(t, 2, stored.NextPosition)
		assertLeaseHeld(t, stored, leaseOwnerA, now, "an advance that stays running keeps the lease")

		resumeAt := now.Add(time.Hour)
		rows, err = repo.AdvanceRun(run.ID, 2, leaseOwnerA, AutomationRunAdvance{Status: models.AutomationRunWaiting, NextPosition: 3, ResumeAt: &resumeAt}, now.Add(2*time.Second))
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)
		stored = leaseRow(t, db, run.ID)
		assert.Equal(t, models.AutomationRunWaiting, stored.Status)
		assertLeaseCleared(t, stored, "waiting")

		finishing := claimed(t, 4, leaseOwnerA, now)
		finished := now.Add(time.Minute)
		rows, err = repo.AdvanceRun(finishing.ID, 1, leaseOwnerA, AutomationRunAdvance{Status: models.AutomationRunCompleted, NextPosition: 2, FinishedAt: &finished}, finished)
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)
		assertLeaseCleared(t, leaseRow(t, db, finishing.ID), "completed")

		released := claimed(t, 5, leaseOwnerA, now)
		rows, err = repo.ReleaseRun(released.ID, 1, leaseOwnerB, now)
		require.NoError(t, err)
		assert.Zero(t, rows, "another owner's release matches nothing")
		stored = leaseRow(t, db, released.ID)
		assert.Equal(t, models.AutomationRunRunning, stored.Status)
		assert.Zero(t, stored.Attempts, "and writes nothing")
		rows, err = repo.ReleaseRun(released.ID, 1, leaseOwnerA, now)
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)
		stored = leaseRow(t, db, released.ID)
		assert.Equal(t, models.AutomationRunPending, stored.Status)
		assert.Equal(t, 1, stored.Attempts)
		assert.Nil(t, stored.ClaimedAt)
		assertLeaseCleared(t, stored, "released")
	})

	t.Run("(d) every cancel keeps its predicate and clears the lease", func(t *testing.T) {
		other := seedAutomationRule(t, db, models.AutomationEntityLead)
		single := claimed(t, 6, leaseOwnerA, now)
		rows, err := repo.CancelRun(single.ID, models.AutomationCancelManual, now)
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)
		stored := leaseRow(t, db, single.ID)
		assert.Equal(t, models.AutomationRunCancelled, stored.Status)
		assertLeaseCleared(t, stored, "CancelRun")

		byRule := seedAutomationRun(t, db, other, 7, models.AutomationRunPending)
		rows, err = repo.ClaimRun(byRule.ID, models.AutomationRunPending, leaseOwnerB, now)
		require.NoError(t, err)
		require.EqualValues(t, 1, rows)
		pendingOfRule := seedAutomationRun(t, db, other, 8, models.AutomationRunPending)
		rows, err = repo.CancelRunsForRule(other.ID, models.AutomationCancelRuleChanged, now)
		require.NoError(t, err)
		assert.EqualValues(t, 2, rows, "running and pending runs of the rule")
		assertLeaseCleared(t, leaseRow(t, db, byRule.ID), "CancelRunsForRule")
		assert.Equal(t, models.AutomationRunCancelled, leaseRow(t, db, pendingOfRule.ID).Status)

		byEntity := claimed(t, 9, leaseOwnerA, now)
		finishedRun := claimed(t, 9, leaseOwnerA, now)
		done := now.Add(time.Second)
		_, err = repo.AdvanceRun(finishedRun.ID, 1, leaseOwnerA, AutomationRunAdvance{Status: models.AutomationRunCompleted, NextPosition: 2, FinishedAt: &done}, done)
		require.NoError(t, err)
		rows, err = repo.CancelRunsForEntity(models.AutomationEntityLead, 9, models.AutomationCancelRecordDeleted, now)
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows, "only the active run of the entity")
		stored = leaseRow(t, db, byEntity.ID)
		assert.Equal(t, models.AutomationRunCancelled, stored.Status)
		assertLeaseCleared(t, stored, "CancelRunsForEntity")
		assert.Equal(t, models.AutomationRunCompleted, leaseRow(t, db, finishedRun.ID).Status, "a finished run is left alone")
	})

	t.Run("(e) the stale read lists running rows with a NULL or old heartbeat in id order", func(t *testing.T) {
		require.NoError(t, db.Session(&gorm.Session{AllowGlobalUpdate: true}).Model(&models.AutomationRun{}).
			Where("status = ?", models.AutomationRunRunning).Update("status", models.AutomationRunCompleted).Error)
		cutoff := now.Add(10 * time.Minute)
		old := claimed(t, 10, leaseOwnerA, now)
		fresh := claimed(t, 11, leaseOwnerB, cutoff.Add(time.Second))
		legacy := seedAutomationRun(t, db, rule, 12, models.AutomationRunPending)
		require.NoError(t, db.Model(&models.AutomationRun{}).Where("id = ?", legacy.ID).Update("status", models.AutomationRunRunning).Error)
		atCutoff := claimed(t, 13, leaseOwnerA, cutoff)
		pending := seedAutomationRun(t, db, rule, 14, models.AutomationRunPending)

		stale, err := repo.ListStaleRunningRuns(cutoff, 10)
		require.NoError(t, err)
		ids := []uint{}
		for _, run := range stale {
			ids = append(ids, run.ID)
		}
		assert.Equal(t, []uint{old.ID, legacy.ID}, ids, "strictly older than the cutoff, or NULL, in id order")
		assert.NotContains(t, ids, fresh.ID)
		assert.NotContains(t, ids, atCutoff.ID)
		assert.NotContains(t, ids, pending.ID)
		require.NotNil(t, stale[0].OwnerInstance, "the read returns the observed owner")
		assert.Equal(t, leaseOwnerA, *stale[0].OwnerInstance)
		assert.Nil(t, stale[1].OwnerInstance)

		limited, err := repo.ListStaleRunningRuns(cutoff, 1)
		require.NoError(t, err)
		require.Len(t, limited, 1)
		assert.Equal(t, old.ID, limited[0].ID)
	})

	t.Run("(f) the recovery re-checks the owner, the staleness and the status", func(t *testing.T) {
		cutoff := now.Add(time.Hour)
		run := claimed(t, 20, leaseOwnerA, now)
		require.NoError(t, db.Model(&models.AutomationRun{}).Where("id = ?", run.ID).Update("attempts", 1).Error)
		observedB := leaseOwnerB
		rows, err := repo.RecoverStaleRun(run.ID, &observedB, cutoff, "", now)
		require.NoError(t, err)
		assert.Zero(t, rows, "another observed owner matches nothing (re-claimed meanwhile)")
		rows, err = repo.RecoverStaleRun(run.ID, nil, cutoff, "", now)
		require.NoError(t, err)
		assert.Zero(t, rows, "a NULL observed owner does not match an owned row")

		require.NoError(t, repo.RenewRunLease(run.ID, leaseOwnerA, cutoff.Add(time.Second)))
		observedA := leaseOwnerA
		rows, err = repo.RecoverStaleRun(run.ID, &observedA, cutoff, "", now)
		require.NoError(t, err)
		assert.Zero(t, rows, "a renewed lease is not stale any more")
		assertLeaseHeld(t, leaseRow(t, db, run.ID), leaseOwnerA, cutoff.Add(time.Second), "spared")

		require.NoError(t, repo.RenewRunLease(run.ID, leaseOwnerA, now))
		recoveredAt := cutoff.Add(time.Minute)
		rows, err = repo.RecoverStaleRun(run.ID, &observedA, cutoff, "", recoveredAt)
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)
		stored := leaseRow(t, db, run.ID)
		assert.Equal(t, models.AutomationRunPending, stored.Status)
		assert.Equal(t, 1, stored.NextPosition, "at its position")
		assert.Equal(t, 1, stored.Attempts, "attempts unchanged")
		assert.Nil(t, stored.ClaimedAt)
		assert.Nil(t, stored.FinishedAt)
		assert.Empty(t, stored.ErrorCode)
		assertLeaseCleared(t, stored, "recovered to pending")

		rows, err = repo.RecoverStaleRun(run.ID, nil, cutoff, "", recoveredAt)
		require.NoError(t, err)
		assert.Zero(t, rows, "a pending run is not running")

		legacy := seedAutomationRun(t, db, rule, 21, models.AutomationRunPending)
		require.NoError(t, db.Model(&models.AutomationRun{}).Where("id = ?", legacy.ID).Update("status", models.AutomationRunRunning).Error)
		rows, err = repo.RecoverStaleRun(legacy.ID, &observedA, cutoff, models.AutomationErrorEmailUnconfirmed, recoveredAt)
		require.NoError(t, err)
		assert.Zero(t, rows, "an owner observed on a NULL-owner row matches nothing")
		rows, err = repo.RecoverStaleRun(legacy.ID, nil, cutoff, models.AutomationErrorEmailUnconfirmed, recoveredAt)
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows, "NULL matches NULL")
		stored = leaseRow(t, db, legacy.ID)
		assert.Equal(t, models.AutomationRunFailed, stored.Status)
		assert.Equal(t, models.AutomationErrorEmailUnconfirmed, stored.ErrorCode)
		require.NotNil(t, stored.FinishedAt)
		assert.True(t, stored.FinishedAt.UTC().Equal(recoveredAt))
		assertLeaseCleared(t, stored, "failed")

		finishedRun := claimed(t, 22, leaseOwnerA, now)
		done := now.Add(time.Second)
		_, err = repo.AdvanceRun(finishedRun.ID, 1, leaseOwnerA, AutomationRunAdvance{Status: models.AutomationRunCompleted, NextPosition: 2, FinishedAt: &done}, done)
		require.NoError(t, err)
		rows, err = repo.RecoverStaleRun(finishedRun.ID, nil, cutoff, "", recoveredAt)
		require.NoError(t, err)
		assert.Zero(t, rows, "a finished run is left alone")
		assert.Equal(t, models.AutomationRunCompleted, leaseRow(t, db, finishedRun.ID).Status)
	})

	t.Run("(g) a send_email row is stamped only while it is still sending", func(t *testing.T) {
		run := claimed(t, 30, leaseOwnerA, now)
		row := &models.AutomationRunStep{RunID: run.ID, Position: 1, StepType: models.AutomationStepSendEmail, Outcome: models.AutomationOutcomeSending,
			Details: map[string]interface{}{"recipient_id": 7}, ExecutedAt: now}
		require.NoError(t, repo.CreateRunStep(row))
		rows, err := repo.FinishSendingStep(row.ID, models.AutomationOutcomeFailed, models.AutomationErrorEmailUnconfirmed, map[string]interface{}{"recipient_id": 7})
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)
		rows, err = repo.FinishSendingStep(row.ID, models.AutomationOutcomeSucceeded, "", map[string]interface{}{"recipient_id": 7})
		require.NoError(t, err)
		assert.Zero(t, rows, "a second stamp matches nothing")
		stored, err := repo.GetRunStep(run.ID, 1)
		require.NoError(t, err)
		assert.Equal(t, models.AutomationOutcomeFailed, stored.Outcome, "the first stamp stands")
		assert.Equal(t, models.AutomationErrorEmailUnconfirmed, stored.ErrorCode)
		assert.EqualValues(t, 7, stored.Details["recipient_id"])

		done := &models.AutomationRunStep{RunID: run.ID, Position: 2, StepType: models.AutomationStepNotify, Outcome: models.AutomationOutcomeSucceeded, ExecutedAt: now}
		require.NoError(t, repo.CreateRunStep(done))
		rows, err = repo.FinishSendingStep(done.ID, models.AutomationOutcomeFailed, models.AutomationErrorMailFailed, nil)
		require.NoError(t, err)
		assert.Zero(t, rows, "a row that never was sending is not stamped")
	})

	t.Run("(h) the owner-blind writers are gone", func(t *testing.T) {
		iface := reflect.TypeOf((*AutomationRepository)(nil)).Elem()
		impl := reflect.TypeOf(&automationRepository{})
		for _, name := range []string{"FailRun", "ResetRunToPending", "UpdateRunStepOutcome", "ListRunsByStatus"} {
			_, inInterface := iface.MethodByName(name)
			assert.False(t, inInterface, "AutomationRepository has no %s", name)
			_, inImpl := impl.MethodByName(name)
			assert.False(t, inImpl, "automationRepository has no %s", name)
		}
		for _, name := range []string{"ClaimRun", "RenewRunLease", "AdvanceRun", "ReleaseRun", "ListStaleRunningRuns", "RecoverStaleRun", "FinishSendingStep"} {
			_, ok := iface.MethodByName(name)
			assert.True(t, ok, "AutomationRepository has %s", name)
		}
	})
}
