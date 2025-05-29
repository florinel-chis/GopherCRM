package service

import (
	"errors"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type leadService struct {
	leadRepo     repository.LeadRepository
	customerRepo repository.CustomerRepository
}

func NewLeadService(leadRepo repository.LeadRepository, customerRepo repository.CustomerRepository) LeadService {
	return &leadService{
		leadRepo:     leadRepo,
		customerRepo: customerRepo,
	}
}

func (s *leadService) Create(lead *models.Lead) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("lead_email", lead.Email), "LeadService", "Create")
	
	// Set default status if not provided
	if lead.Status == "" {
		lead.Status = models.LeadStatusNew
	}
	
	err := s.leadRepo.Create(lead)
	if err != nil {
		logger.WithError(err).Error("Failed to create lead")
		return err
	}
	
	logger.WithField("lead_id", lead.ID).Info("Lead created successfully")
	return nil
}

func (s *leadService) GetByID(id uint) (*models.Lead, error) {
	return s.leadRepo.GetByID(id)
}

func (s *leadService) GetByOwner(ownerID uint, offset, limit int) ([]models.Lead, error) {
	return s.leadRepo.GetByOwnerID(ownerID, offset, limit)
}

func (s *leadService) Update(id uint, updates map[string]interface{}) (*models.Lead, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("lead_id", id), "LeadService", "Update")
	
	lead, err := s.leadRepo.GetByID(id)
	if err != nil {
		logger.WithError(err).Error("Lead not found")
		return nil, err
	}

	// Apply updates
	if firstName, ok := updates["first_name"].(string); ok {
		lead.FirstName = firstName
	}
	if lastName, ok := updates["last_name"].(string); ok {
		lead.LastName = lastName
	}
	if email, ok := updates["email"].(string); ok {
		lead.Email = email
	}
	if phone, ok := updates["phone"].(string); ok {
		lead.Phone = phone
	}
	if company, ok := updates["company"].(string); ok {
		lead.Company = company
	}
	if position, ok := updates["position"].(string); ok {
		lead.Position = position
	}
	if source, ok := updates["source"].(string); ok {
		lead.Source = source
	}
	if status, ok := updates["status"].(models.LeadStatus); ok {
		lead.Status = status
	}
	if notes, ok := updates["notes"].(string); ok {
		lead.Notes = notes
	}
	if ownerID, ok := updates["owner_id"].(uint); ok {
		lead.OwnerID = ownerID
	}

	if err := s.leadRepo.Update(lead); err != nil {
		logger.WithError(err).Error("Failed to update lead")
		return nil, err
	}

	logger.Info("Lead updated successfully")
	return lead, nil
}

func (s *leadService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("lead_id", id), "LeadService", "Delete")
	
	err := s.leadRepo.Delete(id)
	if err != nil {
		logger.WithError(err).Error("Failed to delete lead")
		return err
	}
	
	logger.Info("Lead deleted successfully")
	return nil
}

func (s *leadService) List(offset, limit int) ([]models.Lead, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "LeadService", "List")
	
	leads, err := s.leadRepo.List(offset, limit)
	if err != nil {
		logger.WithError(err).Error("Failed to list leads")
		return nil, 0, err
	}
	
	total, err := s.leadRepo.Count()
	if err != nil {
		logger.WithError(err).Error("Failed to count leads")
		return nil, 0, err
	}
	
	logger.WithField("total", total).Info("Leads listed successfully")
	return leads, total, nil
}

func (s *leadService) ConvertToCustomer(leadID uint, customerData *models.Customer) (*models.Customer, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("lead_id", leadID), "LeadService", "ConvertToCustomer")
	
	lead, err := s.leadRepo.GetByID(leadID)
	if err != nil {
		logger.WithError(err).Error("Lead not found")
		return nil, err
	}

	if lead.Status == models.LeadStatusConverted {
		logger.Warn("Attempted to convert already converted lead")
		return nil, errors.New("lead already converted")
	}

	// Create customer from lead data
	if customerData.Email == "" {
		customerData.Email = lead.Email
	}
	if customerData.FirstName == "" {
		customerData.FirstName = lead.FirstName
	}
	if customerData.LastName == "" {
		customerData.LastName = lead.LastName
	}
	if customerData.Phone == "" {
		customerData.Phone = lead.Phone
	}
	if customerData.Company == "" {
		customerData.Company = lead.Company
	}

	if err := s.customerRepo.Create(customerData); err != nil {
		logger.WithError(err).Error("Failed to create customer")
		return nil, err
	}

	// Update lead status
	if err := s.leadRepo.ConvertToCustomer(leadID, customerData.ID); err != nil {
		logger.WithError(err).Error("Failed to update lead status")
		return nil, err
	}

	logger.WithFields(map[string]interface{}{
		"customer_id": customerData.ID,
		"lead_email":  lead.Email,
	}).Info("Lead converted to customer successfully")
	
	return customerData, nil
}

func (s *leadService) GetCount() (int64, error) {
	return s.leadRepo.Count()
}