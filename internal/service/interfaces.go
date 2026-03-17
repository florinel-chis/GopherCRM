package service

import (
	"github.com/florinel-chis/gophercrm/internal/models"
)

type AuthTokens struct {
	AccessToken  string
	RefreshToken string
}

type AuthService interface {
	Login(email, password string) (string, error)
	LoginWithTokens(email, password string) (*AuthTokens, error)
	ValidateToken(token string) (*models.User, error)
	ValidateAPIKey(key string) (*models.User, error)
	GenerateJWT(user *models.User) (string, error)
	GenerateTokens(user *models.User) (*AuthTokens, error)
	RefreshAccessToken(refreshToken string) (*AuthTokens, error)
	InvalidateRefreshToken(refreshToken string) error
	GenerateCSRFToken() (string, error)
	ValidateCSRFToken(token string) bool
}

type UserService interface {
	Register(user *models.User, password string) error
	GetByID(id uint) (*models.User, error)
	GetByEmail(email string) (*models.User, error)
	Update(id uint, updates map[string]interface{}) (*models.User, error)
	Delete(id uint) error
	List(offset, limit int) ([]models.User, int64, error)
}

type LeadService interface {
	Create(lead *models.Lead) error
	GetByID(id uint) (*models.Lead, error)
	GetByExternalID(externalID string) (*models.Lead, error)
	GetByOwner(ownerID uint, offset, limit int) ([]models.Lead, error)
	GetByClassification(classification models.LeadClassification, offset, limit int) ([]models.Lead, int64, error)
	Update(id uint, updates map[string]interface{}) (*models.Lead, error)
	Delete(id uint) error
	List(offset, limit int) ([]models.Lead, int64, error)
	ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error)
	Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error)
	ConvertToCustomer(leadID uint, customerData *models.Customer) (*models.Customer, error)
	GetCount() (int64, error)
	GetCountByClassification(classification models.LeadClassification) (int64, error)
}

type CustomerService interface {
	Create(customer *models.Customer) error
	GetByID(id uint) (*models.Customer, error)
	Update(customer *models.Customer) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Customer, int64, error)
	GetCount() (int64, error)
}

type TicketService interface {
	Create(ticket *models.Ticket) error
	GetByID(id uint) (*models.Ticket, error)
	GetByCustomer(customerID uint, offset, limit int) ([]models.Ticket, int64, error)
	GetByAssignee(assigneeID uint, offset, limit int) ([]models.Ticket, int64, error)
	Update(ticket *models.Ticket) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Ticket, int64, error)
	GetOpenCount() (int64, error)
}

type TaskService interface {
	Create(task *models.Task) error
	GetByID(id uint) (*models.Task, error)
	GetByAssignee(assigneeID uint, offset, limit int) ([]models.Task, int64, error)
	Update(task *models.Task) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Task, int64, error)
	GetPendingCount() (int64, error)
}

type APIKeyService interface {
	Generate(userID uint, name string) (string, *models.APIKey, error)
	GetByUser(userID uint) ([]models.APIKey, error)
	Revoke(id uint, userID uint) error
	List(userID uint) ([]models.APIKey, error)
}

type BulkOperationService interface {
	// Bulk operation management
	CreateBulkOperation(userID uint, resourceType string, operationType models.BulkOperationType, totalItems int) (*models.BulkOperation, error)
	GetBulkOperation(id uint) (*models.BulkOperation, error)
	GetBulkOperationWithItems(id uint) (*models.BulkOperation, error)
	GetUserBulkOperations(userID uint, offset, limit int) ([]models.BulkOperation, error)
	UpdateBulkOperationStatus(id uint, status models.BulkOperationStatus) error
	
	// Generic bulk operations
	ProcessBulkCreate(userID uint, resourceType string, request *models.BulkCreateRequest) (*models.BulkResponse, error)
	ProcessBulkUpdate(userID uint, resourceType string, request *models.BulkUpdateRequest) (*models.BulkResponse, error)
	ProcessBulkDelete(userID uint, resourceType string, request *models.BulkDeleteRequest) (*models.BulkResponse, error)
	ProcessBulkAction(userID uint, resourceType string, request *models.BulkActionRequest) (*models.BulkResponse, error)
	
	// User bulk operations
	BulkCreateUsers(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkUpdateUsers(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkDeleteUsers(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkActionUsers(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error)
	
	// Lead bulk operations
	BulkCreateLeads(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkUpdateLeads(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkDeleteLeads(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkActionLeads(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error)
	
	// Customer bulk operations
	BulkCreateCustomers(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkUpdateCustomers(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkDeleteCustomers(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkActionCustomers(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error)
	
	// Task bulk operations
	BulkCreateTasks(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkUpdateTasks(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkDeleteTasks(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkActionTasks(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error)
	
	// Ticket bulk operations
	BulkCreateTickets(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkUpdateTickets(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkDeleteTickets(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkActionTickets(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error)
}