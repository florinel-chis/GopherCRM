package service

import (
	"context"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type leadService struct {
	leadRepo     repository.LeadRepository
	customerRepo repository.CustomerRepository
	txManager    *utils.TransactionManager
}

func NewLeadService(leadRepo repository.LeadRepository, customerRepo repository.CustomerRepository, txManager *utils.TransactionManager) LeadService {
	return &leadService{
		leadRepo:     leadRepo,
		customerRepo: customerRepo,
		txManager:    txManager,
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
	// For individual lead retrieval, preload Owner for display purposes
	return s.leadRepo.GetByIDWithPreloads(id, "Owner")
}

func (s *leadService) GetByExternalID(externalID string) (*models.Lead, error) {
	return s.leadRepo.GetByExternalID(externalID)
}

func (s *leadService) GetByOwner(ownerID uint, offset, limit int) ([]models.Lead, error) {
	// For owner-filtered lists, no need to preload Owner since we already know it
	return s.leadRepo.GetByOwnerID(ownerID, offset, limit)
}

func (s *leadService) GetByClassification(classification models.LeadClassification, offset, limit int) ([]models.Lead, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"classification": classification,
		"offset":         offset,
		"limit":          limit,
	}), "LeadService", "GetByClassification")

	leads, err := s.leadRepo.GetByClassification(classification, offset, limit)
	if err != nil {
		logger.WithError(err).Error("Failed to list leads by classification")
		return nil, 0, err
	}

	total, err := s.leadRepo.CountByClassification(classification)
	if err != nil {
		logger.WithError(err).Error("Failed to count leads by classification")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Leads listed by classification successfully")
	return leads, total, nil
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
	if classification, ok := updates["classification"].(models.LeadClassification); ok {
		lead.Classification = classification
	}
	if externalID, ok := updates["external_id"].(string); ok {
		lead.ExternalID = externalID
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
	
	// For general lists, preload Owner for display purposes
	leads, err := s.leadRepo.ListWithPreloads(offset, limit, "Owner")
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

func (s *leadService) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset":     offset,
		"limit":      limit,
		"sort_by":    sortBy,
		"sort_order": sortOrder,
	}), "LeadService", "ListSorted")

	leads, err := s.leadRepo.ListSortedWithPreloads(offset, limit, sortBy, sortOrder, "Owner")
	if err != nil {
		logger.WithError(err).Error("Failed to list leads sorted")
		return nil, 0, err
	}

	total, err := s.leadRepo.Count()
	if err != nil {
		logger.WithError(err).Error("Failed to count leads")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Leads listed sorted successfully")
	return leads, total, nil
}

func (s *leadService) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"query":  query,
		"offset": offset,
		"limit":  limit,
	}), "LeadService", "Search")

	leads, err := s.leadRepo.Search(query, offset, limit, sortBy, sortOrder, "Owner")
	if err != nil {
		logger.WithError(err).Error("Failed to search leads")
		return nil, 0, err
	}

	total, err := s.leadRepo.CountSearch(query)
	if err != nil {
		logger.WithError(err).Error("Failed to count search results")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Lead search completed")
	return leads, total, nil
}

func (s *leadService) ConvertToCustomer(leadID uint, customerData *models.Customer) (*models.Customer, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("lead_id", leadID), "LeadService", "ConvertToCustomer")
	
	// First, validate the lead outside of transaction
	lead, err := s.leadRepo.GetByID(leadID)
	if err != nil {
		logger.WithError(err).Error("Lead not found")
		return nil, err
	}

	if lead.Status == models.LeadStatusConverted {
		logger.Warn("Attempted to convert already converted lead")
		return nil, apperrors.NewLeadAlreadyConverted(leadID)
	}

	// Execute conversion within a transaction with retry logic
	ctx := context.Background()
	var convertedCustomer *models.Customer
	
	err = s.txManager.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
		// Get transaction from context
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}

		// Use transaction-aware repositories
		txLeadRepo := s.leadRepo.WithTx(tx)
		txCustomerRepo := s.customerRepo.WithTx(tx)

		// Re-check lead status within transaction to prevent race conditions
		txLead, err := txLeadRepo.GetByID(leadID)
		if err != nil {
			return err
		}

		if txLead.Status == models.LeadStatusConverted {
			return apperrors.NewLeadAlreadyConverted(leadID)
		}

		// Prepare customer data from lead data
		if customerData.Email == "" {
			customerData.Email = txLead.Email
		}
		if customerData.FirstName == "" {
			customerData.FirstName = txLead.FirstName
		}
		if customerData.LastName == "" {
			customerData.LastName = txLead.LastName
		}
		if customerData.Phone == "" {
			customerData.Phone = txLead.Phone
		}
		if customerData.Company == "" {
			customerData.Company = txLead.Company
		}

		// Create customer within transaction
		if err := txCustomerRepo.Create(customerData); err != nil {
			logger.WithError(err).Error("Failed to create customer within transaction")
			return err
		}

		// Update lead status within transaction
		if err := txLeadRepo.ConvertToCustomer(leadID, customerData.ID); err != nil {
			logger.WithError(err).Error("Failed to update lead status within transaction")
			return err
		}

		convertedCustomer = customerData
		return nil
	}, 3) // Retry up to 3 times for deadlocks

	if err != nil {
		logger.WithError(err).Error("Lead conversion transaction failed")
		return nil, err
	}

	logger.WithFields(map[string]interface{}{
		"customer_id": convertedCustomer.ID,
		"lead_email":  lead.Email,
	}).Info("Lead converted to customer successfully")
	
	return convertedCustomer, nil
}

func (s *leadService) GetCount() (int64, error) {
	return s.leadRepo.Count()
}

func (s *leadService) GetCountByClassification(classification models.LeadClassification) (int64, error) {
	return s.leadRepo.CountByClassification(classification)
}