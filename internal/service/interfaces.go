package service

import (
	"github.com/florinel-chis/gocrm/internal/models"
)

type AuthService interface {
	Login(email, password string) (string, error)
	ValidateToken(token string) (*models.User, error)
	ValidateAPIKey(key string) (*models.User, error)
	GenerateJWT(user *models.User) (string, error)
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
	GetByOwner(ownerID uint, offset, limit int) ([]models.Lead, error)
	Update(id uint, updates map[string]interface{}) (*models.Lead, error)
	Delete(id uint) error
	List(offset, limit int) ([]models.Lead, int64, error)
	ConvertToCustomer(leadID uint, customerData *models.Customer) (*models.Customer, error)
}

type CustomerService interface {
	Create(customer *models.Customer) error
	GetByID(id uint) (*models.Customer, error)
	Update(customer *models.Customer) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Customer, int64, error)
}

type TicketService interface {
	Create(ticket *models.Ticket) error
	GetByID(id uint) (*models.Ticket, error)
	GetByCustomer(customerID uint, offset, limit int) ([]models.Ticket, int64, error)
	GetByAssignee(assigneeID uint, offset, limit int) ([]models.Ticket, int64, error)
	Update(ticket *models.Ticket) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Ticket, int64, error)
}

type TaskService interface {
	Create(task *models.Task) error
	GetByID(id uint) (*models.Task, error)
	GetByAssignee(assigneeID uint, offset, limit int) ([]models.Task, int64, error)
	Update(task *models.Task) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Task, int64, error)
}

type APIKeyService interface {
	Generate(userID uint, name string) (string, *models.APIKey, error)
	GetByUser(userID uint) ([]models.APIKey, error)
	Revoke(id uint, userID uint) error
	List(userID uint) ([]models.APIKey, error)
}