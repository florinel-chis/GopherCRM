package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

type bulkOperationService struct {
	bulkOperationRepo repository.BulkOperationRepository
	bulkRepo          repository.BulkRepository
	userRepo          repository.UserRepository
	leadRepo          repository.LeadRepository
	customerRepo      repository.CustomerRepository
	taskRepo          repository.TaskRepository
	ticketRepo        repository.TicketRepository
	transactionMgr    repository.TransactionManager
	logger            *logrus.Logger
}

func NewBulkOperationService(
	bulkOperationRepo repository.BulkOperationRepository,
	bulkRepo repository.BulkRepository,
	userRepo repository.UserRepository,
	leadRepo repository.LeadRepository,
	customerRepo repository.CustomerRepository,
	taskRepo repository.TaskRepository,
	ticketRepo repository.TicketRepository,
	transactionMgr repository.TransactionManager,
	logger *logrus.Logger,
) BulkOperationService {
	return &bulkOperationService{
		bulkOperationRepo: bulkOperationRepo,
		bulkRepo:          bulkRepo,
		userRepo:          userRepo,
		leadRepo:          leadRepo,
		customerRepo:      customerRepo,
		taskRepo:          taskRepo,
		ticketRepo:        ticketRepo,
		transactionMgr:    transactionMgr,
		logger:            logger,
	}
}

// Bulk operation management
func (s *bulkOperationService) CreateBulkOperation(userID uint, resourceType string, operationType models.BulkOperationType, totalItems int) (*models.BulkOperation, error) {
	operation := &models.BulkOperation{
		UserID:        userID,
		ResourceType:  resourceType,
		OperationType: operationType,
		Status:        models.StatusPending,
		TotalItems:    totalItems,
	}

	err := s.bulkOperationRepo.Create(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to create bulk operation: %w", err)
	}

	return operation, nil
}

func (s *bulkOperationService) GetBulkOperation(id uint) (*models.BulkOperation, error) {
	return s.bulkOperationRepo.GetByID(id)
}

func (s *bulkOperationService) GetBulkOperationWithItems(id uint) (*models.BulkOperation, error) {
	return s.bulkOperationRepo.GetByIDWithItems(id)
}

func (s *bulkOperationService) GetUserBulkOperations(userID uint, offset, limit int) ([]models.BulkOperation, error) {
	return s.bulkOperationRepo.GetByUserID(userID, offset, limit)
}

func (s *bulkOperationService) UpdateBulkOperationStatus(id uint, status models.BulkOperationStatus) error {
	return s.bulkOperationRepo.UpdateStatus(id, status)
}

