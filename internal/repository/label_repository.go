package repository

import (
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type labelRepository struct {
	db *gorm.DB
}

func NewLabelRepository(db *gorm.DB) LabelRepository {
	return &labelRepository{db: db}
}

func (r *labelRepository) Create(label *models.Label) error {
	if err := r.db.Create(label).Error; err != nil {
		// The unique index on labels.name is the backstop behind the service's
		// case-insensitive pre-check: two concurrent creates can both pass the
		// pre-check and only one can win here. Surface that as the sentinel
		// rather than as a raw driver string, which would leak the index name
		// and the driver error code to the caller.
		if isDuplicateKeyError(err) {
			return fmt.Errorf("label %q already exists: %w", label.Name, apperrors.ErrDuplicateLabelName)
		}
		return err
	}
	return nil
}

func (r *labelRepository) GetByID(id uint) (*models.Label, error) {
	var label models.Label
	if err := r.db.First(&label, id).Error; err != nil {
		return nil, err
	}

	count, err := r.countTasks(label.ID)
	if err != nil {
		return nil, err
	}
	label.TaskCount = count

	return &label, nil
}

func (r *labelRepository) Update(label *models.Label) error {
	// Omit the association: an update carries the label's own fields only, and
	// saving Tasks here would rewrite the join table as a side effect of a
	// rename.
	if err := r.db.Omit("Tasks").Save(label).Error; err != nil {
		if isDuplicateKeyError(err) {
			return fmt.Errorf("label %q already exists: %w", label.Name, apperrors.ErrDuplicateLabelName)
		}
		return err
	}
	return nil
}

// Delete removes a label permanently and detaches it from every task, both in
// one transaction: a label that vanished but left its join rows behind would
// make every task carrying it fail to preload.
//
// The delete is Unscoped on purpose — see the type comment on models.Label for
// why labels do not take part in soft deletion.
func (r *labelRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM task_labels WHERE label_id = ?", id).Error; err != nil {
			return err
		}

		result := tx.Unscoped().Delete(&models.Label{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// List returns every label ordered by name, each carrying the number of live
// tasks that use it.
//
// The count is a second query rather than a LEFT JOIN … GROUP BY on the first:
// selecting non-aggregated columns alongside a COUNT is only legal under MySQL
// 8's ONLY_FULL_GROUP_BY by way of functional-dependency detection, and this
// way the statement means exactly the same thing on SQLite.
func (r *labelRepository) List() ([]models.Label, error) {
	labels := []models.Label{}
	if err := r.db.Order("name ASC").Find(&labels).Error; err != nil {
		return nil, err
	}
	if len(labels) == 0 {
		return labels, nil
	}

	ids := make([]uint, 0, len(labels))
	for _, label := range labels {
		ids = append(ids, label.ID)
	}

	counts, err := r.countTasksByLabel(ids)
	if err != nil {
		return nil, err
	}
	for i := range labels {
		labels[i].TaskCount = counts[labels[i].ID]
	}

	return labels, nil
}

// FindByIDs returns the labels matching the given ids, in no particular order.
// Missing ids are simply absent from the result; comparing the length against
// the request is how the caller detects an unknown reference.
func (r *labelRepository) FindByIDs(ids []uint) ([]models.Label, error) {
	labels := []models.Label{}
	if len(ids) == 0 {
		return labels, nil
	}
	err := r.db.Where("id IN ?", ids).Find(&labels).Error
	return labels, err
}

// ExistsByNameInsensitive reports whether another label already uses the name,
// compared case-insensitively. excludeID lets an update ignore the row it is
// about to write; pass 0 for a create.
//
// LOWER() on both sides is what makes MySQL (case-insensitive collation) and
// SQLite (case-sensitive) agree: without it, "Urgent" and "urgent" would be a
// duplicate in production and two distinct labels in the test suite.
func (r *labelRepository) ExistsByNameInsensitive(name string, excludeID uint) (bool, error) {
	query := r.db.Model(&models.Label{}).Where("LOWER(name) = LOWER(?)", name)
	if excludeID != 0 {
		query = query.Where("id <> ?", excludeID)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// countTasks returns how many live tasks carry one label.
func (r *labelRepository) countTasks(id uint) (int64, error) {
	counts, err := r.countTasksByLabel([]uint{id})
	if err != nil {
		return 0, err
	}
	return counts[id], nil
}

// countTasksByLabel counts the live tasks per label. Soft-deleted tasks are
// excluded explicitly: the join is expressed as raw SQL, so GORM's soft-delete
// scope — which only applies to the model being queried — does not reach it.
func (r *labelRepository) countTasksByLabel(ids []uint) (map[uint]int64, error) {
	counts := map[uint]int64{}
	if len(ids) == 0 {
		return counts, nil
	}

	type labelTaskCount struct {
		LabelID uint
		Total   int64
	}

	rows := []labelTaskCount{}
	err := r.db.Table("task_labels").
		Select("task_labels.label_id AS label_id, COUNT(task_labels.task_id) AS total").
		Joins("JOIN tasks ON tasks.id = task_labels.task_id AND tasks.deleted_at IS NULL").
		Where("task_labels.label_id IN ?", ids).
		Group("task_labels.label_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		counts[row.LabelID] = row.Total
	}
	return counts, nil
}

func (r *labelRepository) WithTx(tx *gorm.DB) LabelRepository {
	return &labelRepository{db: tx}
}
