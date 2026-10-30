package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type ticketRepository struct {
	db *gorm.DB
}

func NewTicketRepository(db *gorm.DB) TicketRepository {
	return &ticketRepository{db: db}
}

func (r *ticketRepository) Create(ticket *models.Ticket) error {
	return r.db.Create(ticket).Error
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

func (r *ticketRepository) Update(ticket *models.Ticket) error {
	return r.db.Save(ticket).Error
}

func (r *ticketRepository) Delete(id uint) error {
	return r.db.Delete(&models.Ticket{}, id).Error
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
