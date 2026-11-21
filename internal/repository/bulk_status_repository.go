package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// GetLeadsByIDs is a locking read of the batch's live leads in id order
// (#182): `SELECT … FOR UPDATE … ORDER BY id` on MySQL/MariaDB, a plain read
// on SQLite where the dialector drops the clause. Soft-deleted leads still
// read as missing. It is the "leads in id order" step of the shared lock order
// companies -> leads -> customers -> deals: the batch takes no company lock
// (SetLeadStatus never writes company_id), so a company delete that already
// unlinked these leads makes the batch wait on the lead rows, and a batch that
// locked them first makes the delete's UnlinkLeads wait, with no lock cycle.
func (r *bulkStatusRepository) GetLeadsByIDs(ids []uint) ([]models.Lead, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var leads []models.Lead
	err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ids).Order("id ASC").Find(&leads).Error
	if err != nil {
		return nil, err
	}
	return leads, nil
}

// GetTicketsByIDs is a locking read of the batch's live tickets in id order
// (#242): `SELECT … FOR UPDATE … ORDER BY id` on MySQL, a plain read on SQLite
// where the dialector drops the clause. Inside the batch transaction it locks
// the rows before the status write, in the order an automation step and the
// other ticket writers lock one ticket row each, and the events are recorded
// from the status this read returns, so a status committed by another
// connection after the batch began is the from_value, not a stale snapshot.
func (r *bulkStatusRepository) GetTicketsByIDs(ids []uint) ([]models.Ticket, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var tickets []models.Ticket
	err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ids).Order("id ASC").Find(&tickets).Error
	if err != nil {
		return nil, err
	}
	return tickets, nil
}

// GetTasksByIDs is a locking read of the batch's live tasks in id order
// (#558): `SELECT … FOR UPDATE … ORDER BY id` on MySQL/MariaDB, a plain read
// on SQLite where the dialector drops the clause. Soft-deleted tasks still
// read as missing. Inside the batch transaction it locks the rows before the
// found, assignee and completed-task checks, the same row the single task
// update locks (taskService.UpdateWithLabels through GetByIDForUpdate), so
// each writer judges the status and assignee the other committed.
func (r *bulkStatusRepository) GetTasksByIDs(ids []uint) ([]models.Task, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var tasks []models.Task
	err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ids).Order("id ASC").Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
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
