package repository

import (
	"errors"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ticketRepository struct {
	db *gorm.DB
}

func NewTicketRepository(db *gorm.DB) TicketRepository {
	return &ticketRepository{db: db}
}

// ticketFirstResponseColumn is written only by CreateCommentWithFirstResponse.
// Create and Update leave it out of their statements, so no struct a caller
// filled in, or read before the stamp, can set, overwrite or clear it.
const ticketFirstResponseColumn = "first_responded_at"

func (r *ticketRepository) Create(ticket *models.Ticket) error {
	if err := r.db.Omit(ticketFirstResponseColumn).Create(ticket).Error; err != nil {
		return err
	}
	// The column was not written, so the new row holds NULL.
	ticket.FirstRespondedAt = nil
	return nil
}

func (r *ticketRepository) GetByID(id uint) (*models.Ticket, error) {
	var ticket models.Ticket
	err := r.db.Preload("Customer").Preload("AssignedTo").Preload("Comments", func(db *gorm.DB) *gorm.DB {
		return db.Order("created_at ASC, id ASC")
	}).Preload("Comments.Author").First(&ticket, id).Error
	if err != nil {
		return nil, err
	}
	return &ticket, nil
}

func (r *ticketRepository) GetByCustomerID(customerID uint, offset, limit int) ([]models.Ticket, error) {
	var tickets []models.Ticket
	err := orderByID(r.db.Where("customer_id = ?", customerID), "tickets").Offset(offset).Limit(limit).Find(&tickets).Error
	return tickets, err
}

func (r *ticketRepository) GetByAssignedToID(assignedToID uint, offset, limit int) ([]models.Ticket, error) {
	var tickets []models.Ticket
	err := orderByID(r.db.Where("assigned_to_id = ?", assignedToID), "tickets").Offset(offset).Limit(limit).Find(&tickets).Error
	return tickets, err
}

// Update saves the ticket with GORM's Save, except first_responded_at: a ticket
// read before the first staff reply would otherwise write its NULL back over
// the stamp. The struct then carries the stored value.
func (r *ticketRepository) Update(ticket *models.Ticket) error {
	if err := r.db.Omit(ticketFirstResponseColumn).Save(ticket).Error; err != nil {
		return err
	}
	var stored struct {
		FirstRespondedAt *time.Time
	}
	if err := r.db.Model(&models.Ticket{}).Select(ticketFirstResponseColumn).
		Where("id = ?", ticket.ID).Scan(&stored).Error; err != nil {
		return err
	}
	ticket.FirstRespondedAt = stored.FirstRespondedAt
	return nil
}

// GetByIDForUpdate is the locking read of a ticket write (#242): the bare live
// ticket row with `FOR UPDATE` on MySQL (a plain read on SQLite, where the
// dialector drops the clause), then its assignee with a plain read so the
// caller can answer with it. Only the ticket row is locked. A soft-deleted or
// missing ticket is gorm.ErrRecordNotFound.
func (r *ticketRepository) GetByIDForUpdate(id uint) (*models.Ticket, error) {
	var ticket models.Ticket
	if err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ticket, id).Error; err != nil {
		return nil, err
	}
	if ticket.AssignedToID != nil {
		var assignee models.User
		err := r.db.Where("id = ?", *ticket.AssignedToID).Take(&assignee).Error
		switch {
		case err == nil:
			ticket.AssignedTo = &assignee
		case errors.Is(err, gorm.ErrRecordNotFound):
		default:
			return nil, err
		}
	}
	return &ticket, nil
}

// Delete runs one transaction (a savepoint of the caller's transaction under
// WithTx) that, in order:
//
//  1. locks the live ticket row (`FOR UPDATE` on MySQL); a missing or
//     already-deleted ticket is gorm.ErrRecordNotFound and nothing is written;
//  2. hard-deletes the ticket's comment mail delivery records (#219);
//  3. cancels the ticket's pending, waiting and running automation runs with
//     cancel_reason record_deleted and finished_at (#242);
//  4. hard-deletes the notifications whose entity is the ticket (#242);
//  5. soft-deletes the ticket.
//
// A failure of any step rolls all of them back. Tables absent from the schema
// (fixtures that migrate a subset) are skipped by HasTable probes, never by
// swallowing an error. The ticket row is locked first, the order an automation
// step on the ticket and every ticket writer use, so a step racing the delete
// either commits before it (and its run is then cancelled here) or waits and
// finds the ticket gone. Finished runs, other tickets' runs and notifications
// about leads and deals are untouched; the comments stay as before.
func (r *ticketRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var locked models.Ticket
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&locked, id).Error; err != nil {
			return err
		}
		if err := purgeTicketMailDeliveriesOfTicket(tx, id); err != nil {
			return err
		}
		if err := cancelAutomationForEntity(tx, models.AutomationEntityTicket, id); err != nil {
			return err
		}
		return tx.Delete(&models.Ticket{}, id).Error
	})
}

