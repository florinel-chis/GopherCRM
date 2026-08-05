package service

import (
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
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
	
	// Check for duplicate email. The lookup must be unscoped: the unique index on
	// customers.email is not scoped to deleted_at, so a soft-deleted row still
	// reserves the address and the insert below would be rejected by the database
	// even though a scoped lookup reports the address as free.
	existing, err := s.customerRepo.GetByEmailUnscoped(customer.Email)
	if err == nil && existing != nil {
		logger.Warn("Attempted to create customer with duplicate email")
		return fmt.Errorf("customer with this email already exists: %w", apperrors.ErrDuplicateEmail)
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
		// "No such customer" and "the lookup failed" are different answers and
		// the caller has to be able to tell them apart. The repository hands
		// gorm's own sentinel straight back, which matches neither apperrors
		// sentinel, so the miss is re-wrapped here; anything else is returned
		// untouched so it cannot be mistaken for a missing row.
		if isNotFound(err) {
			logger.WithError(err).Warn("Customer not found")
			return nil, fmt.Errorf("customer %d not found: %w", id, apperrors.ErrNotFound)
		}
		logger.WithError(err).Error("Failed to look up customer")
		return nil, err
	}

	logger.Debug("Customer retrieved successfully")
	return customer, nil
}

func (s *customerService) GetByUserID(userID uint) (*models.Customer, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("user_id", userID), "CustomerService", "GetByUserID")

	customer, err := s.customerRepo.GetByUserID(userID)
	if err != nil {
		logger.WithError(err).Warn("Customer not found for user")
		return nil, err
	}

	logger.Debug("Customer retrieved by user ID successfully")
	return customer, nil
}

func (s *customerService) Update(customer *models.Customer) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("customer_id", customer.ID), "CustomerService", "Update")
	
	// Check for duplicate email if email is being updated
	if customer.Email != "" {
		// Unscoped for the same reason as in Create: a soft-deleted row still
		// holds the address in the database unique index.
		existing, err := s.customerRepo.GetByEmailUnscoped(customer.Email)
		if err == nil && existing != nil && existing.ID != customer.ID {
			logger.Warn("Attempted to update customer with duplicate email")
			return fmt.Errorf("customer with this email already exists: %w", apperrors.ErrDuplicateEmail)
		}
	}
	
	if err := s.customerRepo.Update(customer); err != nil {
		logger.WithError(err).Error("Failed to update customer")
		return err
	}
	
	logger.Info("Customer updated successfully")
	return nil
}

// Delete erases the customer's personal data and then soft-deletes the row. It
// is a GDPR Article 17 erasure and it is IRREVERSIBLE: the email becomes a
// non-routable placeholder and every other personal field — names, phone,
// company, position, postal address, free-text notes — is cleared. See
// customerRepository.Delete for the details.
//
// This is NOT how you park a dormant account. Nothing here is recoverable, so
// use it only when the personal data itself must go.
//
// Note the logging below deliberately records only the customer ID: an erasure
// audit trail that quotes the erased email would defeat the erasure.
func (s *customerService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("customer_id", id), "CustomerService", "Delete")

	// An erasure that matched no row must never report success: the update and
	// the delete both match by primary key, and zero matched rows is not an
	// error in SQL. Reported as the not-found sentinel so the caller can tell
	// "there was nobody to erase" from "the erasure failed".
	if _, err := s.customerRepo.GetByID(id); err != nil {
		if isNotFound(err) {
			logger.WithError(err).Warn("Customer not found")
			return fmt.Errorf("customer %d not found: %w", id, apperrors.ErrNotFound)
		}
		logger.WithError(err).Error("Failed to look up customer for erasure")
		return err
	}

	if err := s.customerRepo.Delete(id); err != nil {
		logger.WithError(err).Error("Failed to erase customer")
		return err
	}

	logger.Info("Customer erased successfully")
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

