package repository

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AC-16: lead erasure, inside the erasure transaction, cancels the lead's
// pending, waiting and running runs with record_deleted and hard-deletes the
// notifications whose entity is the lead; user erasure hard-deletes the
// user's notifications; a failure in these steps rolls the whole erasure
// back. On MySQL, an erasure committed from a second connection while a step
// of the lead's run is paused after its first read wins: once it has
// committed the run is cancelled and no task, notification or run-step row
// is added for it.

func TestAutomationErasureSQLite(t *testing.T) {
	runAutomationErasure(t, openAutomationRepoDB(t, false, 1))
}

func TestAutomationErasureMySQL(t *testing.T) {
	runAutomationErasure(t, openAutomationRepoDB(t, true, 4))
}

func seedNotification(t *testing.T, db *gorm.DB, userID uint, entityType string, entityID uint) *models.Notification {
	t.Helper()
	notification := &models.Notification{UserID: userID, Title: "Follow up", EntityType: entityType, EntityID: entityID}
	require.NoError(t, db.Create(notification).Error)
	return notification
}

func runAutomationErasure(t *testing.T, db *gorm.DB) {
	repo := NewAutomationRepository(db)
	owner := seedAutomationUser(t, db, "owner", models.RoleSales)
	colleague := seedAutomationUser(t, db, "colleague", models.RoleSales)
	rule := seedAutomationRule(t, db, models.AutomationEntityLead)
	newLead := func() *models.Lead {
		lead := &models.Lead{FirstName: "Erase", LastName: "Me", Email: fmt.Sprintf("erase-%d@repo.test", time.Now().UnixNano()), Phone: "+1 555 0100", OwnerID: owner.ID}
		require.NoError(t, db.Create(lead).Error)
		return lead
	}
	runStatus := func(id uint) (string, string) {
		run, err := repo.GetRunByID(id)
		require.NoError(t, err)
		return run.Status, run.CancelReason
	}
	countNotifications := func(where string, args ...interface{}) int64 {
		var count int64
		require.NoError(t, db.Model(&models.Notification{}).Where(where, args...).Count(&count).Error)
		return count
	}

	t.Run("lead erasure cancels the active runs and purges the lead's notifications", func(t *testing.T) {
		lead, other := newLead(), newLead()
		pending := seedAutomationRun(t, db, rule, lead.ID, models.AutomationRunPending)
		waiting := seedAutomationRun(t, db, rule, lead.ID, models.AutomationRunWaiting)
		running := seedAutomationRun(t, db, rule, lead.ID, models.AutomationRunRunning)
		completed := seedAutomationRun(t, db, rule, lead.ID, models.AutomationRunCompleted)
		otherRun := seedAutomationRun(t, db, rule, other.ID, models.AutomationRunPending)
		seedNotification(t, db, owner.ID, models.AutomationEntityLead, lead.ID)
		seedNotification(t, db, colleague.ID, models.AutomationEntityLead, lead.ID)
		kept := seedNotification(t, db, owner.ID, models.AutomationEntityLead, other.ID)

		require.NoError(t, NewLeadRepository(db).Delete(lead.ID))

		for _, id := range []uint{pending.ID, waiting.ID, running.ID} {
			status, reason := runStatus(id)
			assert.Equal(t, models.AutomationRunCancelled, status, "run %d", id)
			assert.Equal(t, models.AutomationCancelRecordDeleted, reason, "run %d", id)
		}
		status, _ := runStatus(completed.ID)
		assert.Equal(t, models.AutomationRunCompleted, status, "a finished run is left alone")
		status, _ = runStatus(otherRun.ID)
		assert.Equal(t, models.AutomationRunPending, status, "another lead's run is left alone")
		assert.Zero(t, countNotifications("entity_type = ? AND entity_id = ?", models.AutomationEntityLead, lead.ID))
		assert.EqualValues(t, 1, countNotifications("id = ?", kept.ID))
		var erased models.Lead
		require.NoError(t, db.Unscoped().First(&erased, lead.ID).Error)
		assert.True(t, erased.DeletedAt.Valid)
		assert.Empty(t, erased.FirstName)
		assert.True(t, strings.HasSuffix(erased.Email, "@anonymized.invalid"))
	})

	t.Run("user erasure purges the user's notifications", func(t *testing.T) {
		leaver := seedAutomationUser(t, db, "leaver", models.RoleSupport)
		lead := newLead()
		seedNotification(t, db, leaver.ID, models.AutomationEntityLead, lead.ID)
		seedNotification(t, db, leaver.ID, models.AutomationEntityDeal, 42)
		kept := seedNotification(t, db, owner.ID, models.AutomationEntityLead, lead.ID)
		require.NoError(t, NewUserRepository(db).Delete(leaver.ID))
		assert.Zero(t, countNotifications("user_id = ?", leaver.ID))
		assert.EqualValues(t, 1, countNotifications("id = ?", kept.ID))
	})

	t.Run("a failure in the automation steps rolls the whole erasure back", func(t *testing.T) {
		lead := newLead()
		run := seedAutomationRun(t, db, rule, lead.ID, models.AutomationRunPending)
		seedNotification(t, db, owner.ID, models.AutomationEntityLead, lead.ID)
		require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("probe:fail-notification-purge", func(tx *gorm.DB) {
			if tx.Statement.Table == "notifications" {
				tx.AddError(errors.New("injected notification purge fault"))
			}
		}))
		err := NewLeadRepository(db).Delete(lead.ID)
		require.NoError(t, db.Callback().Delete().Remove("probe:fail-notification-purge"))
		require.Error(t, err)

		status, reason := runStatus(run.ID)
		assert.Equal(t, models.AutomationRunPending, status, "the cancel was rolled back")
		assert.Empty(t, reason)
		assert.EqualValues(t, 1, countNotifications("entity_type = ? AND entity_id = ?", models.AutomationEntityLead, lead.ID))
		var intact models.Lead
		require.NoError(t, db.Unscoped().First(&intact, lead.ID).Error)
		assert.False(t, intact.DeletedAt.Valid, "the lead is still live")
		assert.Equal(t, "Erase", intact.FirstName, "and still holds its data")
		assert.Equal(t, lead.Email, intact.Email)
	})

	if db.Dialector.Name() != "mysql" {
		return
	}

	t.Run("an erasure committed while a step is paused after its first read wins", func(t *testing.T) {
		lead := newLead()
		run := seedAutomationRun(t, db, rule, lead.ID, models.AutomationRunPending)
		now := time.Now().UTC()
		rows, err := repo.ClaimRun(run.ID, models.AutomationRunPending, now)
		require.NoError(t, err)
		require.EqualValues(t, 1, rows)

		// The step transaction, as the executor opens it: the locking read of
		// the lead, then the pause.
		step := db.Begin()
		require.NoError(t, step.Error)
		stepRepo := repo.WithTx(step)
		locked, err := stepRepo.GetLeadForUpdate(lead.ID)
		require.NoError(t, err)
		require.Equal(t, lead.ID, locked.ID)

		// The erasure from a second connection: it cancels the run, purges
		// the notifications and then waits for the lead row the step holds.
		erased := make(chan error, 1)
		go func() { erased <- NewLeadRepository(db).Delete(lead.ID) }()
		time.Sleep(500 * time.Millisecond)
		select {
		case err := <-erased:
			t.Fatalf("the erasure must block on the step's lock, got %v", err)
		default:
		}

		// The step resumes with its conditional advance, which needs the run
		// row the erasure holds: InnoDB resolves the deadlock against the
		// transaction that modified nothing, which is the step.
		rows, err = stepRepo.AdvanceRun(run.ID, 1, AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: 2}, now)
		if err == nil {
			require.Zero(t, rows, "the advance must not match once the erasure holds the run")
		} else {
			assert.Contains(t, strings.ToLower(err.Error()), "deadlock", "the step is the deadlock victim")
		}
		_ = step.Rollback()

		select {
		case err := <-erased:
			require.NoError(t, err, "the erasure commits")
		case <-time.After(30 * time.Second):
			t.Fatal("the erasure did not finish")
		}

		// A retried step finds the record gone and the run cancelled, so it
		// adds nothing.
		retry := db.Begin()
		require.NoError(t, retry.Error)
		_, err = repo.WithTx(retry).GetLeadForUpdate(lead.ID)
		assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "the erased lead is not found, got %v", err)
		rows, err = repo.WithTx(retry).AdvanceRun(run.ID, 1, AutomationRunAdvance{Status: models.AutomationRunCancelled, NextPosition: 1, CancelReason: models.AutomationCancelRecordDeleted}, now)
		require.NoError(t, err)
		assert.Zero(t, rows, "the run was cancelled by the erasure")
		require.NoError(t, retry.Rollback().Error)

		status, reason := runStatus(run.ID)
		assert.Equal(t, models.AutomationRunCancelled, status)
		assert.Equal(t, models.AutomationCancelRecordDeleted, reason)
		steps, err := repo.ListRunSteps(run.ID)
		require.NoError(t, err)
		assert.Empty(t, steps, "no run-step row")
		var tasks int64
		require.NoError(t, db.Model(&models.Task{}).Count(&tasks).Error)
		assert.Zero(t, tasks)
		assert.Zero(t, countNotifications("entity_type = ? AND entity_id = ?", models.AutomationEntityLead, lead.ID))
	})
}
