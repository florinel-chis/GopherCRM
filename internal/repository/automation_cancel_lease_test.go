package repository

// #333: every cancel of an automation run releases its lease. Lead erasure
// (LeadRepository.Delete and DeleteUnconverted) and ticketRepository.Delete
// cancel through cancelAutomationForEntity, which now clears owner_instance
// and heartbeat_at in the same conditional UPDATE that sets cancelled, as
// automationRepository.cancel (CancelRun, CancelRunsForRule,
// CancelRunsForEntity) already did. After any cancel no worker write renews,
// advances, releases, claims or sweeps the run, and a cancel issued after a
// worker's terminal transition or after another cancel changes nothing.
// Full production schema on SQLite and on a disposable MySQL schema; the
// orderings are exercised sequentially.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutomationCancelReleasesLeaseSQLite(t *testing.T) {
	runAutomationCancelReleasesLease(t, migrateIdentityErasureDB(t, testsupport.Open(t, testsupport.SQLite, "identity_erasure", identityErasureDB)))
}

func TestAutomationCancelReleasesLeaseMySQL(t *testing.T) {
	requireTicketMailMySQL(t)
	runAutomationCancelReleasesLease(t, migrateIdentityErasureDB(t, testsupport.Open(t, testsupport.MySQL, "identity_erasure", identityErasureDB)))
}

const (
	cancelLeaseOwner = "cccccccccccccccccccccccccccccccc"
	cancelLeaseOther = "dddddddddddddddddddddddddddddddd"
)

// cancelLeaseSnapshot is every column a cancel may write, read back from
// the committed row.
type cancelLeaseSnapshot struct {
	status, cancelReason, errorCode string
	finishedAt, heartbeatAt         *time.Time
	ownerInstance                   *string
	updatedAt                       time.Time
	nextPosition                    int
}

func takeCancelLeaseSnapshot(t *testing.T, db *gorm.DB, id uint) cancelLeaseSnapshot {
	t.Helper()
	run := leaseRow(t, db, id)
	return cancelLeaseSnapshot{
		status: run.Status, cancelReason: run.CancelReason, errorCode: run.ErrorCode,
		finishedAt: run.FinishedAt, heartbeatAt: run.HeartbeatAt, ownerInstance: run.OwnerInstance,
		updatedAt: run.UpdatedAt, nextPosition: run.NextPosition,
	}
}

func sameInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.UTC().Equal(b.UTC())
}

func assertCancelLeaseSnapshotEqual(t *testing.T, want, got cancelLeaseSnapshot, msg string) {
	t.Helper()
	assert.Equal(t, want.status, got.status, "%s: status", msg)
	assert.Equal(t, want.cancelReason, got.cancelReason, "%s: cancel_reason", msg)
	assert.Equal(t, want.errorCode, got.errorCode, "%s: error_code", msg)
	assert.Equal(t, want.nextPosition, got.nextPosition, "%s: next_position", msg)
	assert.True(t, sameInstant(want.finishedAt, got.finishedAt), "%s: finished_at %v, want %v", msg, got.finishedAt, want.finishedAt)
	assert.True(t, sameInstant(want.heartbeatAt, got.heartbeatAt), "%s: heartbeat_at %v, want %v", msg, got.heartbeatAt, want.heartbeatAt)
	assert.True(t, want.updatedAt.UTC().Equal(got.updatedAt.UTC()), "%s: updated_at %v, want %v", msg, got.updatedAt, want.updatedAt)
	if want.ownerInstance == nil || got.ownerInstance == nil {
		assert.True(t, want.ownerInstance == nil && got.ownerInstance == nil, "%s: owner_instance %v, want %v", msg, got.ownerInstance, want.ownerInstance)
	} else {
		assert.Equal(t, *want.ownerInstance, *got.ownerInstance, "%s: owner_instance", msg)
	}
}