// Generic bulk operations
func (s *bulkOperationService) ProcessBulkCreate(userID uint, resourceType string, request *models.BulkCreateRequest) (*models.BulkResponse, error) {
	switch resourceType {
	case "users":
		return s.BulkCreateUsers(request, userID)
	case "leads":
		return s.BulkCreateLeads(request, userID)
	case "customers":
		return s.BulkCreateCustomers(request, userID)
	case "tasks":
		return s.BulkCreateTasks(request, userID)
	case "tickets":
		return s.BulkCreateTickets(request, userID)
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
}

func (s *bulkOperationService) ProcessBulkUpdate(userID uint, resourceType string, request *models.BulkUpdateRequest) (*models.BulkResponse, error) {
	switch resourceType {
	case "users":
		return s.BulkUpdateUsers(request, userID)
	case "leads":
		return s.BulkUpdateLeads(request, userID)
	case "customers":
		return s.BulkUpdateCustomers(request, userID)
	case "tasks":
		return s.BulkUpdateTasks(request, userID)
	case "tickets":
		return s.BulkUpdateTickets(request, userID)
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
}

func (s *bulkOperationService) ProcessBulkDelete(userID uint, resourceType string, request *models.BulkDeleteRequest) (*models.BulkResponse, error) {
	switch resourceType {
	case "users":
		return s.BulkDeleteUsers(request, userID)
	case "leads":
		return s.BulkDeleteLeads(request, userID)
	case "customers":
		return s.BulkDeleteCustomers(request, userID)
	case "tasks":
		return s.BulkDeleteTasks(request, userID)
	case "tickets":
		return s.BulkDeleteTickets(request, userID)
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
}

func (s *bulkOperationService) ProcessBulkAction(userID uint, resourceType string, request *models.BulkActionRequest) (*models.BulkResponse, error) {
	switch resourceType {
	case "users":
		return s.BulkActionUsers(request, userID)
	case "leads":
		return s.BulkActionLeads(request, userID)
	case "customers":
		return s.BulkActionCustomers(request, userID)
	case "tasks":
		return s.BulkActionTasks(request, userID)
	case "tickets":
		return s.BulkActionTickets(request, userID)
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
}

// User bulk operations
func (s *bulkOperationService) BulkCreateUsers(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.Items)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "users", models.BulkCreate, len(request.Items))
	if err != nil {
		return nil, err
	}

	// Convert request items to User models
	users := make([]models.User, 0, len(request.Items))
	var conversionErrors []models.BulkItemError

	for i, item := range request.Items {
		user := models.User{}
		if err := s.convertMapToModel(item, &user); err != nil {
			conversionErrors = append(conversionErrors, models.BulkItemError{
				Index:   i,
				Message: fmt.Sprintf("Invalid user data: %v", err),
				Code:    "VALIDATION_ERROR",
			})
			continue
		}

		// Set defaults
		user.IsActive = true
		if user.Role == "" {
			user.Role = models.RoleCustomer
		}

		users = append(users, user)
	}

	if len(conversionErrors) > 0 {
		return &models.BulkResponse{
			OperationID:    operation.ID,
			Status:         models.StatusFailed,
			TotalItems:     len(request.Items),
			ProcessedItems: 0,
			SuccessItems:   0,
			FailedItems:    len(conversionErrors),
			Errors:         conversionErrors,
		}, nil
	}

	// Perform bulk create with transaction
	var results []models.User
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		results, operationErrors = txRepo.BulkCreateUsers(users)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	response := s.buildBulkResponse(operation.ID, len(request.Items), len(results), operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if len(results) == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkUpdateUsers(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.Items)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "users", models.BulkUpdate, len(request.Items))
	if err != nil {
		return nil, err
	}

	// Perform bulk update with transaction
	var results []models.User
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		results, operationErrors = txRepo.BulkUpdateUsers(request.Items)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	response := s.buildBulkResponse(operation.ID, len(request.Items), len(results), operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if len(results) == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkDeleteUsers(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.IDs)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "users", models.BulkDelete, len(request.IDs))
	if err != nil {
		return nil, err
	}

	// Prevent self-deletion
	filteredIDs := make([]uint, 0, len(request.IDs))
	for _, id := range request.IDs {
		if id != currentUserID {
			filteredIDs = append(filteredIDs, id)
		}
	}

	// Perform bulk delete with transaction
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		operationErrors = txRepo.BulkDeleteUsers(filteredIDs)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	successCount := len(filteredIDs) - len(operationErrors)
	response := s.buildBulkDeleteResponse(operation.ID, len(request.IDs), successCount, operationErrors, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if successCount == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkActionUsers(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()

	// Validate request
	if err := s.validateBulkRequest(len(request.IDs)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "users", models.BulkAction, len(request.IDs))
	if err != nil {
		return nil, err
	}

	// Process bulk action based on action type
	switch request.Action {
	case models.UserBulkActionTypes.Activate:
		return s.bulkActivateUsers(operation.ID, request.IDs, true, startTime)
	case models.UserBulkActionTypes.Deactivate:
		return s.bulkActivateUsers(operation.ID, request.IDs, false, startTime)
	case models.UserBulkActionTypes.ChangeRole:
		return s.bulkChangeUserRole(operation.ID, request.IDs, request.Parameters, startTime)
	default:
		return nil, fmt.Errorf("unsupported user action: %s", request.Action)
	}
}

// Helper methods for user actions
func (s *bulkOperationService) bulkActivateUsers(operationID uint, userIDs []uint, isActive bool, startTime time.Time) (*models.BulkResponse, error) {
	updates := make([]models.BulkUpdateItem, len(userIDs))
	for i, id := range userIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"is_active": isActive},
		}
	}

	// Perform bulk update with transaction
	var results []models.User
	var operationErrors []error

	err := s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		results, operationErrors = txRepo.BulkUpdateUsers(updates)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	response := s.buildBulkResponse(operationID, len(userIDs), len(results), operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if len(results) == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operationID, status)

	return response, nil
}

func (s *bulkOperationService) bulkChangeUserRole(operationID uint, userIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	newRole, ok := parameters["role"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'role' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(userIDs))
	for i, id := range userIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"role": newRole},
		}
	}

	// Perform bulk update with transaction
	var results []models.User
	var operationErrors []error

	err := s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		results, operationErrors = txRepo.BulkUpdateUsers(updates)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	response := s.buildBulkResponse(operationID, len(userIDs), len(results), operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if len(results) == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operationID, status)

	return response, nil
}

