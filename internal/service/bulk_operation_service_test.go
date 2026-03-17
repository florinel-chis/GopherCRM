package service

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestBulkOperationService_ValidateBulkRequest(t *testing.T) {
	// Mock repositories
	mockBulkOperationRepo := &mocks.BulkOperationRepository{}
	mockBulkRepo := &mocks.BulkRepository{}
	mockUserRepo := &mocks.UserRepository{}
	mockLeadRepo := &mocks.LeadRepository{}
	mockCustomerRepo := &mocks.CustomerRepository{}
	mockTaskRepo := &mocks.TaskRepository{}
	mockTicketRepo := &mocks.TicketRepository{}
	mockTransactionMgr := &mocks.TransactionManager{}
	
	logger := logrus.New()

	service := NewBulkOperationService(
		mockBulkOperationRepo,
		mockBulkRepo,
		mockUserRepo,
		mockLeadRepo,
		mockCustomerRepo,
		mockTaskRepo,
		mockTicketRepo,
		mockTransactionMgr,
		logger,
	)

	bulkService := service.(*bulkOperationService)

	tests := []struct {
		name        string
		itemCount   int
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Valid item count",
			itemCount:   50,
			expectError: false,
		},
		{
			name:        "Empty request",
			itemCount:   0,
			expectError: true,
			errorMsg:    "no items provided for bulk operation",
		},
		{
			name:        "Too many items",
			itemCount:   1001,
			expectError: true,
			errorMsg:    "too many items: 1001, maximum allowed: 1000",
		},
		{
			name:        "Maximum allowed items",
			itemCount:   1000,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := bulkService.validateBulkRequest(tt.itemCount)
			
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBulkOperationService_CreateBulkOperation(t *testing.T) {
	// Mock repositories
	mockBulkOperationRepo := &mocks.BulkOperationRepository{}
	mockBulkRepo := &mocks.BulkRepository{}
	mockUserRepo := &mocks.UserRepository{}
	mockLeadRepo := &mocks.LeadRepository{}
	mockCustomerRepo := &mocks.CustomerRepository{}
	mockTaskRepo := &mocks.TaskRepository{}
	mockTicketRepo := &mocks.TicketRepository{}
	mockTransactionMgr := &mocks.TransactionManager{}
	
	logger := logrus.New()

	service := NewBulkOperationService(
		mockBulkOperationRepo,
		mockBulkRepo,
		mockUserRepo,
		mockLeadRepo,
		mockCustomerRepo,
		mockTaskRepo,
		mockTicketRepo,
		mockTransactionMgr,
		logger,
	)

	// Set up expectations
	_ = &models.BulkOperation{
		BaseModel:     models.BaseModel{ID: 1},
		UserID:        1,
		ResourceType:  "users",
		OperationType: models.BulkCreate,
		Status:        models.StatusPending,
		TotalItems:    10,
	}

	mockBulkOperationRepo.On("Create", mock.AnythingOfType("*models.BulkOperation")).Return(nil).Run(func(args mock.Arguments) {
		operation := args.Get(0).(*models.BulkOperation)
		operation.ID = 1
	})

	// Execute test
	result, err := service.CreateBulkOperation(1, "users", models.BulkCreate, 10)

	// Assertions
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, uint(1), result.UserID)
	assert.Equal(t, "users", result.ResourceType)
	assert.Equal(t, models.BulkCreate, result.OperationType)
	assert.Equal(t, models.StatusPending, result.Status)
	assert.Equal(t, 10, result.TotalItems)

	// Verify expectations
	mockBulkOperationRepo.AssertExpectations(t)
}

func TestBulkOperationService_ProcessBulkCreate_InvalidResourceType(t *testing.T) {
	// Mock repositories
	mockBulkOperationRepo := &mocks.BulkOperationRepository{}
	mockBulkRepo := &mocks.BulkRepository{}
	mockUserRepo := &mocks.UserRepository{}
	mockLeadRepo := &mocks.LeadRepository{}
	mockCustomerRepo := &mocks.CustomerRepository{}
	mockTaskRepo := &mocks.TaskRepository{}
	mockTicketRepo := &mocks.TicketRepository{}
	mockTransactionMgr := &mocks.TransactionManager{}
	
	logger := logrus.New()

	service := NewBulkOperationService(
		mockBulkOperationRepo,
		mockBulkRepo,
		mockUserRepo,
		mockLeadRepo,
		mockCustomerRepo,
		mockTaskRepo,
		mockTicketRepo,
		mockTransactionMgr,
		logger,
	)

	request := &models.BulkCreateRequest{
		Items: []map[string]interface{}{
			{"name": "Test Item"},
		},
	}

	// Execute test
	result, err := service.ProcessBulkCreate(1, "invalid_resource", request)

	// Assertions
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "unsupported resource type: invalid_resource")
}

func TestBulkOperationService_ConvertMapToModel(t *testing.T) {
	// Mock repositories  
	mockBulkOperationRepo := &mocks.BulkOperationRepository{}
	mockBulkRepo := &mocks.BulkRepository{}
	mockUserRepo := &mocks.UserRepository{}
	mockLeadRepo := &mocks.LeadRepository{}
	mockCustomerRepo := &mocks.CustomerRepository{}
	mockTaskRepo := &mocks.TaskRepository{}
	mockTicketRepo := &mocks.TicketRepository{}
	mockTransactionMgr := &mocks.TransactionManager{}
	
	logger := logrus.New()

	service := NewBulkOperationService(
		mockBulkOperationRepo,
		mockBulkRepo,
		mockUserRepo,
		mockLeadRepo,
		mockCustomerRepo,
		mockTaskRepo,
		mockTicketRepo,
		mockTransactionMgr,
		logger,
	)

	bulkService := service.(*bulkOperationService)

	tests := []struct {
		name        string
		input       map[string]interface{}
		expectError bool
	}{
		{
			name: "Valid user data",
			input: map[string]interface{}{
				"name":  "John Doe",
				"email": "john@example.com",
				"role":  "customer",
			},
			expectError: false,
		},
		{
			name: "Valid lead data",
			input: map[string]interface{}{
				"name":   "Jane Smith",
				"email":  "jane@example.com",
				"source": "website",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var user models.User
			err := bulkService.convertMapToModel(tt.input, &user)
			
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.input["email"], user.Email)
			}
		})
	}
}