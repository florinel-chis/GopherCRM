package service

import (
	"errors"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type ticketService struct {
	ticketRepo   repository.TicketRepository
	customerRepo repository.CustomerRepository
	userRepo     repository.UserRepository
}

func NewTicketService(ticketRepo repository.TicketRepository, customerRepo repository.CustomerRepository, userRepo repository.UserRepository) TicketService {
	return &ticketService{
		ticketRepo:   ticketRepo,
		customerRepo: customerRepo,
		userRepo:     userRepo,
	}
}

func (s *ticketService) Create(ticket *models.Ticket) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("ticket_title", ticket.Title), "TicketService", "Create")
	
	// Set default values
	if ticket.Status == "" {
		ticket.Status = models.TicketStatusOpen
	}
	if ticket.Priority == "" {
		ticket.Priority = models.TicketPriorityMedium
	}
	
	// Verify customer exists
	_, err := s.customerRepo.GetByID(ticket.CustomerID)
	if err != nil {
		logger.WithError(err).Warn("Customer not found")
		return errors.New("customer not found")
	}
	
	// Verify assignee exists if specified
	if ticket.AssignedToID != nil {
		assignee, err := s.userRepo.GetByID(*ticket.AssignedToID)
		if err != nil {
			logger.WithError(err).Warn("Assignee not found")
			return errors.New("assignee not found")
		}
		// Only support and admin users can be assigned tickets
		if assignee.Role != models.RoleSupport && assignee.Role != models.RoleAdmin {
			logger.Warn("Invalid assignee role")
			return errors.New("tickets can only be assigned to support or admin users")
		}
	}
	
	if err := s.ticketRepo.Create(ticket); err != nil {
		logger.WithError(err).Error("Failed to create ticket")
		return err
	}
	
	logger.WithField("ticket_id", ticket.ID).Info("Ticket created successfully")
	return nil
}

func (s *ticketService) GetByID(id uint) (*models.Ticket, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("ticket_id", id), "TicketService", "GetByID")
	
	ticket, err := s.ticketRepo.GetByID(id)
	if err != nil {
		logger.WithError(err).Warn("Ticket not found")
		return nil, err
	}
	
	logger.Debug("Ticket retrieved successfully")
	return ticket, nil
}

func (s *ticketService) GetByCustomer(customerID uint, offset, limit int) ([]models.Ticket, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"customer_id": customerID,
		"offset":      offset,
		"limit":       limit,
	}), "TicketService", "GetByCustomer")
	
	tickets, err := s.ticketRepo.GetByCustomerID(customerID, offset, limit)
	if err != nil {
		logger.WithError(err).Error("Failed to get tickets by customer")
		return nil, 0, err
	}
	
	total, err := s.ticketRepo.CountByCustomerID(customerID)
	if err != nil {
		logger.WithError(err).Error("Failed to count tickets by customer")
		return nil, 0, err
	}
	
	logger.WithField("count", len(tickets)).Info("Tickets retrieved by customer")
	return tickets, total, nil
}

func (s *ticketService) GetByAssignee(assigneeID uint, offset, limit int) ([]models.Ticket, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"assignee_id": assigneeID,
		"offset":      offset,
		"limit":       limit,
	}), "TicketService", "GetByAssignee")
	
	tickets, err := s.ticketRepo.GetByAssignedToID(assigneeID, offset, limit)
	if err != nil {
		logger.WithError(err).Error("Failed to get tickets by assignee")
		return nil, 0, err
	}
	
	total, err := s.ticketRepo.CountByAssignedToID(assigneeID)
	if err != nil {
		logger.WithError(err).Error("Failed to count tickets by assignee")
		return nil, 0, err
	}
	
	logger.WithField("count", len(tickets)).Info("Tickets retrieved by assignee")
	return tickets, total, nil
}

func (s *ticketService) Update(ticket *models.Ticket) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("ticket_id", ticket.ID), "TicketService", "Update")
	
	// Get existing ticket to validate state transitions
	existing, err := s.ticketRepo.GetByID(ticket.ID)
	if err != nil {
		logger.WithError(err).Warn("Ticket not found")
		return err
	}
	
	// Validate status transitions
	if existing.Status == models.TicketStatusClosed && ticket.Status != models.TicketStatusClosed {
		logger.Warn("Cannot reopen closed ticket")
		return errors.New("cannot reopen closed ticket")
	}
	
	// Verify assignee if changed
	if ticket.AssignedToID != nil && (existing.AssignedToID == nil || *ticket.AssignedToID != *existing.AssignedToID) {
		assignee, err := s.userRepo.GetByID(*ticket.AssignedToID)
		if err != nil {
			logger.WithError(err).Warn("Assignee not found")
			return errors.New("assignee not found")
		}
		if assignee.Role != models.RoleSupport && assignee.Role != models.RoleAdmin {
			logger.Warn("Invalid assignee role")
			return errors.New("tickets can only be assigned to support or admin users")
		}
	}
	
	if err := s.ticketRepo.Update(ticket); err != nil {
		logger.WithError(err).Error("Failed to update ticket")
		return err
	}
	
	logger.Info("Ticket updated successfully")
	return nil
}

func (s *ticketService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("ticket_id", id), "TicketService", "Delete")
	
	// Check if ticket exists
	_, err := s.ticketRepo.GetByID(id)
	if err != nil {
		logger.WithError(err).Warn("Ticket not found")
		return err
	}
	
	if err := s.ticketRepo.Delete(id); err != nil {
		logger.WithError(err).Error("Failed to delete ticket")
		return err
	}
	
	logger.Info("Ticket deleted successfully")
	return nil
}

func (s *ticketService) List(offset, limit int) ([]models.Ticket, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "TicketService", "List")
	
	tickets, err := s.ticketRepo.List(offset, limit)
	if err != nil {
		logger.WithError(err).Error("Failed to list tickets")
		return nil, 0, err
	}
	
	total, err := s.ticketRepo.Count()
	if err != nil {
		logger.WithError(err).Error("Failed to count tickets")
		return nil, 0, err
	}
	
	logger.WithField("total", total).Info("Tickets listed successfully")
	return tickets, total, nil
}