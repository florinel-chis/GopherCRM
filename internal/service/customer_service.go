package service

import (
	"errors"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type customerService struct {
	customerRepo repository.CustomerRepository
	userRepo     repository.UserRepository
}

func NewCustomerService(customerRepo repository.CustomerRepository, userRepo repository.UserRepository) CustomerService {
	return &customerService{
		customerRepo: customerRepo,
		userRepo:     userRepo,
	}
}

func (s *customerService) Create(customer *models.Customer) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("customer_email", customer.Email), "CustomerService", "Create")
	
	// Check for duplicate email
	existing, err := s.customerRepo.GetByEmail(customer.Email)
	if err == nil && existing != nil {
		logger.Warn("Attempted to create customer with duplicate email")
		return errors.New("customer with this email already exists")
	}
	
	if err := s.customerRepo.Create(customer); err != nil {
		logger.WithError(err).Error("Failed to create customer")
		return err
	}
	
	logger.WithField("customer_id", customer.ID).Info("Customer created successfully")
	return nil
}

func (s *customerService) GetByID(id uint) (*models.Customer, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("customer_id", id), "CustomerService", "GetByID")
	
	customer, err := s.customerRepo.GetByID(id)
	if err != nil {
		logger.WithError(err).Warn("Customer not found")
		return nil, err
	}
	
	logger.Debug("Customer retrieved successfully")
	return customer, nil
}

func (s *customerService) Update(customer *models.Customer) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("customer_id", customer.ID), "CustomerService", "Update")
	
	// Check for duplicate email if email is being updated
	if customer.Email != "" {
		existing, err := s.customerRepo.GetByEmail(customer.Email)
		if err == nil && existing != nil && existing.ID != customer.ID {
			logger.Warn("Attempted to update customer with duplicate email")
			return errors.New("customer with this email already exists")
		}
	}
	
	if err := s.customerRepo.Update(customer); err != nil {
		logger.WithError(err).Error("Failed to update customer")
		return err
	}
	
	logger.Info("Customer updated successfully")
	return nil
}

func (s *customerService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("customer_id", id), "CustomerService", "Delete")
	
	// Check if customer exists
	_, err := s.customerRepo.GetByID(id)
	if err != nil {
		logger.WithError(err).Warn("Customer not found")
		return err
	}
	
	if err := s.customerRepo.Delete(id); err != nil {
		logger.WithError(err).Error("Failed to delete customer")
		return err
	}
	
	logger.Info("Customer deleted successfully")
	return nil
}

func (s *customerService) List(offset, limit int) ([]models.Customer, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "CustomerService", "List")
	
	customers, err := s.customerRepo.List(offset, limit)
	if err != nil {
		logger.WithError(err).Error("Failed to list customers")
		return nil, 0, err
	}
	
	total, err := s.customerRepo.Count()
	if err != nil {
		logger.WithError(err).Error("Failed to count customers")
		return nil, 0, err
	}
	
	logger.WithField("total", total).Info("Customers listed successfully")
	return customers, total, nil
}