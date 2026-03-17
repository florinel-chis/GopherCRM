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
	err := r.db.Preload("Customer").Preload("AssignedTo").First(&ticket, id).Error
	if err != nil {
		return nil, err
	}
	return &ticket, nil
}

func (r *ticketRepository) GetByCustomerID(customerID uint, offset, limit int) ([]models.Ticket, error) {
	var tickets []models.Ticket
	err := r.db.Where("customer_id = ?", customerID).Offset(offset).Limit(limit).Find(&tickets).Error
	return tickets, err
}

func (r *ticketRepository) GetByAssignedToID(assignedToID uint, offset, limit int) ([]models.Ticket, error) {
	var tickets []models.Ticket
	err := r.db.Where("assigned_to_id = ?", assignedToID).Offset(offset).Limit(limit).Find(&tickets).Error
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
	err := r.db.Preload("Customer").Preload("AssignedTo").Offset(offset).Limit(limit).Find(&tickets).Error
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

func (r *ticketRepository) GetByIDWithPreloads(id uint, preloads ...string) (*models.Ticket, error) {
	var ticket models.Ticket
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	err := query.First(&ticket, id).Error
	if err != nil {
		return nil, err
	}
	return &ticket, nil
}

func (r *ticketRepository) GetByCustomerIDWithPreloads(customerID uint, offset, limit int, preloads ...string) ([]models.Ticket, error) {
	var tickets []models.Ticket
	query := r.db.Where("customer_id = ?", customerID)
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	err := query.Offset(offset).Limit(limit).Find(&tickets).Error
	return tickets, err
}

func (r *ticketRepository) GetByAssignedToIDWithPreloads(assignedToID uint, offset, limit int, preloads ...string) ([]models.Ticket, error) {
	var tickets []models.Ticket
	query := r.db.Where("assigned_to_id = ?", assignedToID)
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	err := query.Offset(offset).Limit(limit).Find(&tickets).Error
	return tickets, err
}

func (r *ticketRepository) ListWithPreloads(offset, limit int, preloads ...string) ([]models.Ticket, error) {
	var tickets []models.Ticket
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	err := query.Offset(offset).Limit(limit).Find(&tickets).Error
	return tickets, err
}

func (r *ticketRepository) ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Ticket, error) {
	var tickets []models.Ticket
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	if sortBy != "" {
		query = query.Order(sortBy + " " + sortOrder)
	}
	err := query.Offset(offset).Limit(limit).Find(&tickets).Error
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
	if sortBy != "" {
		db = db.Order(sortBy + " " + sortOrder)
	}
	err := db.Offset(offset).Limit(limit).Find(&tickets).Error
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

func (r *ticketRepository) WithTx(tx *gorm.DB) TicketRepository {
	return &ticketRepository{db: tx}
}