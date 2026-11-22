package repository

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #427: ListByFilter and CountByFilter share whereTaskListFilter. These roots
// seed tasks across every status and priority, two assignees and two labels, a
// search token reached through the title on some rows and only through the
// description on one row of another status and assignee, tied titles and one
// soft-deleted task matching every filter, then check the rows and the count
// of every filter. The description-only row proves that a search match
// through either alternative is still bound by the status, priority and
// assignee predicates (the OR stays grouped; GORM also groups a Where string
// holding OR when it is combined with other conditions). The roots run on a file-backed SQLite
// database with one connection and, when TEST_MYSQL_DSN is set, on a
// disposable MySQL schema; the MySQL root skips without the variable and fails
// when it is unreachable.

func TestTaskListFilterRepositorySQLite(t *testing.T) {
	runTaskListFilterRepository(t, testsupport.Open(t, testsupport.SQLite, "task_list_filter", taskListFilterDB))
}

func TestTaskListFilterRepositoryMySQL(t *testing.T) {
	runTaskListFilterRepository(t, testsupport.Open(t, testsupport.MySQL, "task_list_filter", taskListFilterDB))
}

// taskListFilterDB is a file-backed SQLite database with one connection, or a uniquely
// named disposable schema on the TEST_MYSQL_DSN server.
var taskListFilterDB = testsupport.Group(
	testsupport.OnSQLite(testsupport.File("task_list_filter.db", testsupport.BusyTimeout)),
	testsupport.Models(
		&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{}, &models.Deal{}, &models.DealStageChange{},
		&models.Label{}, &models.Task{},
	),
)

const taskFilterToken = "tok427"