// Placeholder implementations for other resource types (similar pattern)
func (s *bulkOperationService) BulkCreateLeads(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.Items)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "leads", models.BulkCreate, len(request.Items))
	if err != nil {
		return nil, err
	}

	// Convert request items to Lead models
	leads := make([]models.Lead, 0, len(request.Items))
	var conversionErrors []models.BulkItemError

	for i, item := range request.Items {
		lead := models.Lead{}
		if err := s.convertMapToModel(item, &lead); err != nil {
			conversionErrors = append(conversionErrors, models.BulkItemError{
				Index:   i,
				Message: fmt.Sprintf("Invalid lead data: %v", err),
				Code:    "VALIDATION_ERROR",
			})
			continue
		}

		// Set defaults and owner
		if lead.Status == "" {
			lead.Status = models.LeadStatusNew
		}
		if lead.OwnerID == 0 {
			lead.OwnerID = currentUserID
		}

		leads = append(leads, lead)
	}

	if len(conversionErrors) > 0 {
		return &models.BulkResponse{
			OperationID:    operation.ID,
			Status:         models.StatusFailed,
			TotalItems:     len(request.Items),
			ProcessedItems: 0,
			SuccessItems:   0,
			FailedItems:    len(conversionErrors),
			Errors:         conversionErrors,
		}, nil
	}

	// Perform bulk create with transaction
	var results []models.Lead
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		results, operationErrors = txRepo.BulkCreateLeads(leads)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	response := s.buildBulkResponse(operation.ID, len(request.Items), len(results), operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if len(results) == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkUpdateLeads(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.Items)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "leads", models.BulkUpdate, len(request.Items))
	if err != nil {
		return nil, err
	}

	// Perform bulk update with transaction
	var results []models.Lead
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		results, operationErrors = txRepo.BulkUpdateLeads(request.Items)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	response := s.buildBulkResponse(operation.ID, len(request.Items), len(results), operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if len(results) == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkDeleteLeads(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.IDs)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "leads", models.BulkDelete, len(request.IDs))
	if err != nil {
		return nil, err
	}

	// Perform bulk delete with transaction
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		operationErrors = txRepo.BulkDeleteLeads(request.IDs)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	successCount := len(request.IDs) - len(operationErrors)
	response := s.buildBulkDeleteResponse(operation.ID, len(request.IDs), successCount, operationErrors, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if successCount == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkActionLeads(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()

	// Validate request
	if err := s.validateBulkRequest(len(request.IDs)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "leads", models.BulkAction, len(request.IDs))
	if err != nil {
		return nil, err
	}

	// Process bulk action based on action type
	switch request.Action {
	case models.LeadBulkActionTypes.UpdateStatus:
		return s.bulkUpdateLeadStatus(operation.ID, request.IDs, request.Parameters, startTime)
	case models.LeadBulkActionTypes.Assign:
		return s.bulkAssignLeads(operation.ID, request.IDs, request.Parameters, startTime)
	case models.LeadBulkActionTypes.Convert:
		return s.bulkConvertLeads(operation.ID, request.IDs, request.Parameters, startTime)
	case models.LeadBulkActionTypes.UpdateSource:
		return s.bulkUpdateLeadSource(operation.ID, request.IDs, request.Parameters, startTime)
	default:
		return nil, fmt.Errorf("unsupported lead action: %s", request.Action)
	}
}

func (s *bulkOperationService) BulkCreateCustomers(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.Items)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "customers", models.BulkCreate, len(request.Items))
	if err != nil {
		return nil, err
	}

	// Convert request items to Customer models
	customers := make([]models.Customer, 0, len(request.Items))
	var conversionErrors []models.BulkItemError

	for i, item := range request.Items {
		customer := models.Customer{}
		if err := s.convertMapToModel(item, &customer); err != nil {
			conversionErrors = append(conversionErrors, models.BulkItemError{
				Index:   i,
				Message: fmt.Sprintf("Invalid customer data: %v", err),
				Code:    "VALIDATION_ERROR",
			})
			continue
		}

		// Set defaults - customer doesn't have type field in current model
		// customer.IsActive = true // Customer doesn't have IsActive field

		customers = append(customers, customer)
	}

	if len(conversionErrors) > 0 {
		return &models.BulkResponse{
			OperationID:    operation.ID,
			Status:         models.StatusFailed,
			TotalItems:     len(request.Items),
			ProcessedItems: 0,
			SuccessItems:   0,
			FailedItems:    len(conversionErrors),
			Errors:         conversionErrors,
		}, nil
	}

	// Perform bulk create with transaction
	var results []models.Customer
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		results, operationErrors = txRepo.BulkCreateCustomers(customers)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	response := s.buildBulkResponse(operation.ID, len(request.Items), len(results), operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if len(results) == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkUpdateCustomers(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.Items)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "customers", models.BulkUpdate, len(request.Items))
	if err != nil {
		return nil, err
	}

	// Perform bulk update with transaction
	var results []models.Customer
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		results, operationErrors = txRepo.BulkUpdateCustomers(request.Items)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	response := s.buildBulkResponse(operation.ID, len(request.Items), len(results), operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if len(results) == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkDeleteCustomers(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.IDs)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "customers", models.BulkDelete, len(request.IDs))
	if err != nil {
		return nil, err
	}

	// Perform bulk delete with transaction
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		operationErrors = txRepo.BulkDeleteCustomers(request.IDs)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	successCount := len(request.IDs) - len(operationErrors)
	response := s.buildBulkDeleteResponse(operation.ID, len(request.IDs), successCount, operationErrors, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if successCount == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkActionCustomers(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()

	// Validate request
	if err := s.validateBulkRequest(len(request.IDs)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "customers", models.BulkAction, len(request.IDs))
	if err != nil {
		return nil, err
	}

	// Process bulk action based on action type
	switch request.Action {
	case models.CustomerBulkActionTypes.Activate:
		return s.bulkActivateCustomers(operation.ID, request.IDs, true, startTime)
	case models.CustomerBulkActionTypes.Deactivate:
		return s.bulkActivateCustomers(operation.ID, request.IDs, false, startTime)
	case models.CustomerBulkActionTypes.UpdateType:
		return s.bulkUpdateCustomerType(operation.ID, request.IDs, request.Parameters, startTime)
	case models.CustomerBulkActionTypes.Assign:
		return s.bulkAssignCustomers(operation.ID, request.IDs, request.Parameters, startTime)
	default:
		return nil, fmt.Errorf("unsupported customer action: %s", request.Action)
	}
}

func (s *bulkOperationService) BulkCreateTasks(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.Items)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "tasks", models.BulkCreate, len(request.Items))
	if err != nil {
		return nil, err
	}

	// Convert request items to Task models
	tasks := make([]models.Task, 0, len(request.Items))
	var conversionErrors []models.BulkItemError

	for i, item := range request.Items {
		task := models.Task{}
		if err := s.convertMapToModel(item, &task); err != nil {
			conversionErrors = append(conversionErrors, models.BulkItemError{
				Index:   i,
				Message: fmt.Sprintf("Invalid task data: %v", err),
				Code:    "VALIDATION_ERROR",
			})
			continue
		}

		// Set defaults
		if task.Status == "" {
			task.Status = models.TaskStatusPending
		}
		if task.Priority == "" {
			task.Priority = models.TaskPriorityMedium
		}
		if task.AssignedToID == 0 {
			task.AssignedToID = currentUserID
		}
		// Task doesn't have CreatedByID field in current model

		tasks = append(tasks, task)
	}

	if len(conversionErrors) > 0 {
		return &models.BulkResponse{
			OperationID:    operation.ID,
			Status:         models.StatusFailed,
			TotalItems:     len(request.Items),
			ProcessedItems: 0,
			SuccessItems:   0,
			FailedItems:    len(conversionErrors),
			Errors:         conversionErrors,
		}, nil
	}

	// Perform bulk create with transaction
	var results []models.Task
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		results, operationErrors = txRepo.BulkCreateTasks(tasks)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	response := s.buildBulkResponse(operation.ID, len(request.Items), len(results), operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if len(results) == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkUpdateTasks(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.Items)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "tasks", models.BulkUpdate, len(request.Items))
	if err != nil {
		return nil, err
	}

	// Perform bulk update with transaction
	var results []models.Task
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		results, operationErrors = txRepo.BulkUpdateTasks(request.Items)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	response := s.buildBulkResponse(operation.ID, len(request.Items), len(results), operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if len(results) == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkDeleteTasks(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.IDs)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "tasks", models.BulkDelete, len(request.IDs))
	if err != nil {
		return nil, err
	}

	// Perform bulk delete with transaction
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		operationErrors = txRepo.BulkDeleteTasks(request.IDs)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	successCount := len(request.IDs) - len(operationErrors)
	response := s.buildBulkDeleteResponse(operation.ID, len(request.IDs), successCount, operationErrors, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if successCount == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkActionTasks(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()

	// Validate request
	if err := s.validateBulkRequest(len(request.IDs)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "tasks", models.BulkAction, len(request.IDs))
	if err != nil {
		return nil, err
	}

	// Process bulk action based on action type
	switch request.Action {
	case models.TaskBulkActionTypes.UpdateStatus:
		return s.bulkUpdateTaskStatus(operation.ID, request.IDs, request.Parameters, startTime)
	case models.TaskBulkActionTypes.Assign:
		return s.bulkAssignTasks(operation.ID, request.IDs, request.Parameters, startTime)
	case models.TaskBulkActionTypes.UpdatePriority:
		return s.bulkUpdateTaskPriority(operation.ID, request.IDs, request.Parameters, startTime)
	case models.TaskBulkActionTypes.SetDueDate:
		return s.bulkSetTaskDueDate(operation.ID, request.IDs, request.Parameters, startTime)
	default:
		return nil, fmt.Errorf("unsupported task action: %s", request.Action)
	}
}

func (s *bulkOperationService) BulkCreateTickets(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.Items)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "tickets", models.BulkCreate, len(request.Items))
	if err != nil {
		return nil, err
	}

	// Convert request items to Ticket models
	tickets := make([]models.Ticket, 0, len(request.Items))
	var conversionErrors []models.BulkItemError

	for i, item := range request.Items {
		ticket := models.Ticket{}
		if err := s.convertMapToModel(item, &ticket); err != nil {
			conversionErrors = append(conversionErrors, models.BulkItemError{
				Index:   i,
				Message: fmt.Sprintf("Invalid ticket data: %v", err),
				Code:    "VALIDATION_ERROR",
			})
			continue
		}

		// Set defaults
		if ticket.Status == "" {
			ticket.Status = models.TicketStatusOpen
		}
		if ticket.Priority == "" {
			ticket.Priority = models.TicketPriorityMedium
		}
		// Category field not present in current ticket model
		if ticket.AssignedToID == nil {
			ticket.AssignedToID = &currentUserID
		}
		// Ticket doesn't have CreatedByID field in current model

		tickets = append(tickets, ticket)
	}

	if len(conversionErrors) > 0 {
		return &models.BulkResponse{
			OperationID:    operation.ID,
			Status:         models.StatusFailed,
			TotalItems:     len(request.Items),
			ProcessedItems: 0,
			SuccessItems:   0,
			FailedItems:    len(conversionErrors),
			Errors:         conversionErrors,
		}, nil
	}

	// Perform bulk create with transaction
	var results []models.Ticket
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		results, operationErrors = txRepo.BulkCreateTickets(tickets)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	response := s.buildBulkResponse(operation.ID, len(request.Items), len(results), operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if len(results) == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkUpdateTickets(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.Items)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "tickets", models.BulkUpdate, len(request.Items))
	if err != nil {
		return nil, err
	}

	// Perform bulk update with transaction
	var results []models.Ticket
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		results, operationErrors = txRepo.BulkUpdateTickets(request.Items)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	response := s.buildBulkResponse(operation.ID, len(request.Items), len(results), operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if len(results) == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkDeleteTickets(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()
	
	// Validate request
	if err := s.validateBulkRequest(len(request.IDs)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "tickets", models.BulkDelete, len(request.IDs))
	if err != nil {
		return nil, err
	}

	// Perform bulk delete with transaction
	var operationErrors []error

	err = s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		operationErrors = txRepo.BulkDeleteTickets(request.IDs)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Build response
	successCount := len(request.IDs) - len(operationErrors)
	response := s.buildBulkDeleteResponse(operation.ID, len(request.IDs), successCount, operationErrors, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if successCount == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operation.ID, status)

	return response, nil
}

func (s *bulkOperationService) BulkActionTickets(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error) {
	startTime := time.Now()

	// Validate request
	if err := s.validateBulkRequest(len(request.IDs)); err != nil {
		return nil, err
	}

	// Create bulk operation record
	operation, err := s.CreateBulkOperation(currentUserID, "tickets", models.BulkAction, len(request.IDs))
	if err != nil {
		return nil, err
	}

	// Process bulk action based on action type
	switch request.Action {
	case models.TicketBulkActionTypes.UpdateStatus:
		return s.bulkUpdateTicketStatus(operation.ID, request.IDs, request.Parameters, startTime)
	case models.TicketBulkActionTypes.Assign:
		return s.bulkAssignTickets(operation.ID, request.IDs, request.Parameters, startTime)
	case models.TicketBulkActionTypes.UpdatePriority:
		return s.bulkUpdateTicketPriority(operation.ID, request.IDs, request.Parameters, startTime)
	case models.TicketBulkActionTypes.UpdateCategory:
		return s.bulkUpdateTicketCategory(operation.ID, request.IDs, request.Parameters, startTime)
	default:
		return nil, fmt.Errorf("unsupported ticket action: %s", request.Action)
	}
}

// Helper methods
func (s *bulkOperationService) validateBulkRequest(itemCount int) error {
	if itemCount == 0 {
		return fmt.Errorf("no items provided for bulk operation")
	}
	if itemCount > models.MaxBulkItems {
		return fmt.Errorf("too many items: %d, maximum allowed: %d", itemCount, models.MaxBulkItems)
	}
	return nil
}

func (s *bulkOperationService) convertMapToModel(data map[string]interface{}, model interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(jsonData, model)
}

func (s *bulkOperationService) buildBulkResponse(operationID uint, totalItems, successItems int, errors []error, results interface{}, startTime time.Time) *models.BulkResponse {
	processingTime := time.Since(startTime)
	
	var bulkErrors []models.BulkItemError
	for i, err := range errors {
		bulkErrors = append(bulkErrors, models.BulkItemError{
			Index:   i,
			Message: err.Error(),
			Code:    "PROCESSING_ERROR",
		})
	}

	status := models.StatusCompleted
	if len(errors) > 0 {
		if successItems == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	return &models.BulkResponse{
		OperationID:    operationID,
		Status:         status,
		TotalItems:     totalItems,
		ProcessedItems: totalItems,
		SuccessItems:   successItems,
		FailedItems:    len(errors),
		ProcessingTime: &processingTime,
		Errors:         bulkErrors,
	}
}

func (s *bulkOperationService) buildBulkDeleteResponse(operationID uint, totalItems, successItems int, errors []error, startTime time.Time) *models.BulkResponse {
	processingTime := time.Since(startTime)
	
	var bulkErrors []models.BulkItemError
	for i, err := range errors {
		bulkErrors = append(bulkErrors, models.BulkItemError{
			Index:   i,
			Message: err.Error(),
			Code:    "PROCESSING_ERROR",
		})
	}

	status := models.StatusCompleted
	if len(errors) > 0 {
		if successItems == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	return &models.BulkResponse{
		OperationID:    operationID,
		Status:         status,
		TotalItems:     totalItems,
		ProcessedItems: totalItems,
		SuccessItems:   successItems,
		FailedItems:    len(errors),
		ProcessingTime: &processingTime,
		Errors:         bulkErrors,
	}
}

// Lead bulk action helpers
func (s *bulkOperationService) bulkUpdateLeadStatus(operationID uint, leadIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	newStatus, ok := parameters["status"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'status' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(leadIDs))
	for i, id := range leadIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"status": newStatus},
		}
	}

	return s.performBulkUpdateAction(operationID, "leads", updates, startTime)
}