func runAutomationCancelReleasesLease(t *testing.T, db *gorm.DB) {
	repo := NewAutomationRepository(db)
	leads := NewLeadRepository(db)
	tickets := NewTicketRepository(db)
	owner := seedIdentityStaff(t, db, nextIdentityTag(), models.RoleSales)
	customer := seedExternalIDCustomer(t, db, "cancel-lease")

	newLead := func(t *testing.T) *models.Lead {
		t.Helper()
		lead := &models.Lead{FirstName: "Lease", LastName: "Cancel", Email: fmt.Sprintf("lease-cancel-%s@repo.test", nextIdentityTag()), OwnerID: owner.ID}
		require.NoError(t, db.Create(lead).Error)
		return lead
	}
	newTicket := func(t *testing.T) *models.Ticket {
		t.Helper()
		return seedMailTicket(t, db, customer.ID, "cancel-lease-"+nextIdentityTag())
	}
	clock := func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

	// claimed seeds a pending run and claims it with the real ClaimRun, so it
	// holds a lease: running, owner_instance and heartbeat_at set.
	claimed := func(t *testing.T, rule *models.AutomationRule, entityID uint) *models.AutomationRun {
		t.Helper()
		run := seedAutomationRun(t, db, rule, entityID, models.AutomationRunPending)
		at := clock()
		rows, err := repo.ClaimRun(run.ID, models.AutomationRunPending, cancelLeaseOwner, at)
		require.NoError(t, err)
		require.EqualValues(t, 1, rows)
		assertLeaseHeld(t, leaseRow(t, db, run.ID), cancelLeaseOwner, at, "the claim gave the run a real lease")
		return run
	}

	// activeRuns seeds a pending, a waiting and a claimed running run.
	activeRuns := func(t *testing.T, rule *models.AutomationRule, entityID uint) (pending, waiting, running *models.AutomationRun) {
		t.Helper()
		pending = seedAutomationRun(t, db, rule, entityID, models.AutomationRunPending)
		waiting = seedAutomationRun(t, db, rule, entityID, models.AutomationRunWaiting)
		running = claimed(t, rule, entityID)
		return pending, waiting, running
	}

	assertCancelled := func(t *testing.T, id uint, reason string, msg string) {
		t.Helper()
		run := leaseRow(t, db, id)
		assert.Equal(t, models.AutomationRunCancelled, run.Status, "%s: status", msg)
		assert.Equal(t, reason, run.CancelReason, "%s: cancel_reason", msg)
		assert.NotNil(t, run.FinishedAt, "%s: finished_at set", msg)
		assertLeaseCleared(t, run, msg)
	}

	// assertWorkerInert proves no worker write revives or extends the
	// cancelled run (AC-4), and that the row still reads exactly as the
	// cancel left it.
	assertWorkerInert := func(t *testing.T, id uint, position int) {
		t.Helper()
		before := takeCancelLeaseSnapshot(t, db, id)
		require.Equal(t, models.AutomationRunCancelled, before.status)
		now := clock().Add(time.Second)

		err := repo.RenewRunLease(id, cancelLeaseOwner, now)
		assert.True(t, errors.Is(err, ErrAutomationRunNotOwned), "the former owner's renewal is refused, got %v", err)

		rows, err := repo.AdvanceRun(id, position, cancelLeaseOwner, AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: position + 1}, now)
		require.NoError(t, err)
		assert.Zero(t, rows, "an advance that stays running matches nothing")
		rows, err = repo.AdvanceRun(id, position, cancelLeaseOwner, AutomationRunAdvance{Status: models.AutomationRunCompleted, NextPosition: position + 1, FinishedAt: &now}, now)
		require.NoError(t, err)
		assert.Zero(t, rows, "a completing advance matches nothing")
		rows, err = repo.ReleaseRun(id, position, cancelLeaseOwner, now)
		require.NoError(t, err)
		assert.Zero(t, rows, "a release matches nothing")
		for _, from := range []string{models.AutomationRunPending, models.AutomationRunWaiting} {
			rows, err = repo.ClaimRun(id, from, cancelLeaseOther, now)
			require.NoError(t, err)
			assert.Zero(t, rows, "a claim from %s matches nothing", from)
		}
		stale, err := repo.ListStaleRunningRuns(now.Add(time.Hour), 100000)
		require.NoError(t, err)
		for _, run := range stale {
			assert.NotEqual(t, id, run.ID, "the stale sweep does not list a cancelled run")
		}

		after := takeCancelLeaseSnapshot(t, db, id)
		assertCancelLeaseSnapshotEqual(t, before, after, "after the worker writes")
		assert.Nil(t, after.ownerInstance)
		assert.Nil(t, after.heartbeatAt)
	}

	// --- AC-1: lead erasure ---------------------------------------------------

	leadErasures := []struct {
		name  string
		erase func(ctx context.Context, id uint) error
	}{
		{"admin LeadRepository.Delete", leads.Delete},
		{"non-admin DeleteUnconverted of an unconverted lead", leads.DeleteUnconverted},
	}
	for _, erasure := range leadErasures {
		erasure := erasure
		t.Run("lead erasure releases the lease: "+erasure.name, func(t *testing.T) {
			rule := seedAutomationRule(t, db, models.AutomationEntityLead)
			lead, other := newLead(t), newLead(t)
			pending, waiting, running := activeRuns(t, rule, lead.ID)
			otherRunning := claimed(t, rule, other.ID)
			otherPending := seedAutomationRun(t, db, rule, other.ID, models.AutomationRunPending)
			otherRunningBefore := takeCancelLeaseSnapshot(t, db, otherRunning.ID)
			otherPendingBefore := takeCancelLeaseSnapshot(t, db, otherPending.ID)

			require.NoError(t, erasure.erase(context.Background(), lead.ID))

			for _, run := range []*models.AutomationRun{pending, waiting, running} {
				assertCancelled(t, run.ID, models.AutomationCancelRecordDeleted, fmt.Sprintf("run %d (%s)", run.ID, run.Status))
			}
			assertCancelLeaseSnapshotEqual(t, otherRunningBefore, takeCancelLeaseSnapshot(t, db, otherRunning.ID), "another lead's running run keeps its lease")
			assertCancelLeaseSnapshotEqual(t, otherPendingBefore, takeCancelLeaseSnapshot(t, db, otherPending.ID), "another lead's pending run is untouched")
			assertWorkerInert(t, running.ID, running.NextPosition)
		})
	}

	// --- AC-2: ticket delete --------------------------------------------------

	t.Run("ticket delete releases the lease", func(t *testing.T) {
		rule := seedAutomationRule(t, db, models.AutomationEntityTicket)
		ticket, other := newTicket(t), newTicket(t)
		pending, waiting, running := activeRuns(t, rule, ticket.ID)
		otherRunning := claimed(t, rule, other.ID)
		otherRunningBefore := takeCancelLeaseSnapshot(t, db, otherRunning.ID)

		require.NoError(t, tickets.Delete(ticket.ID))

		for _, run := range []*models.AutomationRun{pending, waiting, running} {
			assertCancelled(t, run.ID, models.AutomationCancelRecordDeleted, fmt.Sprintf("run %d (%s)", run.ID, run.Status))
		}
		assertCancelLeaseSnapshotEqual(t, otherRunningBefore, takeCancelLeaseSnapshot(t, db, otherRunning.ID), "another ticket's running run keeps its lease")
		assertWorkerInert(t, running.ID, running.NextPosition)
	})

	t.Run("a failed run cancellation rolls the ticket delete back with the lease intact", func(t *testing.T) {
		rule := seedAutomationRule(t, db, models.AutomationEntityTicket)
		ticket := newTicket(t)
		pending, waiting, running := activeRuns(t, rule, ticket.ID)
		before := map[uint]cancelLeaseSnapshot{}
		for _, run := range []*models.AutomationRun{pending, waiting, running} {
			before[run.ID] = takeCancelLeaseSnapshot(t, db, run.ID)
		}
		sentinel := errors.New("injected automation_runs update failure")
		name := "automation_cancel_lease_test:automation_runs"
		calls := failUpdateOn(t, db, name, "automation_runs", sentinel)
		err := tickets.Delete(ticket.ID)
		require.NoError(t, db.Callback().Update().Remove(name))
		require.ErrorIs(t, err, sentinel)
		require.Positive(t, calls.Load(), "the failure was injected")

		var live models.Ticket
		require.NoError(t, db.First(&live, ticket.ID).Error, "the ticket is still live")
		for id, want := range before {
			assertCancelLeaseSnapshotEqual(t, want, takeCancelLeaseSnapshot(t, db, id), fmt.Sprintf("run %d after the rollback", id))
		}
		assertLeaseHeld(t, leaseRow(t, db, running.ID), cancelLeaseOwner, *before[running.ID].heartbeatAt, "the running run keeps its lease")
	})

	// --- AC-3: the repository cancels -----------------------------------------

	t.Run("CancelRun releases a real lease", func(t *testing.T) {
		rule := seedAutomationRule(t, db, models.AutomationEntityLead)
		lead := newLead(t)
		running := claimed(t, rule, lead.ID)
		rows, err := repo.CancelRun(running.ID, models.AutomationCancelManual, clock())
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)
		assertCancelled(t, running.ID, models.AutomationCancelManual, "CancelRun")
		assertWorkerInert(t, running.ID, running.NextPosition)
	})

	for _, reason := range []string{models.AutomationCancelRuleChanged, models.AutomationCancelRuleDisabled, models.AutomationCancelRuleDeleted} {
		reason := reason
		t.Run("CancelRunsForRule releases a real lease: "+reason, func(t *testing.T) {
			rule, otherRule := seedAutomationRule(t, db, models.AutomationEntityLead), seedAutomationRule(t, db, models.AutomationEntityLead)
			lead := newLead(t)
			pending, waiting, running := activeRuns(t, rule, lead.ID)
			otherRunning := claimed(t, otherRule, lead.ID)
			otherBefore := takeCancelLeaseSnapshot(t, db, otherRunning.ID)
			rows, err := repo.CancelRunsForRule(rule.ID, reason, clock())
			require.NoError(t, err)
			assert.EqualValues(t, 3, rows)
			for _, run := range []*models.AutomationRun{pending, waiting, running} {
				assertCancelled(t, run.ID, reason, fmt.Sprintf("run %d (%s)", run.ID, run.Status))
			}
			assertCancelLeaseSnapshotEqual(t, otherBefore, takeCancelLeaseSnapshot(t, db, otherRunning.ID), "another rule's run keeps its lease")
			assertWorkerInert(t, running.ID, running.NextPosition)
		})
	}

	t.Run("CancelRunsForEntity releases a real lease", func(t *testing.T) {
		rule := seedAutomationRule(t, db, models.AutomationEntityLead)
		lead, other := newLead(t), newLead(t)
		pending, waiting, running := activeRuns(t, rule, lead.ID)
		otherRunning := claimed(t, rule, other.ID)
		otherBefore := takeCancelLeaseSnapshot(t, db, otherRunning.ID)
		rows, err := repo.CancelRunsForEntity(models.AutomationEntityLead, lead.ID, models.AutomationCancelRecordDeleted, clock())
		require.NoError(t, err)
		assert.EqualValues(t, 3, rows)
		for _, run := range []*models.AutomationRun{pending, waiting, running} {
			assertCancelled(t, run.ID, models.AutomationCancelRecordDeleted, fmt.Sprintf("run %d (%s)", run.ID, run.Status))
		}
		assertCancelLeaseSnapshotEqual(t, otherBefore, takeCancelLeaseSnapshot(t, db, otherRunning.ID), "another lead's run keeps its lease")
		assertWorkerInert(t, running.ID, running.NextPosition)
	})

	// --- AC-5: a late cancel changes nothing ----------------------------------

	type ending struct {
		name string
		end  func(t *testing.T, run *models.AutomationRun)
	}
	endings := []ending{
		{"completed by its owner", func(t *testing.T, run *models.AutomationRun) {
			at := clock()
			rows, err := repo.AdvanceRun(run.ID, run.NextPosition, cancelLeaseOwner,
				AutomationRunAdvance{Status: models.AutomationRunCompleted, NextPosition: run.NextPosition + 1, FinishedAt: &at}, at)
			require.NoError(t, err)
			require.EqualValues(t, 1, rows)
		}},
		{"failed by its owner", func(t *testing.T, run *models.AutomationRun) {
			at := clock()
			rows, err := repo.AdvanceRun(run.ID, run.NextPosition, cancelLeaseOwner,
				AutomationRunAdvance{Status: models.AutomationRunFailed, NextPosition: run.NextPosition, FinishedAt: &at, ErrorCode: "step_failed"}, at)
			require.NoError(t, err)
			require.EqualValues(t, 1, rows)
		}},
		{"cancelled by another cancel", func(t *testing.T, run *models.AutomationRun) {
			rows, err := repo.CancelRun(run.ID, models.AutomationCancelManual, clock())
			require.NoError(t, err)
			require.EqualValues(t, 1, rows)
		}},
	}

	type lateCancel struct {
		name   string
		entity string
		cancel func(t *testing.T, rule *models.AutomationRule, run *models.AutomationRun)
	}
	lateCancels := []lateCancel{
		{"lead erasure", models.AutomationEntityLead, func(t *testing.T, _ *models.AutomationRule, run *models.AutomationRun) {
			require.NoError(t, leads.Delete(context.Background(), run.EntityID))
		}},
		{"ticket delete", models.AutomationEntityTicket, func(t *testing.T, _ *models.AutomationRule, run *models.AutomationRun) {
			require.NoError(t, tickets.Delete(run.EntityID))
		}},
		{"CancelRun", models.AutomationEntityLead, func(t *testing.T, _ *models.AutomationRule, run *models.AutomationRun) {
			rows, err := repo.CancelRun(run.ID, models.AutomationCancelRuleDeleted, clock().Add(time.Second))
			require.NoError(t, err)
			assert.Zero(t, rows, "CancelRun reports 0 rows on a finished run")
		}},
		{"CancelRunsForRule", models.AutomationEntityLead, func(t *testing.T, rule *models.AutomationRule, _ *models.AutomationRun) {
			rows, err := repo.CancelRunsForRule(rule.ID, models.AutomationCancelRuleChanged, clock().Add(time.Second))
			require.NoError(t, err)
			assert.Zero(t, rows, "CancelRunsForRule matches no finished run")
		}},
	}

	for _, late := range lateCancels {
		late := late
		for _, end := range endings {
			end := end
			t.Run("a "+late.name+" after the run was "+end.name+" changes nothing", func(t *testing.T) {
				rule := seedAutomationRule(t, db, late.entity)
				var entityID uint
				if late.entity == models.AutomationEntityTicket {
					entityID = newTicket(t).ID
				} else {
					entityID = newLead(t).ID
				}
				run := claimed(t, rule, entityID)
				end.end(t, run)
				before := takeCancelLeaseSnapshot(t, db, run.ID)
				require.NotEqual(t, models.AutomationRunRunning, before.status)
				// A later clock: an UPDATE that matched the row would move
				// updated_at and finished_at.
				time.Sleep(5 * time.Millisecond)
				late.cancel(t, rule, run)
				assertCancelLeaseSnapshotEqual(t, before, takeCancelLeaseSnapshot(t, db, run.ID), "the late cancel")
			})
		}
	}
}
