package service

// #558: the bulk task status batch and the single task update lock the same
// task row. On a disposable TEST_MYSQL_DSN schema each subtest parks the
// first party right after its locking read (the afterTaskLock seams), sees
// the second party wait on that row in information_schema.PROCESSLIST on two
// consecutive polls, releases the first and checks that the second acted on
// what the first committed. The SQLite root runs the same three cases one
// after the other, plus a label update through the locked write.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// taskLockingRead is a locking read of the tasks table as PROCESSLIST shows
// it: GetTasksByIDs and GetByIDForUpdate alike.
var taskLockingRead = regexp.MustCompile("(?is)^SELECT .*FROM `tasks`.*FOR UPDATE")

type bulkTaskLockEnv struct {
	db      *gorm.DB
	bulk    *bulkStatusService
	tasks   *taskService
	admin   *models.User
	salesA  *models.User
	salesB  *models.User
	labelID uint
}

func newBulkTaskLockEnv(t *testing.T, db *gorm.DB) *bulkTaskLockEnv {
	t.Helper()
	quiet := logrus.New()
	quiet.SetLevel(logrus.PanicLevel)
	txManager := utils.NewTransactionManager(db)
	env := &bulkTaskLockEnv{db: db}
	env.bulk = NewBulkStatusService(repository.NewBulkOperationRepository(db), repository.NewBulkStatusRepository(db),
		txManager, quiet).(*bulkStatusService)
	env.tasks = NewTaskService(repository.NewTaskRepository(db), repository.NewUserRepository(db), repository.NewLeadRepository(db),
		repository.NewCustomerRepository(db), repository.NewLabelRepository(db), repository.NewDealRepository(db),
		WithTaskRowLock(txManager)).(*taskService)
	seq := automationTestSeq.Add(1)
	user := func(name string, role models.UserRole) *models.User {
		u := &models.User{Email: fmt.Sprintf("%s-%d@example.test", name, seq), Password: "x", FirstName: name, LastName: "Test", Role: role, IsActive: true}
		require.NoError(t, db.Create(u).Error)
		return u
	}
	env.admin = user("admin", models.RoleAdmin)
	env.salesA = user("sales-a", models.RoleSales)
	env.salesB = user("sales-b", models.RoleSales)
	label := &models.Label{Name: fmt.Sprintf("lock-%d", seq), Color: "#112233"}
	require.NoError(t, db.Create(label).Error)
	env.labelID = label.ID
	return env
}

func (e *bulkTaskLockEnv) task(t *testing.T, assignee *models.User) *models.Task {
	t.Helper()
	task := &models.Task{Title: "Lock test", Status: models.TaskStatusPending, Priority: models.TaskPriorityMedium, AssignedToID: assignee.ID}
	require.NoError(t, e.db.Create(task).Error)
	return task
}

func (e *bulkTaskLockEnv) stored(t *testing.T, id uint) models.Task {
	t.Helper()
	var task models.Task
	require.NoError(t, e.db.First(&task, id).Error)
	return task
}

func (e *bulkTaskLockEnv) lastOperationStatus(t *testing.T) models.BulkOperationStatus {
	t.Helper()
	var op models.BulkOperation
	require.NoError(t, e.db.Order("id DESC").First(&op).Error)
	return op.Status
}

// update does what TaskHandler.Update does: read, change fields, save as admin.
func (e *bulkTaskLockEnv) update(id uint, change func(*models.Task)) error {
	task, err := e.tasks.taskRepo.GetByID(id)
	if err != nil {
		return err
	}
	change(task)
	return e.tasks.UpdateWithLabels(task, nil, false, false, e.admin.ID)
}

func (e *bulkTaskLockEnv) bulkSet(actor *models.User, id uint, status models.TaskStatus) error {
	_, err := e.bulk.BulkSetTaskStatus(context.Background(), actor.ID, permissions.ForUser(actor).Scope(permissions.TasksUpdate), []uint{id}, status)
	return err
}