func (s *bulkOperationService) bulkAssignLeads(operationID uint, leadIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	assigneeID, ok := parameters["owner_id"].(float64)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'owner_id' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(leadIDs))
	for i, id := range leadIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"owner_id": uint(assigneeID)},
		}
	}

	return s.performBulkUpdateAction(operationID, "leads", updates, startTime)
}

func (s *bulkOperationService) bulkConvertLeads(operationID uint, leadIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	// Convert leads to customers - this is a complex operation
	updates := make([]models.BulkUpdateItem, len(leadIDs))
	for i, id := range leadIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"status": models.LeadStatusConverted},
		}
	}

	return s.performBulkUpdateAction(operationID, "leads", updates, startTime)
}

func (s *bulkOperationService) bulkUpdateLeadSource(operationID uint, leadIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	newSource, ok := parameters["source"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'source' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(leadIDs))
	for i, id := range leadIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"source": newSource},
		}
	}

	return s.performBulkUpdateAction(operationID, "leads", updates, startTime)
}

// Customer bulk action helpers
func (s *bulkOperationService) bulkActivateCustomers(operationID uint, customerIDs []uint, isActive bool, startTime time.Time) (*models.BulkResponse, error) {
	updates := make([]models.BulkUpdateItem, len(customerIDs))
	for i, id := range customerIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"is_active": isActive},
		}
	}

	return s.performBulkUpdateAction(operationID, "customers", updates, startTime)
}

