package repository

import (
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupLabelDB gives each test a private schema on the shared in-memory DSN,
// the same way the customer export tests do.
func setupLabelDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{
		Logger: logger.Discard,
	})
	require.NoError(t, err)

	require.NoError(t, db.Migrator().DropTable("task_labels", &models.Task{}, &models.Label{}, &models.User{}))
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Label{}, &models.Task{}))

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func createLabelTestUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	user := &models.User{
		Email:     "assignee@example.com",
		FirstName: "Ass",
		LastName:  "Ignee",
		Role:      models.RoleSupport,
		IsActive:  true,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func createLabelTestTask(t *testing.T, db *gorm.DB, title string, assigneeID uint, labels ...models.Label) *models.Task {
	t.Helper()
	task := &models.Task{
		Title:        title,
		Status:       models.TaskStatusPending,
		Priority:     models.TaskPriorityMedium,
		AssignedToID: assigneeID,
		Labels:       labels,
	}
	require.NoError(t, db.Create(task).Error)
	return task
}

// The whole point of the many2many is that a task can be read back with the
// labels it was written with, and a label can be read back with the tasks that
// carry it. Both directions are checked so a join-table misconfiguration
// cannot hide behind a one-way read.
func TestLabelRoundTripsThroughTheJoinTable(t *testing.T) {
	db := setupLabelDB(t)
	user := createLabelTestUser(t, db)

	urgent := models.Label{Name: "Urgent", Color: "#FF0000"}
	backend := models.Label{Name: "Backend", Color: "#0000FF"}
	require.NoError(t, db.Create(&urgent).Error)
	require.NoError(t, db.Create(&backend).Error)

	task := createLabelTestTask(t, db, "Ship the thing", user.ID, urgent, backend)

	taskRepo := NewTaskRepository(db)
	loaded, err := taskRepo.GetByID(task.ID)
	require.NoError(t, err)
	require.Len(t, loaded.Labels, 2)
	names := []string{loaded.Labels[0].Name, loaded.Labels[1].Name}
	assert.ElementsMatch(t, []string{"Urgent", "Backend"}, names)

	var joined []models.Task
	require.NoError(t, db.Model(&urgent).Association("Tasks").Find(&joined))
	require.Len(t, joined, 1)
	assert.Equal(t, task.ID, joined[0].ID)
}

// Deleting a label must take its join rows with it. A label row that vanished
// while its task_labels rows survived would leave every task carrying it with
// a dangling reference, and the tasks themselves must be left alone.
func TestDeleteLabelIsHardAndClearsTheJoinRows(t *testing.T) {
	db := setupLabelDB(t)
	user := createLabelTestUser(t, db)
	repo := NewLabelRepository(db)

	label := &models.Label{Name: "Doomed", Color: "#123456"}
	require.NoError(t, repo.Create(label))
	task := createLabelTestTask(t, db, "Survivor", user.ID, *label)

	require.NoError(t, repo.Delete(label.ID))

	// Hard delete: not even an Unscoped read finds the row.
	var remaining int64
	require.NoError(t, db.Unscoped().Model(&models.Label{}).Where("id = ?", label.ID).Count(&remaining).Error)
	assert.Zero(t, remaining, "label must be gone, not soft-deleted")

	var joinRows int64
	require.NoError(t, db.Table("task_labels").Where("label_id = ?", label.ID).Count(&joinRows).Error)
	assert.Zero(t, joinRows, "join rows must be cleared with the label")

	taskRepo := NewTaskRepository(db)
	survivor, err := taskRepo.GetByID(task.ID)
	require.NoError(t, err)
	assert.Empty(t, survivor.Labels)
	assert.Equal(t, "Survivor", survivor.Title)
}

func TestDeleteLabelReportsAMissingRowAsNotFound(t *testing.T) {
	db := setupLabelDB(t)
	repo := NewLabelRepository(db)

	err := repo.Delete(4242)
	require.Error(t, err)
	assert.True(t, apperrors.IsNotFound(err), "expected a not-found error, got %v", err)
}

// The unique index is the backstop behind the service's pre-check; a duplicate
// that slips past the pre-check must still surface as the sentinel rather than
// as a raw driver message.
func TestCreateLabelRejectsADuplicateNameWithTheSentinel(t *testing.T) {
	db := setupLabelDB(t)
	repo := NewLabelRepository(db)

	require.NoError(t, repo.Create(&models.Label{Name: "Unique", Color: "#ABCDEF"}))

	err := repo.Create(&models.Label{Name: "Unique", Color: "#FEDCBA"})
	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.ErrDuplicateLabelName)
	assert.NotContains(t, err.Error(), "UNIQUE constraint", "the driver message must not leak to the caller")
}

