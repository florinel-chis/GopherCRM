package service

// The task events (#516): task.created, task.updated and task.deleted are
// written by every task write path (TaskService CreateWithLabels,
// UpdateWithLabels and Delete, the bulk task status batch and the automation
// create_task step) inside the writing transaction, one pending row per
// active subscribed endpoint, on SQLite and on a disposable TEST_MYSQL_DSN
// schema opened with loc=Local (the MySQL roots skip without the variable and
// fail when it is unreachable). task.deleted follows the #531 rule: every
// active endpoint subscribed to it, whether or not it received an earlier
// event for the task.

import (
	"context"
	"fmt"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTaskWebhookFanOutSQLite(t *testing.T) {
	runTaskWebhookFanOut(t, openTicketIntakeDB(t, false))
}

func TestTaskWebhookFanOutMySQL(t *testing.T) {
	runTaskWebhookFanOut(t, openDealWebhookMySQLDB(t))
}

func TestTaskWebhookAtomicitySQLite(t *testing.T) {
	runTaskWebhookAtomicity(t, openTicketIntakeDB(t, false))
}

func TestTaskWebhookAtomicityMySQL(t *testing.T) {
	runTaskWebhookAtomicity(t, openDealWebhookMySQLDB(t))
}

// storedWebhookTask is a fresh read of the row, soft-deleted included.
func storedWebhookTask(t *testing.T, db *gorm.DB, id uint) models.Task {
	t.Helper()
	var task models.Task
	require.NoError(t, db.Unscoped().First(&task, id).Error)
	return task
}

// storedTaskLabelIDs is the task's label set as stored, ascending.
func storedTaskLabelIDs(t *testing.T, db *gorm.DB, id uint) []uint {
	t.Helper()
	ids := []uint{}
	require.NoError(t, db.Table("task_labels").Where("task_id = ?", id).Order("label_id").Pluck("label_id", &ids).Error)
	return ids
}

// seedLabel writes a label directly.
func (e *webhookEnv) seedLabel() *models.Label {
	e.t.Helper()
	label := &models.Label{Name: fmt.Sprintf("Webhook label %d", webhookTestSeq.Add(1)), Color: "#336699"}
	require.NoError(e.t, e.db.Create(label).Error)
	return label
}

// seedTask writes a task assigned to assignee directly (no webhook), with a
// due date and the given labels.
func (e *webhookEnv) seedTask(assignee uint, status models.TaskStatus, labels ...models.Label) *models.Task {
	e.t.Helper()
	due := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	task := &models.Task{Title: "Seeded task", Description: "d", Status: status, Priority: models.TaskPriorityMedium,
		DueDate: &due, AssignedToID: assignee}
	require.NoError(e.t, repository.NewTaskRepository(e.db).CreateWithLabels(task, labels))
	return task
}

// taskAsServiceReads is the task a PUT /tasks/{id} that repeats the stored
// values hands the service: the service's own read, associations included.
func taskAsServiceReads(t *testing.T, env *webhookEnv, id uint) *models.Task {
	t.Helper()
	task, err := env.tasks.GetByID(id)
	require.NoError(t, err)
	return task
}

func uintsPtr(ids ...uint) *[]uint {
	out := append([]uint{}, ids...)
	return &out
}

func runTaskWebhookFanOut(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	env.wire(env.repo)
	ctx := context.Background()
	admin := env.owner.ID
	all := webhook.AllEventTypes()
	require.Len(t, all, 21)
	var allButDeleted []string
	for _, eventType := range all {
		if eventType != "task.deleted" {
			allButDeleted = append(allButDeleted, eventType)
		}
	}
	require.Len(t, allButDeleted, 20)
	// a: everything. b: task.deleted only, so it never receives a created or
	// updated event. c: every type but task.deleted. d: inactive, everything.
	a := env.endpoint("https://example.com/a", true, all...)
	b := env.endpoint("https://example.com/b", true, "task.deleted")
	c := env.endpoint("https://example.com/c", true, allButDeleted...)
	d := env.endpoint("https://example.com/d", false, all...)
	noRowFor := func(t *testing.T, endpoints ...uint) {
		t.Helper()
		for _, id := range endpoints {
			assert.Zero(t, env.count(&models.WebhookDelivery{}, "endpoint_id = ?", id), "endpoint %d", id)
		}
	}
	noLeadRows := func(t *testing.T) {
		t.Helper()
		assert.Zero(t, env.count(&models.WebhookDelivery{}, "event_type LIKE ?", "lead.%"), "no lead.* row")
	}
	labelA, labelB := env.seedLabel(), env.seedLabel()
	lead := env.seedLead()
	deal := env.seedDeal(models.DealStageProposal)

	t.Run("CreateWithLabels without labels writes task.created", func(t *testing.T) {
		env.clearDeliveries()
		task := &models.Task{Title: "Webhook task", AssignedToID: admin}
		require.NoError(t, env.tasks.CreateWithLabels(task, nil, admin))
		require.NotZero(t, task.ID)
		assertFanOut(t, env, "task.created", task.ID, a.ID, c.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
		noRowFor(t, b.ID, d.ID)
	})

	t.Run("CreateWithLabels with labels writes task.created", func(t *testing.T) {
		env.clearDeliveries()
		task := &models.Task{Title: "Labelled task", AssignedToID: admin}
		require.NoError(t, env.tasks.CreateWithLabels(task, []uint{labelB.ID, labelA.ID}, admin))
		require.NotZero(t, task.ID)
		assert.Equal(t, []uint{labelA.ID, labelB.ID}, storedTaskLabelIDs(t, db, task.ID))
		assertFanOut(t, env, "task.created", task.ID, a.ID, c.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
		noRowFor(t, b.ID, d.ID)
	})

	t.Run("a lead-linked create writes no lead row", func(t *testing.T) {
		env.clearDeliveries()
		task := &models.Task{Title: "Lead task", AssignedToID: admin, LeadID: &lead.ID}
		require.NoError(t, env.tasks.CreateWithLabels(task, nil, admin))
		assertFanOut(t, env, "task.created", task.ID, a.ID, c.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
		noLeadRows(t)
	})

	// The task the update subtests edit: a due date, a lead link, a deal link
	// and the label set {a, b}; bare has the same links and no labels.
	due := time.Now().Add(96 * time.Hour).Truncate(time.Second)
	linked := &models.Task{Title: "Linked task", Description: "x", AssignedToID: admin, DueDate: &due, LeadID: &lead.ID, DealID: &deal.ID}
	require.NoError(t, env.tasks.CreateWithLabels(linked, []uint{labelA.ID, labelB.ID}, admin))
	bare := &models.Task{Title: "Bare task", Description: "y", AssignedToID: admin, DueDate: &due, LeadID: &lead.ID, DealID: &deal.ID}
	require.NoError(t, env.tasks.CreateWithLabels(bare, nil, admin))

	for _, tc := range []struct {
		name   string
		task   *models.Task
		labels *[]uint
	}{
		{"label ids nil", linked, nil},
		{"the stored set reordered", linked, uintsPtr(labelB.ID, labelA.ID)},
		{"the stored set with duplicates", linked, uintsPtr(labelB.ID, labelA.ID, labelA.ID)},
		{"an empty list against an empty set", bare, uintsPtr()},
	} {
		t.Run("an unchanged update writes no row: "+tc.name, func(t *testing.T) {
			env.clearDeliveries()
			same := taskAsServiceReads(t, env, tc.task.ID)
			require.NotNil(t, same.DueDate)
			require.NotNil(t, same.LeadID)
			require.NotNil(t, same.DealID)
			require.NoError(t, env.tasks.UpdateWithLabels(same, tc.labels, false, false, admin))
			assert.Zero(t, env.allDeliveries(), "the stored values are no update")
		})
	}

	t.Run("a changed title writes task.updated", func(t *testing.T) {
		env.clearDeliveries()
		changed := taskAsServiceReads(t, env, linked.ID)
		changed.Title = "Linked task, renamed"
		require.NoError(t, env.tasks.UpdateWithLabels(changed, nil, false, false, admin))
		assert.Equal(t, "Linked task, renamed", storedWebhookTask(t, db, linked.ID).Title)
		assertFanOut(t, env, "task.updated", linked.ID, a.ID, c.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
		noRowFor(t, b.ID, d.ID)
		noLeadRows(t)
	})

	t.Run("a changed label set alone writes task.updated", func(t *testing.T) {
		env.clearDeliveries()
		same := taskAsServiceReads(t, env, linked.ID)
		require.NoError(t, env.tasks.UpdateWithLabels(same, uintsPtr(labelA.ID), false, false, admin))
		assert.Equal(t, []uint{labelA.ID}, storedTaskLabelIDs(t, db, linked.ID))
		assertFanOut(t, env, "task.updated", linked.ID, a.ID, c.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
	})

	t.Run("an empty list against a non-empty set writes task.updated", func(t *testing.T) {
		env.clearDeliveries()
		same := taskAsServiceReads(t, env, linked.ID)
		require.NoError(t, env.tasks.UpdateWithLabels(same, uintsPtr(), false, false, admin))
		assert.Empty(t, storedTaskLabelIDs(t, db, linked.ID))
		assertFanOut(t, env, "task.updated", linked.ID, a.ID, c.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
	})

	t.Run("bulk status writes task.updated for the changed tasks only", func(t *testing.T) {
		env.clearDeliveries()
		already := env.seedTask(admin, models.TaskStatusInProgress)
		first := env.seedTask(admin, models.TaskStatusPending)
		second := env.seedTask(admin, models.TaskStatusPending)
		_, err := env.bulk.BulkSetTaskStatus(ctx, admin, models.PermissionScopeAll, []uint{already.ID, first.ID, second.ID}, models.TaskStatusInProgress)
		require.NoError(t, err)
		for _, task := range []*models.Task{already, first, second} {
			assert.Equal(t, models.TaskStatusInProgress, storedWebhookTask(t, db, task.ID).Status)
		}
		assert.Empty(t, env.deliveries("task.updated", already.ID), "a task already in that status")
		assertFanOut(t, env, "task.updated", first.ID, a.ID, c.ID)
		assertFanOut(t, env, "task.updated", second.ID, a.ID, c.ID)
		assert.Equal(t, int64(4), env.allDeliveries())
		assert.NotEqual(t, env.deliveries("task.updated", first.ID)[0].EventID, env.deliveries("task.updated", second.ID)[0].EventID,
			"one event per task")
	})

	t.Run("a refused bulk batch writes no row", func(t *testing.T) {
		env.clearDeliveries()
		completed := env.seedTask(admin, models.TaskStatusCompleted)
		open := env.seedTask(admin, models.TaskStatusPending)
		_, err := env.bulk.BulkSetTaskStatus(ctx, admin, models.PermissionScopeAll, []uint{completed.ID, open.ID}, models.TaskStatusInProgress)
		require.ErrorIs(t, err, apperrors.ErrCompletedTaskModify)
		assert.Equal(t, models.TaskStatusPending, storedWebhookTask(t, db, open.ID).Status)

		sales := &models.User{Email: env.uniqueEmail("sales"), Password: "x", FirstName: "Sa", LastName: "Les", Role: models.RoleSales, IsActive: true}
		require.NoError(t, db.Create(sales).Error)
		own := env.seedTask(sales.ID, models.TaskStatusPending)
		foreign := env.seedTask(admin, models.TaskStatusPending)
		_, err = env.bulk.BulkSetTaskStatus(ctx, sales.ID, models.PermissionScopeOwn, []uint{own.ID, foreign.ID}, models.TaskStatusInProgress)
		require.Error(t, err)
		assert.Equal(t, models.TaskStatusPending, storedWebhookTask(t, db, own.ID).Status)
		assert.Zero(t, env.allDeliveries(), "a refused batch writes nothing")
	})

	t.Run("Delete writes task.deleted for every subscribed endpoint", func(t *testing.T) {
		env.clearDeliveries()
		// b never received an event for the task; it gets the deleted one.
		task := env.seedTask(admin, models.TaskStatusPending)
		require.NoError(t, env.tasks.Delete(task.ID))
		assert.True(t, storedWebhookTask(t, db, task.ID).DeletedAt.Valid, "soft-deleted")
		assertFanOut(t, env, "task.deleted", task.ID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
		noRowFor(t, c.ID, d.ID)
	})

	t.Run("a Delete that loses the race answers not-found and writes no row", func(t *testing.T) {
		env.clearDeliveries()
		task := env.seedTask(admin, models.TaskStatusPending)
		svc := env.tasks.(*taskService)
		// Another delete commits between the pre-read and the transaction,
		// on its own connection (autocommit).
		svc.beforeTaskDeleteTx = func(id uint) {
			require.NoError(t, db.Delete(&models.Task{}, id).Error)
		}
		t.Cleanup(func() { svc.beforeTaskDeleteTx = nil })
		err := env.tasks.Delete(task.ID)
		require.Error(t, err)
		assert.True(t, apperrors.IsNotFound(err), "%v", err)
		assert.True(t, storedWebhookTask(t, db, task.ID).DeletedAt.Valid)
		assert.Zero(t, env.count(&models.WebhookDelivery{}, "event_type = ?", "task.deleted"), "no second task.deleted")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("the automation create_task step writes task.created", func(t *testing.T) {
		// The service writes above may have recorded automation events; drop
		// them so the dispatch starts only the run under test.
		require.NoError(t, db.Where("1 = 1").Delete(&models.AutomationEvent{}).Error)
		auto := newAutomationEnv(t, db)
		auto.rebuildWith(WithAutomationWebhooks(webhook.NewEmitter(env.repo)))
		rule := auto.leadRule(models.AutomationEventLeadCreated, taskStep(models.AutomationRecipientOwner, 0, 3))
		stepLead := auto.createLead(auto.sales, models.LeadStatusNew)

		env.clearDeliveries()
		tasksBefore := env.count(&models.Task{}, "")
		report, err := auto.svc.DryRun(rule.ID, models.AutomationEntityLead, stepLead.ID)
		require.NoError(t, err)
		require.Len(t, report.Steps, 1)
		assert.Equal(t, tasksBefore, env.count(&models.Task{}, ""), "a dry run writes no task")
		assert.Zero(t, env.allDeliveries(), "a dry run writes no row")

		run := auto.startEntityRun(rule, models.AutomationEntityLead, stepLead.ID)
		auto.executeDue()
		assert.Equal(t, models.AutomationRunCompleted, auto.run(run.ID).Status)
		steps := auto.stepsOf(run.ID)
		require.Len(t, steps, 1)
		assert.Equal(t, models.AutomationOutcomeSucceeded, steps[0].Outcome)
		var created []models.Task
		require.NoError(t, db.Where("lead_id = ?", stepLead.ID).Find(&created).Error)
		require.Len(t, created, 1)
		assertFanOut(t, env, "task.created", created[0].ID, a.ID, c.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
		noLeadRows(t)
	})

	t.Run("an unwired task service writes no row", func(t *testing.T) {
		env.clearDeliveries()
		plain := newWebhookEnv(t, db, nil)
		fresh := &models.Task{Title: "Unwired task", AssignedToID: plain.owner.ID}
		require.NoError(t, plain.tasks.CreateWithLabels(fresh, []uint{labelA.ID}, plain.owner.ID))
		changed := taskAsServiceReads(t, plain, fresh.ID)
		changed.Title = "Unwired, renamed"
		require.NoError(t, plain.tasks.UpdateWithLabels(changed, uintsPtr(), false, false, plain.owner.ID))
		_, err := plain.bulk.BulkSetTaskStatus(ctx, plain.owner.ID, models.PermissionScopeAll, []uint{fresh.ID}, models.TaskStatusInProgress)
		require.NoError(t, err)
		require.NoError(t, plain.tasks.Delete(fresh.ID))
		assert.True(t, storedWebhookTask(t, db, fresh.ID).DeletedAt.Valid)
		assert.Zero(t, env.allDeliveries(), "no outbox row without the option")
	})
}

// runTaskWebhookAtomicity: an outbox insert that fails rolls the task write
// back with it.
func runTaskWebhookAtomicity(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	all := webhook.AllEventTypes()
	env.endpoint("https://example.com/hook", true, all...)
	env.endpoint("https://example.com/hook/second", true, all...)
	ctx := context.Background()
	admin := env.owner.ID
	labelA, labelB := env.seedLabel(), env.seedLabel()
	env.wire(newFailingWebhookRepository(env.repo, injectedFailure, 0))
	joinRows := func() int64 {
		var n int64
		require.NoError(t, db.Table("task_labels").Count(&n).Error)
		return n
	}

	t.Run("Create", func(t *testing.T) {
		tasks := env.count(&models.Task{}, "")
		links := joinRows()
		task := &models.Task{Title: "Lost task", AssignedToID: admin}
		require.ErrorIs(t, env.tasks.CreateWithLabels(task, []uint{labelA.ID, labelB.ID}, admin), injectedFailure)
		assert.Zero(t, task.ID, "the id is reset")
		assert.Equal(t, tasks, env.count(&models.Task{}, ""), "no task row")
		assert.Equal(t, links, joinRows(), "no task_labels row")
		assert.Zero(t, env.allDeliveries(), "every delivery row rolled back")
	})

	t.Run("Update", func(t *testing.T) {
		task := env.seedTask(admin, models.TaskStatusPending, *labelA)
		before := storedWebhookTask(t, db, task.ID)
		changed := taskAsServiceReads(t, env, task.ID)
		changed.Title = "Lost title"
		changed.Status = models.TaskStatusInProgress
		require.ErrorIs(t, env.tasks.UpdateWithLabels(changed, uintsPtr(labelB.ID), false, false, admin), injectedFailure)
		after := storedWebhookTask(t, db, task.ID)
		assert.Equal(t, before.WebhookState(), after.WebhookState(), "the task rolled back")
		assert.Equal(t, []uint{labelA.ID}, storedTaskLabelIDs(t, db, task.ID), "the label set rolled back")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("BulkSetTaskStatus", func(t *testing.T) {
		first, second := env.seedTask(admin, models.TaskStatusPending), env.seedTask(admin, models.TaskStatusPending)
		_, err := env.bulk.BulkSetTaskStatus(ctx, admin, models.PermissionScopeAll, []uint{first.ID, second.ID}, models.TaskStatusInProgress)
		require.ErrorIs(t, err, injectedFailure)
		assert.Equal(t, models.TaskStatusPending, storedWebhookTask(t, db, first.ID).Status)
		assert.Equal(t, models.TaskStatusPending, storedWebhookTask(t, db, second.ID).Status)
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("Delete", func(t *testing.T) {
		task := env.seedTask(admin, models.TaskStatusPending)
		require.ErrorIs(t, env.tasks.Delete(task.ID), injectedFailure)
		assert.False(t, storedWebhookTask(t, db, task.ID).DeletedAt.Valid, "the task stays live")
		assert.Zero(t, env.allDeliveries())
	})
}
