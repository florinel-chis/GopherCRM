package service

import (
	"context"
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/sirupsen/logrus"
)

type customerService struct {
	customerRepo repository.CustomerRepository
	userRepo     repository.UserRepository
	companyRepo  repository.CompanyRepository

	// tx runs Create and Update as one transaction each: Create inserts the
	// customer and emits customer.created (#345), Update takes a locking
	// read, writes the customer and emits customer.updated when a stored
	// value changed (#408). Its emitter is webhook.Nop unless
	// WithCustomerWebhooks sets one.
	tx txDeps
}

// CustomerServiceOption configures NewCustomerService.
type CustomerServiceOption func(*customerService)

// WithCustomerWebhooks makes Create emit customer.created and Update emit
// customer.updated through e, in the transaction that writes the customer.
// A nil e means webhook.Nop.
func WithCustomerWebhooks(e webhook.Emitter) CustomerServiceOption {
	return func(s *customerService) {
		if e == nil {
			e = webhook.Nop
		}
		s.tx.webhooks = e
	}
}

// NewCustomerService builds the customer service. txManager is required and
// must run over the same handle as the repositories; a nil one panics.
func NewCustomerService(customerRepo repository.CustomerRepository, userRepo repository.UserRepository, companyRepo repository.CompanyRepository, txManager *utils.TransactionManager, opts ...CustomerServiceOption) CustomerService {
	s := &customerService{
		customerRepo: customerRepo,
		userRepo:     userRepo,
		companyRepo:  companyRepo,
		tx:           newTxDeps(txManager),
	}
	s.tx.customers = customerRepo
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// checkCompanyLink rejects a company_id that matches no live company with
// apperrors.ErrCompanyNotFound, which the handler answers with 400
// INVALID_REFERENCE. A nil id is no link and always fine.
func (s *customerService) checkCompanyLink(companyID *uint) error {
	if companyID == nil {
		return nil
	}
	if _, err := s.companyRepo.GetByID(*companyID); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("unknown company_id %d: %w", *companyID, apperrors.ErrCompanyNotFound)
		}
		return err
	}
	return nil
}

func (s *customerService) Create(ctx context.Context, customer *models.Customer) error {
	if err := normalizeCustomerWebsite(customer); err != nil {
		return err
	}
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "customer"), "CustomerService", "Create")

	// Check for duplicate email. The lookup must be unscoped: the unique index on
	// customers.email is not scoped to deleted_at, so a soft-deleted row still
	// reserves the address and the insert below would be rejected by the database
	// even though a scoped lookup reports the address as free.
	existing, err := s.customerRepo.GetByEmailUnscoped(customer.Email)
	if err == nil && existing != nil {
		logger.Warn("Attempted to create customer with duplicate email")
		return fmt.Errorf("customer with this email already exists: %w", apperrors.ErrDuplicateEmail)
	}

	if err := s.checkCompanyLink(customer.CompanyID); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Customer rejected")
		return err
	}

	// The customer and its customer.created outbox rows commit together or
	// not at all (#345).
	err = s.tx.Run(ctx, func(ctx context.Context, u txUnit) error {
		if err := u.Customers.Create(customer); err != nil {
			return err
		}
		return u.Webhooks.Created(ctx, webhook.Customer, customer.ID)
	})
	if err != nil {
		customer.ID = 0
		logger.WithError(utils.LogError(err)).Error("Failed to create customer")
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
			logger.WithError(utils.LogError(err)).Warn("Customer not found")
			return nil, fmt.Errorf("customer %d not found: %w", id, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up customer")
		return nil, err
	}

	logger.Debug("Customer retrieved successfully")
	return customer, nil
}

func (s *customerService) GetByUserID(userID uint) (*models.Customer, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("user_id", userID), "CustomerService", "GetByUserID")

	customer, err := s.customerRepo.GetByUserID(userID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Warn("Customer not found for user")
		return nil, err
	}

	logger.Debug("Customer retrieved by user ID successfully")
	return customer, nil
}

func (s *customerService) Update(ctx context.Context, customer *models.Customer) error {
	if err := normalizeCustomerWebsite(customer); err != nil {
		return err
	}
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

	if err := s.checkCompanyLink(customer.CompanyID); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Customer update rejected")
		return err
	}

	return s.updateLocked(ctx, customer, logger)
}