func (s *bulkOperationService) bulkUpdateCustomerType(operationID uint, customerIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	newType, ok := parameters["type"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'type' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(customerIDs))
	for i, id := range customerIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"type": newType},
		}
	}

	return s.performBulkUpdateAction(operationID, "customers", updates, startTime)
}

func (s *bulkOperationService) bulkAssignCustomers(operationID uint, customerIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	assigneeID, ok := parameters["assigned_to_id"].(float64)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'assigned_to_id' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(customerIDs))
	for i, id := range customerIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"assigned_to_id": uint(assigneeID)},
		}
	}

	return s.performBulkUpdateAction(operationID, "customers", updates, startTime)
}

// Task bulk action helpers
func (s *bulkOperationService) bulkUpdateTaskStatus(operationID uint, taskIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	newStatus, ok := parameters["status"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'status' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(taskIDs))
	for i, id := range taskIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"status": newStatus},
		}
	}

	return s.performBulkUpdateAction(operationID, "tasks", updates, startTime)
}

func (s *bulkOperationService) bulkAssignTasks(operationID uint, taskIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	assigneeID, ok := parameters["assigned_to_id"].(float64)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'assigned_to_id' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(taskIDs))
	for i, id := range taskIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"assigned_to_id": uint(assigneeID)},
		}
	}

	return s.performBulkUpdateAction(operationID, "tasks", updates, startTime)
}

