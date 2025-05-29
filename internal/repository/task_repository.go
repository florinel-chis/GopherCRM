package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
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