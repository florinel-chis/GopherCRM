package repository

import (
	"context"
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

// TransactionManager provides transaction management capabilities
type TransactionManager interface {
	WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error
	WithTransactionAndRetry(ctx context.Context, fn func(ctx context.Context) error, maxRetries int) error
}

// TransactionContext allows repositories to work within a transaction
type TransactionContext struct {
	DB *gorm.DB
}

// RepositoryWithTransaction extends base repository with transaction support
type RepositoryWithTransaction interface {
	WithTx(tx *gorm.DB) interface{}
}

type UserRepository interface {
	Create(user *models.User) error
	GetByID(id uint) (*models.User, error)
	GetByEmail(email string) (*models.User, error)
	Update(user *models.User) error
	Delete(id uint) error
	List(offset, limit int) ([]models.User, error)
	ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.User, error)
	Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.User, error)
	CountSearch(query string) (int64, error)
	Count() (int64, error)
	UpdateLastLogin(id uint) error
	WithTx(tx *gorm.DB) UserRepository
}

type LeadRepository interface {
	Create(lead *models.Lead) error
	GetByID(id uint) (*models.Lead, error)
	GetByIDWithPreloads(id uint, preloads ...string) (*models.Lead, error)
	GetByExternalID(externalID string) (*models.Lead, error)
	GetByOwnerID(ownerID uint, offset, limit int) ([]models.Lead, error)
	GetByOwnerIDWithPreloads(ownerID uint, offset, limit int, preloads ...string) ([]models.Lead, error)
	GetByClassification(classification models.LeadClassification, offset, limit int) ([]models.Lead, error)
	Update(lead *models.Lead) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Lead, error)
	ListWithPreloads(offset, limit int, preloads ...string) ([]models.Lead, error)
	ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Lead, error)
	Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Lead, error)
	CountSearch(query string) (int64, error)
	Count() (int64, error)
	CountByClassification(classification models.LeadClassification) (int64, error)
	ConvertToCustomer(leadID uint, customerID uint) error
	WithTx(tx *gorm.DB) LeadRepository
}

type CustomerRepository interface {
	Create(customer *models.Customer) error
	GetByID(id uint) (*models.Customer, error)
	GetByIDWithPreloads(id uint, preloads ...string) (*models.Customer, error)
	GetByEmail(email string) (*models.Customer, error)
	Update(customer *models.Customer) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Customer, error)
	ListWithPreloads(offset, limit int, preloads ...string) ([]models.Customer, error)
	ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Customer, error)
	Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Customer, error)
	CountSearch(query string) (int64, error)
	Count() (int64, error)
	WithTx(tx *gorm.DB) CustomerRepository
}

type TicketRepository interface {
	Create(ticket *models.Ticket) error
	GetByID(id uint) (*models.Ticket, error)
	GetByIDWithPreloads(id uint, preloads ...string) (*models.Ticket, error)
	GetByCustomerID(customerID uint, offset, limit int) ([]models.Ticket, error)
	GetByCustomerIDWithPreloads(customerID uint, offset, limit int, preloads ...string) ([]models.Ticket, error)
	GetByAssignedToID(assignedToID uint, offset, limit int) ([]models.Ticket, error)
	GetByAssignedToIDWithPreloads(assignedToID uint, offset, limit int, preloads ...string) ([]models.Ticket, error)
	Update(ticket *models.Ticket) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Ticket, error)
	ListWithPreloads(offset, limit int, preloads ...string) ([]models.Ticket, error)
	ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Ticket, error)
	Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Ticket, error)
	CountSearch(query string) (int64, error)
	Count() (int64, error)
	CountByCustomerID(customerID uint) (int64, error)
	CountByAssignedToID(assignedToID uint) (int64, error)
	CountOpen() (int64, error)
	WithTx(tx *gorm.DB) TicketRepository
}