func appErrorDetail(t *testing.T, err error, key string) interface{} {
	t.Helper()
	var appErr *apperrors.AppError
	require.True(t, errors.As(err, &appErr), "an AppError: %v", err)
	return appErr.Details[key]
}

// bulkTaskLockCase is one ordering: first runs parked after its locking read
// (MySQL) or simply first (SQLite); second runs after it.
type bulkTaskLockCase struct {
	name        string
	bulkFirst   bool
	assignee    func(e *bulkTaskLockEnv) *models.User
	runBulk     func(e *bulkTaskLockEnv, id uint) error
	runUpdate   func(e *bulkTaskLockEnv, id uint) error
	checkResult func(t *testing.T, e *bulkTaskLockEnv, id uint, bulkErr, updateErr error)
}

func bulkTaskLockCases() []bulkTaskLockCase {
	return []bulkTaskLockCase{
		{
			name:      "bulk_first",
			bulkFirst: true,
			assignee:  func(e *bulkTaskLockEnv) *models.User { return e.salesA },
			runBulk: func(e *bulkTaskLockEnv, id uint) error {
				return e.bulkSet(e.admin, id, models.TaskStatusCancelled)
			},
			runUpdate: func(e *bulkTaskLockEnv, id uint) error {
				return e.update(id, func(task *models.Task) { task.Status = models.TaskStatusCompleted })
			},
			checkResult: func(t *testing.T, e *bulkTaskLockEnv, id uint, bulkErr, updateErr error) {
				require.NoError(t, bulkErr)
				require.NoError(t, updateErr)
				assert.Equal(t, models.TaskStatusCompleted, e.stored(t, id).Status)
				assert.Equal(t, models.StatusCompleted, e.lastOperationStatus(t))
			},
		},
		{
			name:     "update_first",
			assignee: func(e *bulkTaskLockEnv) *models.User { return e.salesA },
			runBulk: func(e *bulkTaskLockEnv, id uint) error {
				return e.bulkSet(e.admin, id, models.TaskStatusCancelled)
			},
			runUpdate: func(e *bulkTaskLockEnv, id uint) error {
				return e.update(id, func(task *models.Task) { task.Status = models.TaskStatusCompleted })
			},
			checkResult: func(t *testing.T, e *bulkTaskLockEnv, id uint, bulkErr, updateErr error) {
				require.NoError(t, updateErr)
				require.Error(t, bulkErr)
				assert.True(t, errors.Is(bulkErr, apperrors.ErrCompletedTaskModify), "completed-task refusal: %v", bulkErr)
				assert.Equal(t, []uint{id}, appErrorDetail(t, bulkErr, "completed_ids"))
				assert.Equal(t, models.TaskStatusCompleted, e.stored(t, id).Status)
				assert.Equal(t, models.StatusFailed, e.lastOperationStatus(t))
			},
		},
		{
			name:     "reassign_first",
			assignee: func(e *bulkTaskLockEnv) *models.User { return e.salesA },
			runBulk: func(e *bulkTaskLockEnv, id uint) error {
				return e.bulkSet(e.salesA, id, models.TaskStatusInProgress)
			},
			runUpdate: func(e *bulkTaskLockEnv, id uint) error {
				return e.update(id, func(task *models.Task) { task.AssignedToID = e.salesB.ID })
			},
			checkResult: func(t *testing.T, e *bulkTaskLockEnv, id uint, bulkErr, updateErr error) {
				require.NoError(t, updateErr)
				require.Error(t, bulkErr)
				assert.True(t, errors.Is(bulkErr, apperrors.ErrForbidden), "forbidden: %v", bulkErr)
				assert.Equal(t, []uint{id}, appErrorDetail(t, bulkErr, "forbidden_ids"))
				stored := e.stored(t, id)
				assert.Equal(t, e.salesB.ID, stored.AssignedToID)
				assert.Equal(t, models.TaskStatusPending, stored.Status)
			},
		},
	}
}