func TestListLabelsIsOrderedByNameAndCountsLiveTasks(t *testing.T) {
	db := setupLabelDB(t)
	user := createLabelTestUser(t, db)
	repo := NewLabelRepository(db)

	zeta := &models.Label{Name: "zeta", Color: "#111111"}
	alpha := &models.Label{Name: "alpha", Color: "#222222"}
	unused := &models.Label{Name: "mid", Color: "#333333"}
	require.NoError(t, repo.Create(zeta))
	require.NoError(t, repo.Create(alpha))
	require.NoError(t, repo.Create(unused))

	createLabelTestTask(t, db, "One", user.ID, *alpha)
	createLabelTestTask(t, db, "Two", user.ID, *alpha, *zeta)
	deleted := createLabelTestTask(t, db, "Three", user.ID, *zeta)
	require.NoError(t, db.Delete(deleted).Error)

	labels, err := repo.List()
	require.NoError(t, err)
	require.Len(t, labels, 3)

	assert.Equal(t, []string{"alpha", "mid", "zeta"}, []string{labels[0].Name, labels[1].Name, labels[2].Name})
	assert.Equal(t, int64(2), labels[0].TaskCount)
	assert.Equal(t, int64(0), labels[1].TaskCount, "a label nobody uses counts zero, it is not omitted")
	// The third task was soft-deleted, so it must not be counted even though
	// its join row still exists.
	assert.Equal(t, int64(1), labels[2].TaskCount)
}

func TestGetByIDCarriesTheTaskCount(t *testing.T) {
	db := setupLabelDB(t)
	user := createLabelTestUser(t, db)
	repo := NewLabelRepository(db)

	label := &models.Label{Name: "Counted", Color: "#0F0F0F"}
	require.NoError(t, repo.Create(label))
	createLabelTestTask(t, db, "A", user.ID, *label)
	createLabelTestTask(t, db, "B", user.ID, *label)

	loaded, err := repo.GetByID(label.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), loaded.TaskCount)
}