func (s *bulkOperationService) bulkUpdateTaskPriority(operationID uint, taskIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	newPriority, ok := parameters["priority"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'priority' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(taskIDs))
	for i, id := range taskIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"priority": newPriority},
		}
	}

	return s.performBulkUpdateAction(operationID, "tasks", updates, startTime)
}

func (s *bulkOperationService) bulkSetTaskDueDate(operationID uint, taskIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	dueDateStr, ok := parameters["due_date"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'due_date' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(taskIDs))
	for i, id := range taskIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"due_date": dueDateStr},
		}
	}

	return s.performBulkUpdateAction(operationID, "tasks", updates, startTime)
}

// Ticket bulk action helpers
func (s *bulkOperationService) bulkUpdateTicketStatus(operationID uint, ticketIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	newStatus, ok := parameters["status"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'status' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(ticketIDs))
	for i, id := range ticketIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"status": newStatus},
		}
	}

	return s.performBulkUpdateAction(operationID, "tickets", updates, startTime)
}

func (s *bulkOperationService) bulkAssignTickets(operationID uint, ticketIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	assigneeID, ok := parameters["assigned_to_id"].(float64)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'assigned_to_id' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(ticketIDs))
	for i, id := range ticketIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"assigned_to_id": uint(assigneeID)},
		}
	}

	return s.performBulkUpdateAction(operationID, "tickets", updates, startTime)
}

