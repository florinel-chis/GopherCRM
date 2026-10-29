package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type bulkStatusRepository struct {
	db *gorm.DB
}

func NewBulkStatusRepository(db *gorm.DB) BulkStatusRepository {
	return &bulkStatusRepository{db: db}
}

func (r *bulkStatusRepository) WithTx(tx *gorm.DB) BulkStatusRepository {
	return &bulkStatusRepository{db: tx}
}

// Bulk status updates
//
// These are the read and write halves of a bulk status change. They are kept
// apart so the service can authorize every row before a single one is written:
// the read returns whatever exists (a caller comparing the result against the
// requested IDs learns which are missing, without a per-ID round trip), and the
// write sets one column on all of them in a single statement.
//
// Nothing here touches personal data — status is a workflow field — so no
// erasure is involved and no per-item isolation is needed. All-or-nothing is
// the whole contract: the caller runs both halves inside one transaction and
// returns an error to roll it back.

// getByIDs loads whatever rows exist among ids. Soft-deleted rows are excluded
// by GORM's default scope, so a deleted record reads as missing, which is what
// it is as far as the API is concerned.
func getByIDs[T any](db *gorm.DB, ids []uint) ([]T, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var entities []T
	if err := db.Where("id IN ?", ids).Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// setStatus writes one status onto every listed row.
//
// RowsAffected is deliberately not treated as a count of updated records: MySQL
// reports rows *changed*, not rows *matched*, so a record that already holds the
// requested status is not counted. Existence is established by the caller's
// preceding read instead.
func setStatus(db *gorm.DB, model interface{}, ids []uint, status interface{}) error {
	if len(ids) == 0 {
		return nil
	}
	return db.Model(model).Where("id IN ?", ids).Update("status", status).Error
}

func (r *bulkStatusRepository) GetLeadsByIDs(ids []uint) ([]models.Lead, error) {
	return getByIDs[models.Lead](r.db, ids)
}

func (r *bulkStatusRepository) GetTicketsByIDs(ids []uint) ([]models.Ticket, error) {
	return getByIDs[models.Ticket](r.db, ids)
}

func (r *bulkStatusRepository) GetTasksByIDs(ids []uint) ([]models.Task, error) {
	return getByIDs[models.Task](r.db, ids)
}

func (r *bulkStatusRepository) SetLeadStatus(ids []uint, status models.LeadStatus) error {
	return setStatus(r.db, &models.Lead{}, ids, status)
}

func (r *bulkStatusRepository) SetTicketStatus(ids []uint, status models.TicketStatus) error {
	return setStatus(r.db, &models.Ticket{}, ids, status)
}

func (r *bulkStatusRepository) SetTaskStatus(ids []uint, status models.TaskStatus) error {
	return setStatus(r.db, &models.Task{}, ids, status)
}