func (s *customerService) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Customer, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset":     offset,
		"limit":      limit,
		"sort_by":    sortBy,
		"sort_order": sortOrder,
	}), "CustomerService", "ListSorted")

	customers, err := s.customerRepo.ListSortedWithPreloads(offset, limit, sortBy, sortOrder)
	if err != nil {
		logger.WithError(err).Error("Failed to list customers sorted")
		return nil, 0, err
	}

	total, err := s.customerRepo.Count()
	if err != nil {
		logger.WithError(err).Error("Failed to count customers")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Customers listed sorted successfully")
	return customers, total, nil
}

func (s *customerService) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Customer, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"query":  query,
		"offset": offset,
		"limit":  limit,
	}), "CustomerService", "Search")

	customers, err := s.customerRepo.Search(query, offset, limit, sortBy, sortOrder)
	if err != nil {
		logger.WithError(err).Error("Failed to search customers")
		return nil, 0, err
	}

	total, err := s.customerRepo.CountSearch(query)
	if err != nil {
		logger.WithError(err).Error("Failed to count search results")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Customer search completed")
	return customers, total, nil
}

func (s *customerService) GetCount() (int64, error) {
	return s.customerRepo.Count()
}

// ExportAll returns every customer matching the optional search term, unpaginated.
//
// The caller is expected to be the admin-only CSV export: this is a bulk read of
// personal data, so it is deliberately a distinct method rather than "List with
// a very large limit", and the audit log below records that a full export was
// taken.
func (s *customerService) ExportAll(search, sortBy, sortOrder string) ([]models.Customer, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"search":     search,
		"sort_by":    sortBy,
		"sort_order": sortOrder,
	}), "CustomerService", "ExportAll")

	customers, err := s.customerRepo.ListAllForExport(search, sortBy, sortOrder)
	if err != nil {
		logger.WithError(err).Error("Failed to export customers")
		return nil, err
	}

	logger.WithField("count", len(customers)).Info("Customer export produced")
	return customers, nil
}

// Assign sets the staff account that owns a customer relationship.
//
// The assignee is validated the same way a ticket assignee is: the account has
// to exist, has to be active, and has to hold a role that can actually work the
// record. Customer ownership is a sales function, so admin and sales qualify and
// support and customer accounts do not — handing a book of customers to a
// customer-role account would be a data-protection incident, not a typo.
//
// Each rejection carries its own sentinel so the handler can answer 404 for a
// missing customer or user and 400 for one that exists but cannot be assigned.
func (s *customerService) Assign(customerID, userID uint) (*models.Customer, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"customer_id": customerID,
		"user_id":     userID,
	}), "CustomerService", "Assign")

	customer, err := s.customerRepo.GetByID(customerID)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(err).Warn("Customer not found")
			return nil, fmt.Errorf("customer %d not found: %w", customerID, apperrors.ErrNotFound)
		}
		logger.WithError(err).Error("Failed to look up customer for assignment")
		return nil, err
	}

	assignee, err := s.userRepo.GetByID(userID)
	if err != nil {
		logger.WithError(err).Warn("Assignee not found")
		return nil, fmt.Errorf("assignee not found: %w", apperrors.ErrAssigneeNotFound)
	}

	if !assignee.IsActive {
		logger.Warn("Cannot assign customer to inactive user")
		return nil, fmt.Errorf("cannot assign customer to inactive user: %w", apperrors.ErrInactiveUser)
	}

	if assignee.Role != models.RoleSales && assignee.Role != models.RoleAdmin {
		logger.WithField("assignee_role", assignee.Role).Warn("Invalid assignee role")
		return nil, fmt.Errorf("customers can only be assigned to sales or admin users: %w", apperrors.ErrInvalidCustomerAssignee)
	}

	customer.AssignedToID = &userID
	customer.AssignedTo = nil // let the caller re-read the association rather than persist a stale copy
	if err := s.customerRepo.Update(customer); err != nil {
		logger.WithError(err).Error("Failed to assign customer")
		return nil, err
	}

	logger.Info("Customer assigned successfully")
	return customer, nil
}