// updateLocked is the write of Update (#408): one transaction that reads the
// live customer with a row lock, writes the fifteen columns
// customerRepository.Update owns and, when at least one of them differs from
// the locked row, emits customer.updated. A customer that is no longer live
// at the locking read is the same not-found answer as a write that lost to an
// erasure. A failed write or outbox insert rolls everything back.
//
// Lock order (docs/CONCURRENCY.md: companies -> leads -> customers -> deals):
// the one customers row is the only row lock this transaction takes. The
// duplicate-email and company checks ran before it, as plain reads; nothing
// here may take a locking read on companies or leads, which would invert the
// order against a company delete (UnlinkCustomers).
func (s *customerService) updateLocked(ctx context.Context, customer *models.Customer, logger *logrus.Entry) error {
	err := s.tx.Run(ctx, func(ctx context.Context, u txUnit) error {
		// The repository's preloads of this read are plain reads.
		locked, err := s.customerRepo.WithTx(u.LockingRead()).GetByID(customer.ID)
		if err != nil {
			return err
		}
		if err := u.Customers.Update(customer); err != nil {
			return err
		}
		if !customerStoredValuesChanged(locked, customer) {
			return nil
		}
		return u.Webhooks.Updated(ctx, webhook.Customer, customer.ID)
	})
	if err != nil {
		// The row is gone: the write lost to an erasure and wrote nothing.
		// That is the request's answer, not a failure.
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Customer update lost: no live customer")
			return fmt.Errorf("customer %d not found: %w", customer.ID, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to update customer")
		return err
	}
	logger.Info("Customer updated successfully")
	return nil
}

// customerStoredValuesChanged reports whether an update changed any of the
// columns customerRepository.Update writes: the rule of the customer.updated
// webhook event (#408). updated_at is not part of it.
func customerStoredValuesChanged(before, after *models.Customer) bool {
	return before.FirstName != after.FirstName ||
		before.LastName != after.LastName ||
		before.Email != after.Email ||
		before.Phone != after.Phone ||
		before.Company != after.Company ||
		before.Website != after.Website ||
		before.Position != after.Position ||
		before.Address != after.Address ||
		before.City != after.City ||
		before.State != after.State ||
		before.Country != after.Country ||
		before.PostalCode != after.PostalCode ||
		before.Notes != after.Notes ||
		!equalUintPtr(before.CompanyID, after.CompanyID) ||
		!equalUintPtr(before.AssignedToID, after.AssignedToID)
}

// Delete erases the customer's personal data and then soft-deletes the row. It
// is a GDPR Article 17 erasure and it is IRREVERSIBLE: the email becomes a
// non-routable placeholder and every other personal field — names, phone,
// company, website, position, postal address, free-text notes — is cleared. See
// customerRepository.Delete for the details.
//
// This is NOT how you park a dormant account. Nothing here is recoverable, so
// use it only when the personal data itself must go.
//
// Note the logging below deliberately records only the customer ID: an erasure
// audit trail that quotes the erased email would defeat the erasure.
func (s *customerService) Delete(ctx context.Context, id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("customer_id", id), "CustomerService", "Delete")

	// An erasure that matched no row must never report success: the update and
	// the delete both match by primary key, and zero matched rows is not an
	// error in SQL. Reported as the not-found sentinel so the caller can tell
	// "there was nobody to erase" from "the erasure failed".
	if _, err := s.customerRepo.GetByID(id); err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Customer not found")
			return fmt.Errorf("customer %d not found: %w", id, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up customer for erasure")
		return err
	}

	if err := s.customerRepo.Delete(ctx, id); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to erase customer")
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
		logger.WithError(utils.LogError(err)).Error("Failed to list customers")
		return nil, 0, err
	}

	total, err := s.customerRepo.Count()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count customers")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Customers listed successfully")
	return customers, total, nil
}

func (s *customerService) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Customer, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "CustomerService", "ListSorted")

	customers, err := s.customerRepo.ListSortedWithPreloads(offset, limit, sortBy, sortOrder)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list customers sorted")
		return nil, 0, err
	}

	total, err := s.customerRepo.Count()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count customers")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Customers listed sorted successfully")
	return customers, total, nil
}

func (s *customerService) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Customer, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "CustomerService", "Search")

	customers, err := s.customerRepo.Search(query, offset, limit, sortBy, sortOrder)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to search customers")
		return nil, 0, err
	}

	total, err := s.customerRepo.CountSearch(query)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count search results")
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
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{}), "CustomerService", "ExportAll")

	customers, err := s.customerRepo.ListAllForExport(search, sortBy, sortOrder)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to export customers")
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
			logger.WithError(utils.LogError(err)).Warn("Customer not found")
			return nil, fmt.Errorf("customer %d not found: %w", customerID, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up customer for assignment")
		return nil, err
	}

	assignee, err := s.userRepo.GetByID(userID)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Assignee not found")
			return nil, fmt.Errorf("assignee not found: %w", apperrors.ErrAssigneeNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up assignee for assignment")
		return nil, err
	}

	if !assignee.IsActive {
		logger.Warn("Cannot assign customer to inactive user")
		return nil, fmt.Errorf("cannot assign customer to inactive user: %w", apperrors.ErrInactiveUser)
	}

	if !permissions.ForUser(assignee).Has(permissions.AssignmentCustomerAssignee) {
		logger.WithField("assignee_role", assignee.Role).Warn("Invalid assignee role")
		return nil, fmt.Errorf("customers can only be assigned to sales or admin users: %w", apperrors.ErrInvalidCustomerAssignee)
	}

	customer.AssignedToID = &userID
	customer.AssignedTo = nil // let the caller re-read the association rather than persist a stale copy
	if err := s.customerRepo.Update(customer); err != nil {
		// As in Update: the customer was erased after the read above.
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Customer assignment lost: no live customer")
			return nil, fmt.Errorf("customer %d not found: %w", customerID, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to assign customer")
		return nil, err
	}

	logger.Info("Customer assigned successfully")
	return customer, nil
}
