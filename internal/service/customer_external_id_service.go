package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// Validation limits of the external identity triple, in runes. They are the
// column widths of customer_external_ids (TestCustomerExternalIDMaxLengthsMatchTheColumns).
const (
	CustomerExternalIDSourceMaxLength = models.CustomerExternalIDSourceMaxLength
	CustomerExternalIDScopeMaxLength  = models.CustomerExternalIDScopeMaxLength
	CustomerExternalIDValueMaxLength  = models.CustomerExternalIDValueMaxLength
)

// The field names a CustomerExternalIDFieldError reports; they are the JSON
// and query names.
const (
	CustomerExternalIDFieldSource     = "source"
	CustomerExternalIDFieldScope      = "scope"
	CustomerExternalIDFieldExternalID = "external_id"
)

// ErrCustomerExternalIDsUnavailable is returned by every method of a
// CustomerExternalIDService built without its repository or transaction
// manager, before anything is read or written. It is a wiring fault, answered
// with the generic 500.
var ErrCustomerExternalIDsUnavailable = errors.New("customer external ids are unavailable")

// CustomerExternalIDFieldError is a refused field of the identity triple. It
// names the field and the rule, never the value, and matches
// apperrors.ErrValidation.
type CustomerExternalIDFieldError struct {
	// Field is source, scope or external_id.
	Field string
	// Reason is the rule the value broke, for example "is required".
	Reason string
}

func (e *CustomerExternalIDFieldError) Error() string {
	return fmt.Sprintf("%s %s", e.Field, e.Reason)
}

// Unwrap makes the error match apperrors.ErrValidation.
func (e *CustomerExternalIDFieldError) Unwrap() error { return apperrors.ErrValidation }

// LogMessage is what utils.LogError writes: the field and the rule.
func (e *CustomerExternalIDFieldError) LogMessage() string {
	return "external id field " + e.Field + " " + e.Reason
}

// validateExternalIDField applies the shared character rules and the length
// bounds, counted in runes.
func validateExternalIDField(field, value string, minLength, maxLength int) error {
	if !utf8.ValidString(value) {
		return &CustomerExternalIDFieldError{Field: field, Reason: "must be valid UTF-8"}
	}
	length := utf8.RuneCountInString(value)
	if length < minLength {
		return &CustomerExternalIDFieldError{Field: field, Reason: "is required"}
	}
	if length > maxLength {
		return &CustomerExternalIDFieldError{Field: field, Reason: fmt.Sprintf("must be at most %d characters", maxLength)}
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return &CustomerExternalIDFieldError{Field: field, Reason: "must not contain control characters"}
		}
	}
	if strings.TrimSpace(value) != value {
		return &CustomerExternalIDFieldError{Field: field, Reason: "must not start or end with whitespace"}
	}
	return nil
}

// ValidateCustomerExternalIdentity checks the triple: source 1-64, scope
// 0-128 and external_id 1-255 characters (runes), each valid UTF-8 with no
// control characters and no leading or trailing whitespace. The first refused
// field is returned as a *CustomerExternalIDFieldError, in the order source,
// scope, external_id. Values are compared exactly, so nothing is normalised.
func ValidateCustomerExternalIdentity(source, scope, externalID string) error {
	if err := validateExternalIDField(CustomerExternalIDFieldSource, source, 1, CustomerExternalIDSourceMaxLength); err != nil {
		return err
	}
	if err := validateExternalIDField(CustomerExternalIDFieldScope, scope, 0, CustomerExternalIDScopeMaxLength); err != nil {
		return err
	}
	return validateExternalIDField(CustomerExternalIDFieldExternalID, externalID, 1, CustomerExternalIDValueMaxLength)
}

type customerExternalIDService struct {
	repo      repository.CustomerExternalIDRepository
	txManager *utils.TransactionManager
}

// NewCustomerExternalIDService wires the service. A nil repository or
// transaction manager leaves a service whose every method returns
// ErrCustomerExternalIDsUnavailable.
func NewCustomerExternalIDService(repo repository.CustomerExternalIDRepository, txManager *utils.TransactionManager) CustomerExternalIDService {
	return &customerExternalIDService{repo: repo, txManager: txManager}
}

