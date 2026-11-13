package repository

// AC-6 of #242: ticketRepository.Delete runs one transaction that locks the
// live ticket row, hard-deletes its ticket_mail_deliveries, cancels its
// pending/waiting/running automation runs (cancelled, record_deleted,
// finished_at), hard-deletes the notifications about the ticket, then
// soft-deletes the ticket. Finished runs, other tickets' runs and lead/deal
// notifications are untouched; an injected failure in any step rolls all of
// them back; absent automation tables are skipped by HasTable probes. Full
// production schema on SQLite and on a disposable MySQL schema.

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestTicketDeleteCancelsAutomationSQLite(t *testing.T) {
	runTicketDeleteCancelsAutomation(t, openIdentityErasureDB(t, false))
	t.Run("absent automation tables are skipped", runTicketDeleteWithoutAutomationTables)
}

func TestTicketDeleteCancelsAutomationMySQL(t *testing.T) {
	requireTicketMailMySQL(t)
	runTicketDeleteCancelsAutomation(t, openIdentityErasureDB(t, true))
}

// ticketAutomationFixture is one ticket with every kind of row the delete
// reaches, plus the rows it must leave alone.
type ticketAutomationFixture struct {
	target, other              *models.Ticket
	active, finished, otherRun []models.AutomationRun
	foreignRun                 models.AutomationRun
	targetNotes, keptNotes     []models.Notification
}

var ticketDeleteSeq struct {
	sync.Mutex
	n int
}

func nextTicketDeleteKey() string {
	ticketDeleteSeq.Lock()
	defer ticketDeleteSeq.Unlock()
	ticketDeleteSeq.n++
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), ticketDeleteSeq.n)
}

func seedTicketAutomationFixture(t *testing.T, db *gorm.DB) *ticketAutomationFixture {
	t.Helper()
	staff := seedIdentityStaff(t, db, nextIdentityTag(), models.RoleSupport)
	customer := seedExternalIDCustomer(t, db, "tauto")
	f := &ticketAutomationFixture{
		target: seedMailTicket(t, db, customer.ID, "auto-target"),
		other:  seedMailTicket(t, db, customer.ID, "auto-other"),
	}
	seedMailDelivery(t, db, f.target.ID, customer.ID, staff.ID)
	seedMailDelivery(t, db, f.other.ID, customer.ID, staff.ID)

	rule := &models.AutomationRule{Name: "Ticket rule", EntityType: models.AutomationEntityTicket,
		TriggerType: models.AutomationEventTicketUpdated, Enabled: true}
	rule.Steps = []models.AutomationStep{{StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{Title: "T"}}}
	require.NoError(t, NewAutomationRepository(db).CreateRule(rule))
	run := func(entityType string, entityID uint, status string) models.AutomationRun {
		r := models.AutomationRun{RuleID: rule.ID, RuleVersion: 1, EntityType: entityType, EntityID: entityID,
			DedupeKey: "tdel:" + nextTicketDeleteKey(), Status: status, NextPosition: 1}
		if status == models.AutomationRunWaiting {
			resume := time.Now().UTC().Add(time.Hour)
			r.ResumeAt = &resume
		}
		if status == models.AutomationRunCancelled {
			r.CancelReason = models.AutomationCancelManual
		}
		require.NoError(t, db.Create(&r).Error)
		return r
	}
	for _, status := range []string{models.AutomationRunPending, models.AutomationRunWaiting, models.AutomationRunRunning} {
		f.active = append(f.active, run(models.AutomationEntityTicket, f.target.ID, status))
	}
	for _, status := range []string{models.AutomationRunCompleted, models.AutomationRunFailed, models.AutomationRunCancelled} {
		f.finished = append(f.finished, run(models.AutomationEntityTicket, f.target.ID, status))
	}
	f.otherRun = append(f.otherRun, run(models.AutomationEntityTicket, f.other.ID, models.AutomationRunPending))
	// A lead run whose id happens to equal the ticket's.
	f.foreignRun = run(models.AutomationEntityLead, f.target.ID, models.AutomationRunWaiting)

	note := func(entityType string, entityID uint) models.Notification {
		n := models.Notification{UserID: staff.ID, Title: "N", EntityType: entityType, EntityID: entityID}
		require.NoError(t, db.Create(&n).Error)
		return n
	}
	f.targetNotes = []models.Notification{note(models.AutomationEntityTicket, f.target.ID), note(models.AutomationEntityTicket, f.target.ID)}
	f.keptNotes = []models.Notification{
		note(models.AutomationEntityTicket, f.other.ID),
		note(models.AutomationEntityLead, f.target.ID),
		note(models.AutomationEntityDeal, f.target.ID),
	}
	return f
}