func (s *bulkOperationService) bulkUpdateTicketPriority(operationID uint, ticketIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	newPriority, ok := parameters["priority"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'priority' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(ticketIDs))
	for i, id := range ticketIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"priority": newPriority},
		}
	}

	return s.performBulkUpdateAction(operationID, "tickets", updates, startTime)
}

func (s *bulkOperationService) bulkUpdateTicketCategory(operationID uint, ticketIDs []uint, parameters map[string]interface{}, startTime time.Time) (*models.BulkResponse, error) {
	newCategory, ok := parameters["category"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'category' parameter")
	}

	updates := make([]models.BulkUpdateItem, len(ticketIDs))
	for i, id := range ticketIDs {
		updates[i] = models.BulkUpdateItem{
			ID:      id,
			Updates: map[string]interface{}{"category": newCategory},
		}
	}

	return s.performBulkUpdateAction(operationID, "tickets", updates, startTime)
}

// Generic helper for performing bulk update actions
func (s *bulkOperationService) performBulkUpdateAction(operationID uint, resourceType string, updates []models.BulkUpdateItem, startTime time.Time) (*models.BulkResponse, error) {
	var results interface{}
	var operationErrors []error

	err := s.transactionMgr.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, _ := utils.GetTxFromContext(ctx)
		txRepo := s.bulkRepo.WithTx(tx)
		
		switch resourceType {
		case "leads":
			results, operationErrors = txRepo.BulkUpdateLeads(updates)
		case "customers":
			results, operationErrors = txRepo.BulkUpdateCustomers(updates)
		case "tasks":
			results, operationErrors = txRepo.BulkUpdateTasks(updates)
		case "tickets":
			results, operationErrors = txRepo.BulkUpdateTickets(updates)
		default:
			return fmt.Errorf("unsupported resource type: %s", resourceType)
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("transaction failed: %w", err)
	}

	// Count successful results
	var successCount int
	switch r := results.(type) {
	case []models.Lead:
		successCount = len(r)
	case []models.Customer:
		successCount = len(r)
	case []models.Task:
		successCount = len(r)
	case []models.Ticket:
		successCount = len(r)
	}

	// Build response
	response := s.buildBulkResponse(operationID, len(updates), successCount, operationErrors, results, startTime)

	// Update operation status
	status := models.StatusCompleted
	if len(operationErrors) > 0 {
		if successCount == 0 {
			status = models.StatusFailed
		} else {
			status = models.StatusPartial
		}
	}

	s.UpdateBulkOperationStatus(operationID, status)

	return response, nil
}