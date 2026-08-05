package repository

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
)

type taskRepository struct {
	db *gorm.DB
}

func NewTaskRepository(db *gorm.DB) TaskRepository {
	return &taskRepository{db: db}
}

func (r *taskRepository) Create(task *models.Task) error {
	return r.db.Create(task).Error
}

func (r *taskRepository) GetByID(id uint) (*models.Task, error) {
	var task models.Task
	err := r.db.Preload("AssignedTo").Preload("Lead").Preload("Customer").First(&task, id).Error
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (r *taskRepository) GetByAssignedToID(assignedToID uint, offset, limit int) ([]models.Task, error) {
	var tasks []models.Task
	err := r.db.Where("assigned_to_id = ?", assignedToID).Offset(offset).Limit(limit).Find(&tasks).Error
	return tasks, err
}

func (r *taskRepository) Update(task *models.Task) error {
	return r.db.Save(task).Error
}

func (r *taskRepository) Delete(id uint) error {
	return r.db.Delete(&models.Task{}, id).Error
}

func (r *taskRepository) List(offset, limit int) ([]models.Task, error) {
	var tasks []models.Task
	err := r.db.Preload("AssignedTo").Offset(offset).Limit(limit).Find(&tasks).Error
	return tasks, err
}

func (r *taskRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.Task{}).Count(&count).Error
	return count, err
}

func (r *taskRepository) CountByAssignedToID(assignedToID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Task{}).Where("assigned_to_id = ?", assignedToID).Count(&count).Error
	return count, err
}

func (r *taskRepository) CountPending() (int64, error) {
	var count int64
	err := r.db.Model(&models.Task{}).Where("status IN ?", []string{"pending", "in_progress"}).Count(&count).Error
	return count, err
}

func (r *taskRepository) GetByIDWithPreloads(id uint, preloads ...string) (*models.Task, error) {
	var task models.Task
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	err := query.First(&task, id).Error
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (r *taskRepository) GetByAssignedToIDWithPreloads(assignedToID uint, offset, limit int, preloads ...string) ([]models.Task, error) {
	var tasks []models.Task
	query := r.db.Where("assigned_to_id = ?", assignedToID)
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	err := query.Offset(offset).Limit(limit).Find(&tasks).Error
	return tasks, err
}

func (r *taskRepository) ListWithPreloads(offset, limit int, preloads ...string) ([]models.Task, error) {
	var tasks []models.Task
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	err := query.Offset(offset).Limit(limit).Find(&tasks).Error
	return tasks, err
}

func (r *taskRepository) ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Task, error) {
	var tasks []models.Task
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	if sortBy != "" {
		orderClause, err := utils.SafeOrderClause("tasks", sortBy, sortOrder)
		if err != nil {
			return nil, err
		}
		if orderClause != "" {
			query = query.Order(orderClause)
		}
	}
	err := query.Offset(offset).Limit(limit).Find(&tasks).Error
	return tasks, err
}

func (r *taskRepository) Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Task, error) {
	var tasks []models.Task
	db := r.db
	for _, preload := range preloads {
		db = db.Preload(preload)
	}
	searchPattern := "%" + query + "%"
	db = db.Where(
		"title LIKE ? OR description LIKE ?",
		searchPattern, searchPattern,
	)
	if sortBy != "" {
		orderClause, err := utils.SafeOrderClause("tasks", sortBy, sortOrder)
		if err != nil {
			return nil, err
		}
		if orderClause != "" {
			db = db.Order(orderClause)
		}
	}
	err := db.Offset(offset).Limit(limit).Find(&tasks).Error
	return tasks, err
}

func (r *taskRepository) CountSearch(query string) (int64, error) {
	var count int64
	searchPattern := "%" + query + "%"
	err := r.db.Model(&models.Task{}).Where(
		"title LIKE ? OR description LIKE ?",
		searchPattern, searchPattern,
	).Count(&count).Error
	return count, err
}

// CountByStatus returns the number of live tasks per status. Statuses with no
// rows are absent from the map; the caller supplies the full label set.
func (r *taskRepository) CountByStatus() (map[string]int64, error) {
	return countGroupedByColumn(r.db.Model(&models.Task{}), "status")
}

// ListUpcoming returns the open tasks with the nearest due dates first.
//
// Completed and cancelled tasks are excluded — they are not upcoming work — and
// so are tasks with no due date at all, which have no place on a due-soonest
// list. Overdue tasks are deliberately kept: they are the ones that most need
// to surface. A nil assignedToID means every assignee.
func (r *taskRepository) ListUpcoming(assignedToID *uint, limit int) ([]models.Task, error) {
	tasks := []models.Task{}
	err := r.openDueQuery(assignedToID).
		Order("due_date ASC, id ASC").
		Limit(limit).
		Find(&tasks).Error
	return tasks, err
}

// ListDueBetween returns the open tasks whose due date falls in [from, to],
// nearest first. A nil assignedToID means every assignee.
func (r *taskRepository) ListDueBetween(assignedToID *uint, from, to time.Time, limit int) ([]models.Task, error) {
	tasks := []models.Task{}
	err := r.openDueQuery(assignedToID).
		Where("due_date >= ? AND due_date <= ?", from, to).
		Order("due_date ASC, id ASC").
		Limit(limit).
		Find(&tasks).Error
	return tasks, err
}

// openDueQuery is the shared predicate behind the two due-date listings: a task
// that is still open and actually has a due date, optionally narrowed to one
// assignee.
func (r *taskRepository) openDueQuery(assignedToID *uint) *gorm.DB {
	query := r.db.Preload("AssignedTo").
		Where("due_date IS NOT NULL").
		Where("status NOT IN ?", []string{
			string(models.TaskStatusCompleted),
			string(models.TaskStatusCancelled),
		})
	if assignedToID != nil {
		query = query.Where("assigned_to_id = ?", *assignedToID)
	}
	return query
}

// ListRecentlyCompleted returns completed tasks, most recently touched first.
// There is no completed_at column, so updated_at stands in for the completion
// time.
func (r *taskRepository) ListRecentlyCompleted(limit int) ([]models.Task, error) {
	tasks := []models.Task{}
	err := r.db.Preload("AssignedTo").
		Where("status = ?", models.TaskStatusCompleted).
		Order("updated_at DESC, id DESC").
		Limit(limit).
		Find(&tasks).Error
	return tasks, err
}

func (r *taskRepository) WithTx(tx *gorm.DB) TaskRepository {
	return &taskRepository{db: tx}
}