func TestBulkTaskStatusLockMySQL(t *testing.T) {
	for _, tc := range bulkTaskLockCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			db := openAutomationMySQL(t, 6)
			env := newBulkTaskLockEnv(t, db)
			task := env.task(t, tc.assignee(env))

			gate := newParkGate()
			bulkDone := make(chan error, 1)
			updateDone := make(chan error, 1)
			if tc.bulkFirst {
				env.bulk.afterTaskLock = func([]uint) { gate.park() }
				go func() { bulkDone <- tc.runBulk(env, task.ID) }()
				gate.awaitReached(t, "bulk")
				go func() { updateDone <- tc.runUpdate(env, task.ID) }()
			} else {
				env.tasks.afterTaskLock = func(uint) { gate.park() }
				go func() { updateDone <- tc.runUpdate(env, task.ID) }()
				gate.awaitReached(t, "update")
				go func() { bulkDone <- tc.runBulk(env, task.ID) }()
			}
			// The release runs even when the wait is never seen, so the
			// parked party finishes before the schema is dropped.
			released := false
			release := func() {
				if !released {
					released = true
					close(gate.release)
				}
			}
			t.Cleanup(release)
			awaitLeadCompanyLockWait(t, db, taskLockingRead)
			release()

			bulkErr := awaitDone(t, "bulk", bulkDone)
			updateErr := awaitDone(t, "update", updateDone)
			assertNotDeadlock(t, bulkErr)
			assertNotDeadlock(t, updateErr)
			tc.checkResult(t, env, task.ID, bulkErr, updateErr)
		})
	}
}

func TestBulkTaskStatusLockSQLite(t *testing.T) {
	for _, tc := range bulkTaskLockCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := newBulkTaskLockEnv(t, openAutomationSQLite(t))
			task := env.task(t, tc.assignee(env))
			var bulkErr, updateErr error
			if tc.bulkFirst {
				bulkErr = tc.runBulk(env, task.ID)
				updateErr = tc.runUpdate(env, task.ID)
			} else {
				updateErr = tc.runUpdate(env, task.ID)
				bulkErr = tc.runBulk(env, task.ID)
			}
			tc.checkResult(t, env, task.ID, bulkErr, updateErr)
		})
	}

	t.Run("labels_through_the_locked_write", func(t *testing.T) {
		env := newBulkTaskLockEnv(t, openAutomationSQLite(t))
		task := env.task(t, env.salesA)
		var seamID uint
		env.tasks.afterTaskLock = func(id uint) { seamID = id }

		current, err := env.tasks.taskRepo.GetByID(task.ID)
		require.NoError(t, err)
		current.Status = models.TaskStatusInProgress
		labels := []uint{env.labelID}
		require.NoError(t, env.tasks.UpdateWithLabels(current, &labels, false, false, env.admin.ID))
		assert.Equal(t, task.ID, seamID, "the write went through the locking read")

		stored, err := env.tasks.taskRepo.GetByID(task.ID)
		require.NoError(t, err)
		assert.Equal(t, models.TaskStatusInProgress, stored.Status)
		require.Len(t, stored.Labels, 1)
		assert.Equal(t, env.labelID, stored.Labels[0].ID)

		// A completed task stays completed through the locked write too.
		stored.Status = models.TaskStatusCompleted
		require.NoError(t, env.tasks.UpdateWithLabels(stored, nil, false, false, env.admin.ID))
		reopened, err := env.tasks.taskRepo.GetByID(task.ID)
		require.NoError(t, err)
		reopened.Status = models.TaskStatusPending
		err = env.tasks.UpdateWithLabels(reopened, &labels, false, false, env.admin.ID)
		assert.True(t, errors.Is(err, apperrors.ErrCompletedTaskModify), "completed-task refusal: %v", err)
		assert.Equal(t, models.TaskStatusCompleted, env.stored(t, task.ID).Status)
	})
}