func (r *ticketRepository) List(offset, limit int) ([]models.Ticket, error) {
	var tickets []models.Ticket
	err := orderByID(r.db.Preload("Customer").Preload("AssignedTo"), "tickets").Offset(offset).Limit(limit).Find(&tickets).Error
	return tickets, err
}

func (r *ticketRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.Ticket{}).Count(&count).Error
	return count, err
}

func (r *ticketRepository) CountByCustomerID(customerID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Ticket{}).Where("customer_id = ?", customerID).Count(&count).Error
	return count, err
}

func (r *ticketRepository) CountByAssignedToID(assignedToID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Ticket{}).Where("assigned_to_id = ?", assignedToID).Count(&count).Error
	return count, err
}

func (r *ticketRepository) CountOpen() (int64, error) {
	var count int64
	err := r.db.Model(&models.Ticket{}).Where("status IN ?", []string{"open", "in_progress"}).Count(&count).Error
	return count, err
}

func (r *ticketRepository) ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Ticket, error) {
	var tickets []models.Ticket
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	query, err := orderPage(query, "tickets", sortBy, sortOrder)
	if err != nil {
		return nil, err
	}
	err = query.Offset(offset).Limit(limit).Find(&tickets).Error
	return tickets, err
}

func (r *ticketRepository) Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Ticket, error) {
	var tickets []models.Ticket
	db := r.db
	for _, preload := range preloads {
		db = db.Preload(preload)
	}
	searchPattern := "%" + query + "%"
	db = db.Where(
		"title LIKE ? OR description LIKE ? OR resolution LIKE ?",
		searchPattern, searchPattern, searchPattern,
	)
	db, err := orderPage(db, "tickets", sortBy, sortOrder)
	if err != nil {
		return nil, err
	}
	err = db.Offset(offset).Limit(limit).Find(&tickets).Error
	return tickets, err
}

func (r *ticketRepository) CountSearch(query string) (int64, error) {
	var count int64
	searchPattern := "%" + query + "%"
	err := r.db.Model(&models.Ticket{}).Where(
		"title LIKE ? OR description LIKE ? OR resolution LIKE ?",
		searchPattern, searchPattern, searchPattern,
	).Count(&count).Error
	return count, err
}

// whereAssigneeScope applies the scope predicate. The search alternatives are
// parenthesised so the OR can never escape the assignee and customer filters.
func whereAssigneeScope(db *gorm.DB, scope TicketAssigneeScope) *gorm.DB {
	db = db.Where("assigned_to_id = ?", scope.AssignedToID)
	if scope.CustomerID != nil {
		db = db.Where("customer_id = ?", *scope.CustomerID)
	}
	if scope.Search != "" {
		searchPattern := "%" + scope.Search + "%"
		db = db.Where(
			"(title LIKE ? OR description LIKE ? OR resolution LIKE ?)",
			searchPattern, searchPattern, searchPattern,
		)
	}
	return db
}

// ListByAssigneeScope returns one page of the tickets matching scope. The
// filter is part of the query, so it applies before offset and limit; the id
// breaks ties in the sort direction so that consecutive pages neither repeat
// nor skip a row (see orderPage).
func (r *ticketRepository) ListByAssigneeScope(scope TicketAssigneeScope, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Ticket, error) {
	tickets := []models.Ticket{}
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	query = whereAssigneeScope(query, scope)
	query, err := orderPage(query, "tickets", sortBy, sortOrder)
	if err != nil {
		return nil, err
	}
	err = query.Offset(offset).Limit(limit).Find(&tickets).Error
	return tickets, err
}

// CountByAssigneeScope counts every ticket matching scope, with the same
// predicate as ListByAssigneeScope and before any pagination.
func (r *ticketRepository) CountByAssigneeScope(scope TicketAssigneeScope) (int64, error) {
	var count int64
	err := whereAssigneeScope(r.db.Model(&models.Ticket{}), scope).Count(&count).Error
	return count, err
}

// whereTicketListFilter is the one predicate of ListByFilter and CountByFilter.
// It ANDs the optional assignee, status and priority with the parenthesised
// search alternatives, so the search OR can never let a row with another
// assignee, status or priority through. Columns are qualified with the table
// name and every value is bound.
func whereTicketListFilter(db *gorm.DB, filter TicketListFilter) *gorm.DB {
	if filter.AssigneeID != nil {
		db = db.Where("tickets.assigned_to_id = ?", *filter.AssigneeID)
	}
	if filter.Status != "" {
		db = db.Where("tickets.status = ?", filter.Status)
	}
	if filter.Priority != "" {
		db = db.Where("tickets.priority = ?", filter.Priority)
	}
	if filter.Search != "" {
		searchPattern := "%" + filter.Search + "%"
		db = db.Where(
			"(tickets.title LIKE ? OR tickets.description LIKE ? OR tickets.resolution LIKE ?)",
			searchPattern, searchPattern, searchPattern,
		)
	}
	return db
}