// The log lines of this service carry the operation, customer_id,
// mapping_id, counts and presence flags only: a source, scope or external_id
// identifies a person in another system and never reaches a log line.
func (s *customerExternalIDService) logger(operation string) *logrus.Entry {
	return utils.LogServiceCall(utils.Logger.WithField("operation", operation), "CustomerExternalIDService", operation)
}

func (s *customerExternalIDService) wired() bool {
	return s.repo != nil && s.txManager != nil
}

func (s *customerExternalIDService) AddExternalID(ctx context.Context, customerID uint, source, scope, externalID string) (*models.CustomerExternalID, error) {
	logger := s.logger("AddExternalID").WithField("customer_id", customerID)
	if !s.wired() {
		logger.Error("Customer external ids are unavailable: no repository or transaction manager is wired")
		return nil, ErrCustomerExternalIDsUnavailable
	}
	if err := ValidateCustomerExternalIdentity(source, scope, externalID); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Customer external id refused by validation")
		return nil, err
	}

	mapping := &models.CustomerExternalID{CustomerID: customerID, Source: source, Scope: scope, ExternalID: externalID}
	err := s.txManager.WithTransaction(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		repo := s.repo.WithTx(tx)
		// The lock comes first: a concurrent erasure either commits before it
		// (the customer reads as gone) or waits behind this transaction and
		// then deletes the mapping inserted here.
		if err := repo.LockLiveCustomer(ctx, customerID); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("customer %d: %w", customerID, apperrors.ErrNotFound)
			}
			return err
		}
		return repo.Create(ctx, mapping)
	})
	if err != nil {
		switch {
		case errors.Is(err, apperrors.ErrNotFound):
			logger.Warn("Customer external id refused: no live customer")
		case errors.Is(err, apperrors.ErrCustomerExternalIDTaken):
			logger.Warn("Customer external id refused: the identity is already mapped")
		default:
			logger.WithError(utils.LogError(err)).Error("Failed to add customer external id")
		}
		return nil, err
	}

	logger.WithField("mapping_id", mapping.ID).Info("Customer external id added")
	return mapping, nil
}

func (s *customerExternalIDService) RemoveExternalID(ctx context.Context, customerID, mappingID uint) error {
	logger := s.logger("RemoveExternalID").WithFields(logrus.Fields{"customer_id": customerID, "mapping_id": mappingID})
	if !s.wired() {
		logger.Error("Customer external ids are unavailable: no repository or transaction manager is wired")
		return ErrCustomerExternalIDsUnavailable
	}
	if err := s.repo.WithTx(s.txManager.DB(ctx)).DeleteForCustomer(ctx, customerID, mappingID); err != nil {
		if isNotFound(err) {
			logger.Warn("Customer external id not found for this customer")
			return fmt.Errorf("customer %d mapping %d: %w", customerID, mappingID, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to remove customer external id")
		return err
	}
	logger.Info("Customer external id removed")
	return nil
}

func (s *customerExternalIDService) LookupCustomer(ctx context.Context, source, scope, externalID string) (*models.Customer, error) {
	logger := s.logger("LookupCustomer").WithField("scope_present", scope != "")
	if !s.wired() {
		logger.Error("Customer external ids are unavailable: no repository or transaction manager is wired")
		return nil, ErrCustomerExternalIDsUnavailable
	}
	if err := ValidateCustomerExternalIdentity(source, scope, externalID); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Customer external id lookup refused by validation")
		return nil, err
	}
	customer, err := s.repo.WithTx(s.txManager.DB(ctx)).FindCustomerByIdentity(ctx, source, scope, externalID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to look up customer by external id")
		return nil, err
	}
	if customer == nil {
		logger.WithField("count", 0).Debug("Customer external id lookup found no customer")
		return nil, nil
	}
	logger.WithFields(logrus.Fields{"count": 1, "customer_id": customer.ID}).Debug("Customer external id lookup found a customer")
	return customer, nil
}
