package service

// The write half of the task-to-deal link (#240), through the real service and
// task repository on a file-backed SQLite database. Every assertion on a link
// reads the committed row: the struct the service was handed proves nothing,
// because GORM writes a preloaded belongs-to id back over the foreign key.

import (
	"errors"
	"fmt"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// taskDealFixture is the automation fixture (admin, sales owner, a second sales
// user) plus the two roles that see no deal and the service under test.
type taskDealFixture struct {
	*automationEnv
	support  *models.User
	customer *models.User
	service  TaskService
}

func newTaskDealFixture(t *testing.T) *taskDealFixture {
	t.Helper()
	env := newAutomationEnv(t, openAutomationSQLite(t))
	return &taskDealFixture{
		automationEnv: env,
		support:       env.createUser("support", models.RoleSupport, true),
		customer:      env.createUser("customer", models.RoleCustomer, true),
		service:       NewTaskService(env.tasks, env.users, env.leads, env.customers, repository.NewLabelRepository(env.db), env.deals),
	}
}

func (f *taskDealFixture) deal(owner *models.User, stage models.DealStage) *models.Deal {
	f.t.Helper()
	return f.createDeal(owner, stage, 100, "EUR", nil, nil)
}

// linkedTask inserts a task directly, so no rule of the service decides the
// fixture. A nil dealID is an unlinked task.
func (f *taskDealFixture) linkedTask(assignee *models.User, dealID *uint) *models.Task {
	f.t.Helper()
	task := &models.Task{Title: "Task", Status: models.TaskStatusPending, Priority: models.TaskPriorityMedium, AssignedToID: assignee.ID, DealID: dealID}
	require.NoError(f.t, f.db.Create(task).Error)
	return task
}

// committedDealID is the stored link, read outside anything the service holds.
func (f *taskDealFixture) committedDealID(taskID uint) *uint {
	f.t.Helper()
	var stored models.Task
	require.NoError(f.t, f.db.First(&stored, taskID).Error)
	return stored.DealID
}

// loaded reads the task the way the handler does: every association preloaded.
func (f *taskDealFixture) loaded(taskID uint) *models.Task {
	f.t.Helper()
	task, err := f.tasks.GetByID(taskID)
	require.NoError(f.t, err)
	return task
}

func (f *taskDealFixture) assertCommitted(taskID uint, want *uint, msg string) {
	f.t.Helper()
	got := f.committedDealID(taskID)
	if want == nil {
		assert.Nil(f.t, got, msg)
		return
	}
	require.NotNil(f.t, got, msg)
	assert.Equal(f.t, *want, *got, msg)
}

// outageDealRepo is a deal repository whose lookup fails for a reason other than
// a missing row.
type outageDealRepo struct {
	repository.DealRepository
	err error
}

func (r outageDealRepo) GetByID(uint) (*models.Deal, error) { return nil, r.err }

func uintRef(value uint) *uint { return &value }

// TestTaskDealLinkWriteRules is the rule of a deal_id on create and update.
func TestTaskDealLinkWriteRules(t *testing.T) {
	f := newTaskDealFixture(t)
	open := f.deal(f.sales, models.DealStageProposal)
	won := f.deal(f.sales, models.DealStageWon)
	lost := f.deal(f.sales, models.DealStageLost)
	gone := f.deal(f.sales, models.DealStageProposal)
	require.NoError(t, f.db.Delete(&models.Deal{}, gone.ID).Error)

	refused := []struct {
		name   string
		actor  *models.User
		dealID uint
	}{
		{"support", f.support, open.ID},
		{"customer", f.customer, open.ID},
		{"another owner's deal", f.other, open.ID},
		{"a missing deal", f.admin, 999999},
		{"a soft-deleted deal", f.admin, gone.ID},
	}

	t.Run("create accepts a deal the actor may see, closed deals included", func(t *testing.T) {
		for _, tc := range []struct {
			actor *models.User
			deal  *models.Deal
		}{{f.admin, open}, {f.admin, won}, {f.sales, open}, {f.sales, won}, {f.sales, lost}} {
			task := &models.Task{Title: "New", AssignedToID: tc.actor.ID, DealID: uintRef(tc.deal.ID)}
			require.NoError(t, f.service.CreateWithLabels(task, nil, tc.actor.ID), "%s on deal %d", tc.actor.Role, tc.deal.ID)
			f.assertCommitted(task.ID, &tc.deal.ID, "the link is stored")
			require.NotNil(t, task.Deal, "the checked deal is attached for the response")
			assert.Equal(t, tc.deal.ID, task.Deal.ID)
		}
	})

	t.Run("create refuses everything else with one message shape and writes nothing", func(t *testing.T) {
		for _, tc := range refused {
			before := f.count(&models.Task{})
			task := &models.Task{Title: "Refused", AssignedToID: tc.actor.ID, DealID: uintRef(tc.dealID)}
			err := f.service.CreateWithLabels(task, nil, tc.actor.ID)
			require.ErrorIs(t, err, apperrors.ErrDealNotFound, tc.name)
			assert.Equal(t, fmt.Sprintf("unknown deal_id %d: deal not found", tc.dealID), err.Error(), tc.name)
			assert.Equal(t, before, f.count(&models.Task{}), tc.name)
		}
	})

	t.Run("create without an actor cannot link a deal", func(t *testing.T) {
		err := f.service.Create(&models.Task{Title: "No actor", AssignedToID: f.admin.ID, DealID: uintRef(open.ID)})
		assert.ErrorIs(t, err, apperrors.ErrDealNotFound)
	})

	t.Run("update refuses everything else with one message shape and writes nothing", func(t *testing.T) {
		for _, tc := range refused {
			stored := f.linkedTask(tc.actor, nil)
			task := f.loaded(stored.ID)
			task.Title = "Must not persist"
			task.DealID = uintRef(tc.dealID)
			err := f.service.UpdateWithLabels(task, nil, false, true, tc.actor.ID)
			require.ErrorIs(t, err, apperrors.ErrDealNotFound, tc.name)
			assert.Equal(t, fmt.Sprintf("unknown deal_id %d: deal not found", tc.dealID), err.Error(), tc.name)
			reread := f.loaded(stored.ID)
			assert.Equal(t, "Task", reread.Title, tc.name)
			assert.Nil(t, reread.DealID, tc.name)
		}
	})

	t.Run("a lookup that fails for another reason returns that error", func(t *testing.T) {
		outage := errors.New("connection refused")
		broken := NewTaskService(f.tasks, f.users, f.leads, f.customers, repository.NewLabelRepository(f.db), outageDealRepo{err: outage})
		before := f.count(&models.Task{})

		err := broken.CreateWithLabels(&models.Task{Title: "Outage", AssignedToID: f.admin.ID, DealID: uintRef(open.ID)}, nil, f.admin.ID)
		require.ErrorIs(t, err, outage)
		assert.NotErrorIs(t, err, apperrors.ErrDealNotFound)
		assert.Equal(t, before, f.count(&models.Task{}))

		stored := f.linkedTask(f.admin, nil)
		task := f.loaded(stored.ID)
		task.DealID = uintRef(open.ID)
		err = broken.UpdateWithLabels(task, nil, false, true, f.admin.ID)
		require.ErrorIs(t, err, outage)
		assert.NotErrorIs(t, err, apperrors.ErrDealNotFound)
		f.assertCommitted(stored.ID, nil, "nothing written")
	})

	t.Run("a sent deal_id is checked even when it equals the stored link", func(t *testing.T) {
		hidden := f.deal(f.other, models.DealStageProposal)
		stored := f.linkedTask(f.sales, &hidden.ID)

		var answers []string
		for _, sent := range []uint{hidden.ID, 999999} {
			task := f.loaded(stored.ID)
			task.DealID = uintRef(sent)
			err := f.service.UpdateWithLabels(task, nil, false, true, f.sales.ID)
			require.ErrorIs(t, err, apperrors.ErrDealNotFound)
			answers = append(answers, fmt.Sprintf("unknown deal_id %d: deal not found", sent))
			assert.Equal(t, answers[len(answers)-1], err.Error(), "the stored id answers like any other hidden id")
		}
		f.assertCommitted(stored.ID, &hidden.ID, "the refused writes changed nothing")

		mine := f.linkedTask(f.sales, &open.ID)
		task := f.loaded(mine.ID)
		task.DealID = uintRef(open.ID)
		require.NoError(t, f.service.UpdateWithLabels(task, nil, false, true, f.sales.ID), "the owner may send its own link back")
	})

	t.Run("an absent deal_id keeps the stored link unchecked", func(t *testing.T) {
		hidden := f.deal(f.other, models.DealStageProposal)
		stored := f.linkedTask(f.sales, &hidden.ID)

		task := f.loaded(stored.ID)
		task.Title = "Renamed by the assignee"
		require.NoError(t, f.service.UpdateWithLabels(task, nil, false, false, f.sales.ID))
		f.assertCommitted(stored.ID, &hidden.ID, "an edit that never named deal_id keeps the link")
		assert.Equal(t, "Renamed by the assignee", f.loaded(stored.ID).Title)

		// A struct built without the link, not read back first, keeps it too.
		bare := &models.Task{BaseModel: models.BaseModel{ID: stored.ID}, Title: "Bare", Status: models.TaskStatusPending, Priority: models.TaskPriorityMedium, AssignedToID: f.sales.ID}
		require.NoError(t, f.service.UpdateWithLabels(bare, nil, false, false, f.sales.ID))
		f.assertCommitted(stored.ID, &hidden.ID, "absence is not a clear")
	})

	t.Run("a changed id without the flag is still checked", func(t *testing.T) {
		hidden := f.deal(f.other, models.DealStageProposal)
		stored := f.linkedTask(f.sales, nil)
		task := f.loaded(stored.ID)
		task.DealID = uintRef(hidden.ID)
		require.ErrorIs(t, f.service.Update(task), apperrors.ErrDealNotFound)
		f.assertCommitted(stored.ID, nil, "Update has no actor and cannot link a deal")
	})

	t.Run("a deal link coexists with a lead or a customer and the conflict is unchanged", func(t *testing.T) {
		customer := f.createCustomer()
		lead := f.createLead(f.sales, models.LeadStatusNew)

		withCustomer := &models.Task{Title: "With customer", AssignedToID: f.sales.ID, DealID: uintRef(open.ID), CustomerID: &customer.ID}
		require.NoError(t, f.service.CreateWithLabels(withCustomer, nil, f.sales.ID))
		f.assertCommitted(withCustomer.ID, &open.ID, "beside a customer")

		withLead := &models.Task{Title: "With lead", AssignedToID: f.sales.ID, DealID: uintRef(open.ID), LeadID: &lead.ID}
		require.NoError(t, f.service.CreateWithLabels(withLead, nil, f.sales.ID))
		f.assertCommitted(withLead.ID, &open.ID, "beside a lead")

		conflict := &models.Task{Title: "Both", AssignedToID: f.sales.ID, DealID: uintRef(open.ID), LeadID: &lead.ID, CustomerID: &customer.ID}
		assert.ErrorIs(t, f.service.CreateWithLabels(conflict, nil, f.sales.ID), apperrors.ErrTaskLeadCustomerConflict)

		task := f.loaded(withLead.ID)
		task.CustomerID = &customer.ID
		assert.ErrorIs(t, f.service.UpdateWithLabels(task, nil, false, false, f.sales.ID), apperrors.ErrTaskLeadCustomerConflict)
	})

	t.Run("a changed link is committed although the previous deal was preloaded", func(t *testing.T) {
		next := f.deal(f.sales, models.DealStageNegotiation)
		stored := f.linkedTask(f.sales, &open.ID)

		task := f.loaded(stored.ID)
		require.NotNil(t, task.Deal, "the previous deal is preloaded")
		task.DealID = uintRef(next.ID)
		require.NoError(t, f.service.UpdateWithLabels(task, nil, false, true, f.sales.ID))
		f.assertCommitted(stored.ID, &next.ID, "GORM must not write the preloaded id back")
		require.NotNil(t, task.Deal, "the new deal is attached for the response")
		assert.Equal(t, next.ID, task.Deal.ID)

		// The same through the path that replaces the labels.
		empty := []uint{}
		task = f.loaded(stored.ID)
		task.DealID = uintRef(open.ID)
		require.NoError(t, f.service.UpdateWithLabels(task, &empty, false, true, f.sales.ID))
		f.assertCommitted(stored.ID, &open.ID, "through UpdateWithLabels of the repository")
	})
}

// TestTaskDealLinkClear is deal_id 0: it commits NULL when the actor may see the
// stored deal, and returns no error and changes nothing when it may not.
func TestTaskDealLinkClear(t *testing.T) {
	f := newTaskDealFixture(t)
	mine := f.deal(f.sales, models.DealStageProposal)
	hidden := f.deal(f.other, models.DealStageProposal)

	// clear runs what the handler runs for a body of {"deal_id":0}.
	clear := func(t *testing.T, taskID uint, actor *models.User) *models.Task {
		t.Helper()
		task := f.loaded(taskID)
		task.DealID = nil
		require.NoError(t, f.service.UpdateWithLabels(task, nil, false, true, actor.ID))
		return task
	}

	t.Run("the sales owner clears its own link although the deal was preloaded", func(t *testing.T) {
		stored := f.linkedTask(f.sales, &mine.ID)
		require.NotNil(t, f.loaded(stored.ID).Deal)
		task := clear(t, stored.ID, f.sales)
		f.assertCommitted(stored.ID, nil, "a NULL is written")
		assert.Nil(t, task.DealID)
		assert.Nil(t, task.Deal)
	})

	t.Run("a pointer to zero is the same clear", func(t *testing.T) {
		stored := f.linkedTask(f.sales, &mine.ID)
		task := f.loaded(stored.ID)
		task.DealID = uintRef(0)
		require.NoError(t, f.service.UpdateWithLabels(task, nil, false, true, f.sales.ID))
		f.assertCommitted(stored.ID, nil, "0 means no link")
	})

	t.Run("an admin clears any link", func(t *testing.T) {
		stored := f.linkedTask(f.sales, &hidden.ID)
		clear(t, stored.ID, f.admin)
		f.assertCommitted(stored.ID, nil, "an admin may see every deal")
	})

	t.Run("an admin clears the link of a deleted deal", func(t *testing.T) {
		deleted := f.deal(f.sales, models.DealStageProposal)
		stored := f.linkedTask(f.sales, &deleted.ID)
		require.NoError(t, f.db.Delete(&models.Deal{}, deleted.ID).Error)
		clear(t, stored.ID, f.admin)
		f.assertCommitted(stored.ID, nil, "the id of a deleted deal is an admin's to clear")
	})

	t.Run("a clear of a hidden link returns no error and changes nothing", func(t *testing.T) {
		for _, actor := range []*models.User{f.sales, f.support, f.customer} {
			stored := f.linkedTask(actor, &hidden.ID)
			clear(t, stored.ID, actor)
			f.assertCommitted(stored.ID, &hidden.ID, string(actor.Role))
		}
	})

	t.Run("nor can sales clear the link of a deleted deal it owned", func(t *testing.T) {
		deleted := f.deal(f.sales, models.DealStageProposal)
		stored := f.linkedTask(f.sales, &deleted.ID)
		require.NoError(t, f.db.Delete(&models.Deal{}, deleted.ID).Error)
		clear(t, stored.ID, f.sales)
		f.assertCommitted(stored.ID, &deleted.ID, "only an admin may see a deal that is gone")
	})

	t.Run("a clear keeps the rest of the edit", func(t *testing.T) {
		stored := f.linkedTask(f.sales, &hidden.ID)
		task := f.loaded(stored.ID)
		task.Title = "Renamed"
		task.DealID = nil
		require.NoError(t, f.service.UpdateWithLabels(task, nil, false, true, f.sales.ID))
		assert.Equal(t, "Renamed", f.loaded(stored.ID).Title)
		f.assertCommitted(stored.ID, &hidden.ID, "the hidden link stays")
	})

	t.Run("a deal the actor may see replaces even a hidden link", func(t *testing.T) {
		stored := f.linkedTask(f.sales, &hidden.ID)
		task := f.loaded(stored.ID)
		task.DealID = uintRef(mine.ID)
		require.NoError(t, f.service.UpdateWithLabels(task, nil, false, true, f.sales.ID))
		f.assertCommitted(stored.ID, &mine.ID, "the new id is committed")
	})

	t.Run("a clear of an unlinked task is a no-op", func(t *testing.T) {
		stored := f.linkedTask(f.sales, nil)
		clear(t, stored.ID, f.sales)
		f.assertCommitted(stored.ID, nil, "still unlinked")
	})

	t.Run("creating with deal_id 0 gives an unlinked task", func(t *testing.T) {
		task := &models.Task{Title: "Zero", AssignedToID: f.sales.ID, DealID: uintRef(0)}
		require.NoError(t, f.service.CreateWithLabels(task, nil, f.sales.ID))
		f.assertCommitted(task.ID, nil, "0 means no link")
		assert.Nil(t, task.DealID)
		assert.Nil(t, task.Deal)
	})
}

// TestTaskDealLogsOmitDealTitle holds the log lines of the link to ids: neither
// the task service nor a deal run's create_task step may log the deal's title
// or notes.
func TestTaskDealLogsOmitDealTitle(t *testing.T) {
	const (
		title = "deal-title-canary"
		notes = "deal-notes-canary"
	)
	output, hook := capturePrivacyLogs(t)
	f := newTaskDealFixture(t)
	newDeal := func(owner *models.User) *models.Deal {
		deal := &models.Deal{Title: title, Notes: notes, Stage: models.DealStageProposal, Currency: "EUR", Probability: 40, OwnerID: owner.ID}
		require.NoError(t, f.db.Create(deal).Error)
		return deal
	}
	deal := newDeal(f.sales)
	hidden := newDeal(f.other)

	findEntry := func(message string, dealID uint) bool {
		for _, entry := range hook.AllEntries() {
			if entry.Message == message && entry.Data["deal_id"] == dealID {
				return true
			}
		}
		return false
	}

	created := &models.Task{Title: "Accepted", AssignedToID: f.sales.ID, DealID: uintRef(deal.ID)}
	require.NoError(t, f.service.CreateWithLabels(created, nil, f.sales.ID))
	assert.True(t, findEntry("Deal link accepted", deal.ID), "an accepted link logs its id")

	refused := &models.Task{Title: "Refused", AssignedToID: f.sales.ID, DealID: uintRef(hidden.ID)}
	require.ErrorIs(t, f.service.CreateWithLabels(refused, nil, f.sales.ID), apperrors.ErrDealNotFound)
	assert.True(t, findEntry("Deal link rejected", hidden.ID), "a refused link logs its id")

	onHidden := f.linkedTask(f.sales, &hidden.ID)
	task := f.loaded(onHidden.ID)
	task.DealID = nil
	require.NoError(t, f.service.UpdateWithLabels(task, nil, false, true, f.sales.ID))
	assert.True(t, findEntry("Deal link kept: hidden from the actor", hidden.ID))

	task = f.loaded(created.ID)
	task.DealID = nil
	require.NoError(t, f.service.UpdateWithLabels(task, nil, false, true, f.sales.ID))
	assert.True(t, findEntry("Deal link cleared", deal.ID))

	broken := NewTaskService(f.tasks, f.users, f.leads, f.customers, repository.NewLabelRepository(f.db), outageDealRepo{err: errors.New("connection refused")})
	require.Error(t, broken.CreateWithLabels(&models.Task{Title: "Outage", AssignedToID: f.sales.ID, DealID: uintRef(deal.ID)}, nil, f.sales.ID))
	assert.True(t, findEntry("Failed to check deal link", deal.ID))

	// A deal run's create_task step, whose task title carries the deal's title
	// on purpose: that text belongs in the task, not in a log line.
	rule := f.createRule(models.AutomationEntityDeal, models.AutomationEventDealCreated, models.AutomationTrigger{},
		dealTaskStep(models.AutomationRecipientOwner, 0, 1))
	f.recordEvent(models.AutomationEntityDeal, deal.ID, models.AutomationEventDealCreated, "", "proposal", models.AutomationOriginUser)
	f.dispatch()
	f.executeDue()
	_, err := f.svc.DryRun(rule.ID, models.AutomationEntityDeal, deal.ID)
	require.NoError(t, err)
	runs := f.runsOf(rule.ID)
	require.Len(t, runs, 1)
	require.Equal(t, models.AutomationRunCompleted, runs[0].Status)
	steps := f.stepsOf(runs[0].ID)
	require.Len(t, steps, 1)
	assert.EqualValues(t, deal.ID, steps[0].Details["deal_id"])

	var automated models.Task
	require.NoError(t, f.db.Where("title LIKE ?", "%"+title+"%").First(&automated).Error, "the task itself renders the title")

	logs := output.String()
	assert.NotEmpty(t, logs)
	assert.NotContains(t, logs, title)
	assert.NotContains(t, logs, notes)
}