func runTaskListFilterRepository(t *testing.T, db *gorm.DB) {
	user1 := &models.User{Email: "user1@task-filter.test", Password: "x", FirstName: "One", LastName: "User", Role: models.RoleSales, IsActive: true}
	user2 := &models.User{Email: "user2@task-filter.test", Password: "x", FirstName: "Two", LastName: "User", Role: models.RoleSupport, IsActive: true}
	require.NoError(t, db.Create(user1).Error)
	require.NoError(t, db.Create(user2).Error)
	label1 := &models.Label{Name: "Filter one", Color: "#112233"}
	label2 := &models.Label{Name: "Filter two", Color: "#445566"}
	require.NoError(t, db.Create(label1).Error)
	require.NoError(t, db.Create(label2).Error)

	seed := func(title, description string, status models.TaskStatus, priority models.TaskPriority, assignee *models.User, labels ...*models.Label) uint {
		t.Helper()
		task := &models.Task{Title: title, Description: description, Status: status, Priority: priority, AssignedToID: assignee.ID}
		require.NoError(t, db.Create(task).Error)
		for _, l := range labels {
			require.NoError(t, db.Model(task).Association("Labels").Append(l))
		}
		return task.ID
	}
	// t1, t4 match the token in the title; t7 matches it only in the
	// description and differs from t1 in status, priority and assignee. t2 and
	// t3 share a title (and status) so the title sort needs the id tie-break.
	t1 := seed("alpha "+taskFilterToken, "", models.TaskStatusPending, models.TaskPriorityHigh, user1, label1)
	t2 := seed("alpha", "", models.TaskStatusPending, models.TaskPriorityHigh, user2, label1, label2)
	t3 := seed("alpha", "", models.TaskStatusPending, models.TaskPriorityLow, user1, label2)
	t4 := seed("beta "+taskFilterToken, "", models.TaskStatusInProgress, models.TaskPriorityMedium, user1)
	t5 := seed("gamma", "", models.TaskStatusCompleted, models.TaskPriorityHigh, user2, label1)
	t6 := seed("delta", "", models.TaskStatusCancelled, models.TaskPriorityLow, user2)
	t7 := seed("plain", "notes mention "+taskFilterToken, models.TaskStatusCompleted, models.TaskPriorityMedium, user2)
	deleted := seed("alpha "+taskFilterToken, taskFilterToken, models.TaskStatusPending, models.TaskPriorityHigh, user1, label1, label2)
	require.NoError(t, db.Delete(&models.Task{}, deleted).Error)

	repo := NewTaskRepository(db)
	ids := func(tasks []models.Task) []uint {
		out := make([]uint, 0, len(tasks))
		for _, task := range tasks {
			out = append(out, task.ID)
		}
		return out
	}
	check := func(t *testing.T, filter TaskListFilter, offset, limit int, sortBy, sortOrder string, wantRows []uint, wantCount int64) {
		t.Helper()
		rows, err := repo.ListByFilter(filter, offset, limit, sortBy, sortOrder, "AssignedTo", "Labels")
		require.NoError(t, err)
		assert.Equal(t, wantRows, ids(rows))
		for _, row := range rows {
			assert.NotEqual(t, deleted, row.ID, "a soft-deleted task is never returned")
			assert.Equal(t, row.AssignedToID, row.AssignedTo.ID, "the assignee is preloaded")
		}
		count, err := repo.CountByFilter(filter)
		require.NoError(t, err)
		assert.Equal(t, wantCount, count)
	}
	u1 := user1.ID
	pending, completed := string(models.TaskStatusPending), string(models.TaskStatusCompleted)
	high := string(models.TaskPriorityHigh)

	t.Run("status alone", func(t *testing.T) {
		check(t, TaskListFilter{Status: pending}, 0, 20, "", "", []uint{t1, t2, t3}, 3)
		check(t, TaskListFilter{Status: string(models.TaskStatusInProgress)}, 0, 20, "", "", []uint{t4}, 1)
		check(t, TaskListFilter{Status: completed}, 0, 20, "", "", []uint{t5, t7}, 2)
		check(t, TaskListFilter{Status: string(models.TaskStatusCancelled)}, 0, 20, "", "", []uint{t6}, 1)
	})

	t.Run("priority alone", func(t *testing.T) {
		check(t, TaskListFilter{Priority: high}, 0, 20, "", "", []uint{t1, t2, t5}, 3)
		check(t, TaskListFilter{Priority: string(models.TaskPriorityMedium)}, 0, 20, "", "", []uint{t4, t7}, 2)
		check(t, TaskListFilter{Priority: string(models.TaskPriorityLow)}, 0, 20, "", "", []uint{t3, t6}, 2)
	})

	t.Run("status and priority", func(t *testing.T) {
		check(t, TaskListFilter{Status: pending, Priority: high}, 0, 20, "", "", []uint{t1, t2}, 2)
		check(t, TaskListFilter{Status: completed, Priority: string(models.TaskPriorityLow)}, 0, 20, "", "", []uint{}, 0)
	})

	t.Run("status, priority and assignee", func(t *testing.T) {
		check(t, TaskListFilter{Status: pending, Priority: high, AssigneeID: &u1}, 0, 20, "", "", []uint{t1}, 1)
		none := uint(0)
		check(t, TaskListFilter{Status: pending, AssigneeID: &none}, 0, 20, "", "", []uint{}, 0)
	})

	t.Run("status, priority and label", func(t *testing.T) {
		check(t, TaskListFilter{Status: pending, Priority: high, LabelID: label1.ID}, 0, 20, "", "", []uint{t1, t2}, 2)
		check(t, TaskListFilter{Status: pending, Priority: high, LabelID: label2.ID}, 0, 20, "", "", []uint{t2}, 1)
		check(t, TaskListFilter{Status: pending, LabelID: label2.ID, AssigneeID: &u1}, 0, 20, "", "", []uint{t3}, 1)
	})

	t.Run("search keeps the OR parenthesised", func(t *testing.T) {
		check(t, TaskListFilter{Search: taskFilterToken}, 0, 20, "", "", []uint{t1, t4, t7}, 3)
		check(t, TaskListFilter{Status: pending, Search: taskFilterToken}, 0, 20, "", "", []uint{t1}, 1)
		check(t, TaskListFilter{Status: completed, Search: taskFilterToken}, 0, 20, "", "", []uint{t7}, 1)
		check(t, TaskListFilter{Priority: high, Search: taskFilterToken}, 0, 20, "", "", []uint{t1}, 1)
		check(t, TaskListFilter{AssigneeID: &u1, Search: taskFilterToken}, 0, 20, "", "", []uint{t1, t4}, 2)
	})

	t.Run("every predicate together", func(t *testing.T) {
		check(t, TaskListFilter{Status: pending, Priority: high, AssigneeID: &u1, LabelID: label1.ID, Search: taskFilterToken},
			0, 20, "", "", []uint{t1}, 1)
	})

	t.Run("title sort with the id tie-break", func(t *testing.T) {
		check(t, TaskListFilter{Status: pending}, 0, 20, "title", "asc", []uint{t2, t3, t1}, 3)
		check(t, TaskListFilter{Status: pending}, 0, 20, "title", "desc", []uint{t1, t3, t2}, 3)
		check(t, TaskListFilter{Status: pending, LabelID: label1.ID}, 0, 20, "title", "asc", []uint{t2, t1}, 2)
	})

	t.Run("paging applies after the filter", func(t *testing.T) {
		check(t, TaskListFilter{Status: pending}, 1, 1, "", "", []uint{t2}, 3)
		check(t, TaskListFilter{Status: pending}, 1, 2, "title", "desc", []uint{t3, t2}, 3)
		check(t, TaskListFilter{Status: pending}, 3, 2, "", "", []uint{}, 3)
	})

	t.Run("an unknown sort column is an error", func(t *testing.T) {
		_, err := repo.ListByFilter(TaskListFilter{Status: pending}, 0, 20, "title; DROP", "asc")
		require.Error(t, err)
	})

	// #618: the search is a literal substring. Each literal row holds one LIKE
	// metacharacter, and each decoy row holds another character where an
	// unescaped wildcard would match it. Seeded last, so the subtests above keep
	// their rows and counts.
	t.Run("search matches wildcards literally", func(t *testing.T) {
		underscore := seed("a_b", "", models.TaskStatusPending, models.TaskPriorityLow, user1)
		seed("axb", "", models.TaskStatusPending, models.TaskPriorityLow, user1)
		percent := seed("50%", "", models.TaskStatusPending, models.TaskPriorityLow, user1)
		seed("50x", "", models.TaskStatusPending, models.TaskPriorityLow, user1)
		bang := seed("plain", "a!b", models.TaskStatusPending, models.TaskPriorityLow, user1)
		backslash := seed(`a\b`, "", models.TaskStatusPending, models.TaskPriorityLow, user1)

		for _, tc := range []struct {
			term string
			want uint
		}{{"_", underscore}, {"%", percent}, {"!", bang}, {`\`, backslash}} {
			check(t, TaskListFilter{Search: tc.term}, 0, 20, "", "", []uint{tc.want}, 1)
		}
	})
}