func loadRun(t *testing.T, db *gorm.DB, id uint) models.AutomationRun {
	t.Helper()
	var run models.AutomationRun
	require.NoError(t, db.First(&run, id).Error)
	return run
}

func countNotifications(t *testing.T, db *gorm.DB, ids []models.Notification) int64 {
	t.Helper()
	list := make([]uint, 0, len(ids))
	for _, n := range ids {
		list = append(list, n.ID)
	}
	var count int64
	require.NoError(t, db.Unscoped().Model(&models.Notification{}).Where("id IN ?", list).Count(&count).Error)
	return count
}

// assertTicketDeleteUntouched checks the fixture as it was before a refused
// or rolled-back delete.
func assertTicketDeleteUntouched(t *testing.T, db *gorm.DB, f *ticketAutomationFixture) {
	t.Helper()
	var live models.Ticket
	require.NoError(t, db.First(&live, f.target.ID).Error, "the ticket is still live")
	assert.EqualValues(t, 1, countTicketMailDeliveries(t, db, "ticket_id = ?", f.target.ID), "the deliveries are kept")
	for _, run := range f.active {
		got := loadRun(t, db, run.ID)
		assert.Equal(t, run.Status, got.Status, "run %d keeps its status", run.ID)
		assert.Empty(t, got.CancelReason)
		assert.Nil(t, got.FinishedAt)
	}
	assert.EqualValues(t, len(f.targetNotes), countNotifications(t, db, f.targetNotes), "the notifications are kept")
}

func runTicketDeleteCancelsAutomation(t *testing.T, db *gorm.DB) {
	repo := NewTicketRepository(db)

	t.Run("cancels the ticket's open runs and purges its notifications", func(t *testing.T) {
		f := seedTicketAutomationFixture(t, db)
		before := time.Now().UTC().Add(-time.Second)
		require.NoError(t, repo.Delete(f.target.ID))

		var deleted models.Ticket
		require.NoError(t, db.Unscoped().First(&deleted, f.target.ID).Error)
		assert.True(t, deleted.DeletedAt.Valid, "the ticket is soft-deleted")
		assert.Zero(t, countTicketMailDeliveries(t, db, "ticket_id = ?", f.target.ID), "the deliveries are purged")
		assert.EqualValues(t, 1, countTicketMailDeliveries(t, db, "ticket_id = ?", f.other.ID))

		for _, run := range f.active {
			got := loadRun(t, db, run.ID)
			assert.Equal(t, models.AutomationRunCancelled, got.Status, "run %d (%s)", run.ID, run.Status)
			assert.Equal(t, models.AutomationCancelRecordDeleted, got.CancelReason)
			require.NotNil(t, got.FinishedAt)
			assert.False(t, got.FinishedAt.Before(before.Truncate(time.Second)))
		}
		for _, run := range f.finished {
			got := loadRun(t, db, run.ID)
			assert.Equal(t, run.Status, got.Status, "a finished run is untouched")
			assert.Equal(t, run.CancelReason, got.CancelReason)
		}
		for _, run := range append(f.otherRun, f.foreignRun) {
			got := loadRun(t, db, run.ID)
			assert.Equal(t, run.Status, got.Status, "other records' runs are untouched")
			assert.Empty(t, got.CancelReason)
		}
		assert.Zero(t, countNotifications(t, db, f.targetNotes), "the ticket's notifications are hard-deleted")
		assert.EqualValues(t, len(f.keptNotes), countNotifications(t, db, f.keptNotes), "other notifications are kept")
	})

	t.Run("a missing or deleted ticket is not found and writes nothing", func(t *testing.T) {
		require.ErrorIs(t, repo.Delete(99999999), gorm.ErrRecordNotFound)
		f := seedTicketAutomationFixture(t, db)
		require.NoError(t, repo.Delete(f.target.ID))
		require.ErrorIs(t, repo.Delete(f.target.ID), gorm.ErrRecordNotFound, "a second delete finds no live row")
	})

	failures := []struct {
		name, table string
		update      bool
	}{
		{"mail delivery purge", "ticket_mail_deliveries", false},
		{"run cancellation", "automation_runs", true},
		{"notification purge", "notifications", false},
		{"ticket soft delete", "tickets", false},
	}
	for _, failure := range failures {
		failure := failure
		t.Run("a failure in the "+failure.name+" rolls everything back", func(t *testing.T) {
			f := seedTicketAutomationFixture(t, db)
			sentinel := errors.New("injected failure: " + failure.name)
			name := "ticket_delete_automation_test:" + failure.table
			var calls interface{ Load() int32 }
			if failure.update {
				calls = failUpdateOn(t, db, name, failure.table, sentinel)
			} else {
				calls = failOn(t, db, name, failure.table, sentinel)
			}
			err := repo.Delete(f.target.ID)
			require.ErrorIs(t, err, sentinel)
			require.Positive(t, calls.Load(), "the failure was injected")
			if failure.update {
				require.NoError(t, db.Callback().Update().Remove(name))
			} else {
				require.NoError(t, db.Callback().Delete().Remove(name))
			}
			assertTicketDeleteUntouched(t, db, f)
		})
	}

	t.Run("a WithTx caller that rolls back keeps everything", func(t *testing.T) {
		f := seedTicketAutomationFixture(t, db)
		rollback := errors.New("caller rolls back")
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := repo.WithTx(tx).Delete(f.target.ID); err != nil {
				return err
			}
			return rollback
		})
		require.ErrorIs(t, err, rollback)
		assertTicketDeleteUntouched(t, db, f)
	})

	t.Run("the ticket row is locked first", func(t *testing.T) {
		f := seedTicketAutomationFixture(t, db)
		log := captureRepoStatements(t, db)
		require.NoError(t, repo.Delete(f.target.ID))
		lock := log.first(regexp.MustCompile("(?is)^SELECT .*FROM [`\"]?tickets[`\"]?"))
		cancel := log.first(regexp.MustCompile("(?is)^UPDATE [`\"]?automation_runs[`\"]?"))
		soft := log.first(regexp.MustCompile("(?is)^UPDATE [`\"]?tickets[`\"]? SET [`\"]?deleted_at"))
		require.GreaterOrEqual(t, lock, 0, log.String())
		assert.Less(t, lock, cancel, "the lock precedes the cancellation")
		assert.Less(t, cancel, soft, "the soft delete comes last")
		if db.Dialector.Name() == "mysql" {
			assert.GreaterOrEqual(t, log.first(regexp.MustCompile("(?is)^SELECT .*FROM `tickets`.*FOR UPDATE")), 0,
				"the lock is FOR UPDATE on MySQL:\n%s", log.String())
		}
	})
}