type TaskRepository interface {
	Create(task *models.Task) error
	GetByID(id uint) (*models.Task, error)
	GetByIDWithPreloads(id uint, preloads ...string) (*models.Task, error)
	GetByAssignedToID(assignedToID uint, offset, limit int) ([]models.Task, error)
	GetByAssignedToIDWithPreloads(assignedToID uint, offset, limit int, preloads ...string) ([]models.Task, error)
	Update(task *models.Task) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Task, error)
	ListWithPreloads(offset, limit int, preloads ...string) ([]models.Task, error)
	ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Task, error)
	Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Task, error)
	CountSearch(query string) (int64, error)
	Count() (int64, error)
	CountByAssignedToID(assignedToID uint) (int64, error)
	CountPending() (int64, error)
	WithTx(tx *gorm.DB) TaskRepository
}

type APIKeyRepository interface {
	Create(apiKey *models.APIKey) error
	GetByID(id uint) (*models.APIKey, error)
	GetByKeyHash(keyHash string) (*models.APIKey, error)
	GetByUserID(userID uint) ([]models.APIKey, error)
	Update(apiKey *models.APIKey) error
	Delete(id uint) error
	UpdateLastUsed(id uint) error
	WithTx(tx *gorm.DB) APIKeyRepository
}

// Note: ConfigurationRepository is defined in configuration_repository.go

type RefreshTokenRepository interface {
	Create(token *models.RefreshToken) error
	GetByTokenHash(tokenHash string) (*models.RefreshToken, error)
	GetByUserID(userID uint) ([]models.RefreshToken, error)
	RevokeByTokenHash(tokenHash string) error
	RevokeAllByUserID(userID uint) error
	RevokeAllForUser(userID uint) error
	DeleteExpired() error
	DeleteByTokenHash(tokenHash string) error
	WithTx(tx *gorm.DB) RefreshTokenRepository
}

type BulkOperationRepository interface {
	Create(operation *models.BulkOperation) error
	GetByID(id uint) (*models.BulkOperation, error)
	GetByIDWithItems(id uint) (*models.BulkOperation, error)
	GetByUserID(userID uint, offset, limit int) ([]models.BulkOperation, error)
	Update(operation *models.BulkOperation) error
	UpdateStatus(id uint, status models.BulkOperationStatus) error
	Delete(id uint) error
	List(offset, limit int) ([]models.BulkOperation, error)
	CreateItem(item *models.BulkOperationItem) error
	UpdateItem(item *models.BulkOperationItem) error
	GetItemsByOperationID(operationID uint) ([]models.BulkOperationItem, error)
	WithTx(tx *gorm.DB) BulkOperationRepository
}

type BulkRepository interface {
	// User bulk operations
	BulkCreateUsers(users []models.User) ([]models.User, []error)
	BulkUpdateUsers(updates []models.BulkUpdateItem) ([]models.User, []error)
	BulkDeleteUsers(ids []uint) []error

	// Lead bulk operations
	BulkCreateLeads(leads []models.Lead) ([]models.Lead, []error)
	BulkUpdateLeads(updates []models.BulkUpdateItem) ([]models.Lead, []error)
	BulkDeleteLeads(ids []uint) []error

	// Customer bulk operations
	BulkCreateCustomers(customers []models.Customer) ([]models.Customer, []error)
	BulkUpdateCustomers(updates []models.BulkUpdateItem) ([]models.Customer, []error)
	BulkDeleteCustomers(ids []uint) []error

	// Task bulk operations
	BulkCreateTasks(tasks []models.Task) ([]models.Task, []error)
	BulkUpdateTasks(updates []models.BulkUpdateItem) ([]models.Task, []error)
	BulkDeleteTasks(ids []uint) []error

	// Ticket bulk operations
	BulkCreateTickets(tickets []models.Ticket) ([]models.Ticket, []error)
	BulkUpdateTickets(updates []models.BulkUpdateItem) ([]models.Ticket, []error)
	BulkDeleteTickets(ids []uint) []error

	WithTx(tx *gorm.DB) BulkRepository
}