// SQLite compares strings case-sensitively and MySQL's default collation does
// not. The repository's LOWER() on both sides is what makes the two agree, so
// this test is the SQLite half of that contract.
func TestExistsByNameInsensitiveIgnoresCaseAndTheExcludedRow(t *testing.T) {
	db := setupLabelDB(t)
	repo := NewLabelRepository(db)

	label := &models.Label{Name: "Urgent", Color: "#FF0000"}
	require.NoError(t, repo.Create(label))

	exists, err := repo.ExistsByNameInsensitive("urgent", 0)
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = repo.ExistsByNameInsensitive("URGENT", label.ID)
	require.NoError(t, err)
	assert.False(t, exists, "a label may collide with itself; that is a recolour, not a duplicate")

	exists, err = repo.ExistsByNameInsensitive("something else", 0)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestFindByIDsReturnsOnlyTheLabelsThatExist(t *testing.T) {
	db := setupLabelDB(t)
	repo := NewLabelRepository(db)

	first := &models.Label{Name: "First", Color: "#010101"}
	second := &models.Label{Name: "Second", Color: "#020202"}
	require.NoError(t, repo.Create(first))
	require.NoError(t, repo.Create(second))

	found, err := repo.FindByIDs([]uint{first.ID, second.ID, 9999})
	require.NoError(t, err)
	assert.Len(t, found, 2, "an unknown id is absent from the result, which is how the caller detects it")

	empty, err := repo.FindByIDs(nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// The label filter is the read path behind GET /tasks?label_id=N. It has to
// narrow correctly, respect the assignee scoping, and sort through a join
// without tripping over an ambiguous column name.
func TestListByLabelFiltersSortsAndScopes(t *testing.T) {
	db := setupLabelDB(t)
	owner := createLabelTestUser(t, db)
	other := &models.User{Email: "other@example.com", FirstName: "Oth", LastName: "Er", Role: models.RoleSupport, IsActive: true}
	require.NoError(t, db.Create(other).Error)

	labelRepo := NewLabelRepository(db)
	tagged := &models.Label{Name: "Tagged", Color: "#AA00AA"}
	untagged := &models.Label{Name: "Untagged", Color: "#00AA00"}
	require.NoError(t, labelRepo.Create(tagged))
	require.NoError(t, labelRepo.Create(untagged))

	createLabelTestTask(t, db, "Bravo", owner.ID, *tagged)
	createLabelTestTask(t, db, "Alpha", owner.ID, *tagged)
	createLabelTestTask(t, db, "Charlie", other.ID, *tagged)
	createLabelTestTask(t, db, "Delta", owner.ID, *untagged)

	taskRepo := NewTaskRepository(db)

	all, err := taskRepo.ListByLabel(tagged.ID, nil, 0, 20, "title", "asc", "AssignedTo", "Labels")
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, []string{"Alpha", "Bravo", "Charlie"}, []string{all[0].Title, all[1].Title, all[2].Title})
	assert.Equal(t, owner.ID, all[0].AssignedTo.ID, "the preloads must still be applied through the join")
	assert.Len(t, all[0].Labels, 1)

	total, err := taskRepo.CountByLabel(tagged.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)

	mine, err := taskRepo.ListByLabel(tagged.ID, &owner.ID, 0, 20, "title", "desc")
	require.NoError(t, err)
	require.Len(t, mine, 2)
	assert.Equal(t, []string{"Bravo", "Alpha"}, []string{mine[0].Title, mine[1].Title})

	mineTotal, err := taskRepo.CountByLabel(tagged.ID, &owner.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), mineTotal)

	// An unvalidated sort column must be refused, not interpolated.
	_, err = taskRepo.ListByLabel(tagged.ID, nil, 0, 20, "name); DROP TABLE tasks;--", "asc")
	assert.Error(t, err)
}

func TestListByLabelExcludesSoftDeletedTasks(t *testing.T) {
	db := setupLabelDB(t)
	user := createLabelTestUser(t, db)
	labelRepo := NewLabelRepository(db)

	label := &models.Label{Name: "Live", Color: "#999999"}
	require.NoError(t, labelRepo.Create(label))

	keep := createLabelTestTask(t, db, "Keep", user.ID, *label)
	drop := createLabelTestTask(t, db, "Drop", user.ID, *label)
	require.NoError(t, db.Delete(drop).Error)

	taskRepo := NewTaskRepository(db)
	tasks, err := taskRepo.ListByLabel(label.ID, nil, 0, 20, "", "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, keep.ID, tasks[0].ID)

	count, err := taskRepo.CountByLabel(label.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

// CreateWithLabels and UpdateWithLabels are the write half of the feature.
// Update replaces the set outright, which is the semantic the PUT endpoint
// promises, and clearing it must remove the join rows rather than orphan them.
func TestCreateAndUpdateReplaceTheWholeLabelSet(t *testing.T) {
	db := setupLabelDB(t)
	user := createLabelTestUser(t, db)
	labelRepo := NewLabelRepository(db)

	one := &models.Label{Name: "One", Color: "#111111"}
	two := &models.Label{Name: "Two", Color: "#222222"}
	three := &models.Label{Name: "Three", Color: "#333333"}
	for _, label := range []*models.Label{one, two, three} {
		require.NoError(t, labelRepo.Create(label))
	}

	taskRepo := NewTaskRepository(db)
	task := &models.Task{
		Title:        "Labelled",
		Status:       models.TaskStatusPending,
		Priority:     models.TaskPriorityMedium,
		AssignedToID: user.ID,
	}
	require.NoError(t, taskRepo.CreateWithLabels(task, []models.Label{*one, *two}))

	loaded, err := taskRepo.GetByID(task.ID)
	require.NoError(t, err)
	require.Len(t, loaded.Labels, 2)

	// Replacing with a disjoint set drops the old rows and adds the new one.
	require.NoError(t, taskRepo.UpdateWithLabels(loaded, []models.Label{*three}))
	loaded, err = taskRepo.GetByID(task.ID)
	require.NoError(t, err)
	require.Len(t, loaded.Labels, 1)
	assert.Equal(t, "Three", loaded.Labels[0].Name)

	// An empty set clears it.
	require.NoError(t, taskRepo.UpdateWithLabels(loaded, nil))
	loaded, err = taskRepo.GetByID(task.ID)
	require.NoError(t, err)
	assert.Empty(t, loaded.Labels)

	var joinRows int64
	require.NoError(t, db.Table("task_labels").Where("task_id = ?", task.ID).Count(&joinRows).Error)
	assert.Zero(t, joinRows)

	// Writing labels must never rewrite the labels themselves.
	var untouched models.Label
	require.NoError(t, db.First(&untouched, one.ID).Error)
	assert.Equal(t, "One", untouched.Name)
	assert.Equal(t, "#111111", untouched.Color)
}

// The label listings back /tasks/my and the non-admin task list, which read
// through GetByAssignedToID; a task there without its labels renders without
// its chips.
func TestGetByAssignedToIDPreloadsLabels(t *testing.T) {
	db := setupLabelDB(t)
	user := createLabelTestUser(t, db)
	labelRepo := NewLabelRepository(db)

	label := &models.Label{Name: "Mine", Color: "#ABABAB"}
	require.NoError(t, labelRepo.Create(label))
	createLabelTestTask(t, db, "Mine to do", user.ID, *label)

	taskRepo := NewTaskRepository(db)
	tasks, err := taskRepo.GetByAssignedToID(user.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Len(t, tasks[0].Labels, 1)
	assert.Equal(t, "Mine", tasks[0].Labels[0].Name)
}

// A hard delete frees the name for reuse. That is the whole reason labels do
// not soft-delete, so it is worth pinning down.
func TestDeletedLabelNameCanBeReused(t *testing.T) {
	db := setupLabelDB(t)
	repo := NewLabelRepository(db)

	first := &models.Label{Name: "Recycled", Color: "#121212"}
	require.NoError(t, repo.Create(first))
	require.NoError(t, repo.Delete(first.ID))

	second := &models.Label{Name: "Recycled", Color: "#343434"}
	require.NoError(t, repo.Create(second), "reusing the name of a deleted label must be allowed")
	assert.NotEqual(t, first.ID, second.ID)
}