// failUpdateOn makes every update of the table fail with the sentinel until
// the callback is removed.
func failUpdateOn(t *testing.T, db *gorm.DB, name, table string, sentinel error) interface{ Load() int32 } {
	t.Helper()
	calls := &countingCalls{}
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != table {
			return
		}
		calls.add()
		_ = tx.AddError(sentinel)
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
	return calls
}

type countingCalls struct {
	mu sync.Mutex
	n  int32
}

func (c *countingCalls) add() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *countingCalls) Load() int32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// runTicketDeleteWithoutAutomationTables deletes a ticket in a schema that
// has neither the automation tables nor the notifications nor the mail
// deliveries: the probes skip them.
func runTicketDeleteWithoutAutomationTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subset.db")
	db, err := gorm.Open(sqlite.Open("file:"+path+"?_pragma=foreign_keys(1)"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Customer{}, &models.Ticket{}, &models.TicketComment{}))
	for _, model := range []interface{}{&models.AutomationRun{}, &models.Notification{}, &models.TicketMailDelivery{}} {
		require.False(t, db.Migrator().HasTable(model), "the fixture must not have %T", model)
	}
	customer := &models.Customer{FirstName: "Sub", LastName: "Set", Email: "subset-" + nextTicketDeleteKey() + "@example.test"}
	require.NoError(t, db.Create(customer).Error)
	ticket := &models.Ticket{Title: "Subset", Description: "d", Status: models.TicketStatusOpen,
		Priority: models.TicketPriorityLow, CustomerID: customer.ID}
	require.NoError(t, db.Create(ticket).Error)

	require.NoError(t, NewTicketRepository(db).Delete(ticket.ID))
	var deleted models.Ticket
	require.NoError(t, db.Unscoped().First(&deleted, ticket.ID).Error)
	assert.True(t, deleted.DeletedAt.Valid)
}