// ListByFilter returns one page of the tickets matching filter. The filter is
// applied before offset and limit, and the rows walk orderPage's total order:
// the validated sort column, then id in the same direction, or id ascending.
func (r *ticketRepository) ListByFilter(filter TicketListFilter, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Ticket, error) {
	tickets := []models.Ticket{}
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	query = whereTicketListFilter(query, filter)
	query, err := orderPage(query, "tickets", sortBy, sortOrder)
	if err != nil {
		return nil, err
	}
	err = query.Offset(offset).Limit(limit).Find(&tickets).Error
	return tickets, err
}

// CountByFilter counts every live ticket matching filter, with the predicate
// of ListByFilter and before any pagination.
func (r *ticketRepository) CountByFilter(filter TicketListFilter) (int64, error) {
	var count int64
	err := whereTicketListFilter(r.db.Model(&models.Ticket{}), filter).Count(&count).Error
	return count, err
}

// CountByPriority returns the number of live tickets per priority. Priorities
// with no rows are absent from the map; the caller supplies the full label set.
func (r *ticketRepository) CountByPriority() (map[string]int64, error) {
	return countGroupedByColumn(r.db.Model(&models.Ticket{}), "priority")
}

// ListRecent returns the newest tickets first, for the dashboard widget. A nil
// assignedToID means every ticket, assigned or not; a non-nil one narrows to
// the tickets assigned to that user, which is what the support role gets. The
// filter is part of the query, so it applies before the limit.
func (r *ticketRepository) ListRecent(assignedToID *uint, limit int) ([]models.Ticket, error) {
	tickets := []models.Ticket{}
	query := r.db.Preload("Customer").Preload("AssignedTo")
	if assignedToID != nil {
		query = query.Where("assigned_to_id = ?", *assignedToID)
	}
	err := query.Order("created_at DESC, id DESC").
		Limit(limit).
		Find(&tickets).Error
	return tickets, err
}

// ListRecentlyResolved returns tickets that have left the open states, most
// recently touched first.
//
// Both "resolved" and "closed" count as resolutions, mirroring CountOpen, which
// treats open and in_progress as the open half. There is no resolved_at column,
// so updated_at stands in for the moment of resolution. A nil assignedToID
// means every ticket; a non-nil one narrows to that assignee before the limit.
func (r *ticketRepository) ListRecentlyResolved(assignedToID *uint, limit int) ([]models.Ticket, error) {
	tickets := []models.Ticket{}
	query := r.db.Preload("Customer").Preload("AssignedTo").
		Where("status IN ?", []string{string(models.TicketStatusResolved), string(models.TicketStatusClosed)})
	if assignedToID != nil {
		query = query.Where("assigned_to_id = ?", *assignedToID)
	}
	err := query.Order("updated_at DESC, id DESC").
		Limit(limit).
		Find(&tickets).Error
	return tickets, err
}

func (r *ticketRepository) CreateComment(comment *models.TicketComment) error {
	return r.db.Create(comment).Error
}

// CreateCommentWithFirstResponse inserts a staff reply and, in the same
// transaction, stamps the ticket's first_responded_at with the comment's
// created_at when it is still NULL. A ticket that already carries a stamp
// keeps it. Through WithTx the pair runs under a savepoint of the caller's
// transaction, so a caller that rolls back keeps neither.
func (r *ticketRepository) CreateCommentWithFirstResponse(comment *models.TicketComment) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(comment).Error; err != nil {
			return err
		}
		return tx.Exec(
			"UPDATE tickets SET first_responded_at = ? WHERE id = ? AND first_responded_at IS NULL AND deleted_at IS NULL",
			comment.CreatedAt, comment.TicketID,
		).Error
	})
}

func (r *ticketRepository) ListComments(ticketID uint, includeInternal bool) ([]models.TicketComment, error) {
	var comments []models.TicketComment
	query := r.db.Where("ticket_id = ?", ticketID)
	if !includeInternal {
		query = query.Where("is_internal = ?", false)
	}
	err := query.Preload("Author").Order("created_at ASC, id ASC").Find(&comments).Error
	return comments, err
}

func (r *ticketRepository) WithTx(tx *gorm.DB) TicketRepository {
	return &ticketRepository{db: tx}
}
