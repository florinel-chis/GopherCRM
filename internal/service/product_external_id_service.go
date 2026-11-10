package service

import (
	"context"
	"errors"
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// ErrProductExternalIDsUnavailable is returned by every method of a
// ProductExternalIDService built without its repository or transaction
// manager, and by ProductService.Delete built without WithProductExternalIDs,
// before anything is read or written. It is a wiring fault, answered with the
// generic 500.
var ErrProductExternalIDsUnavailable = errors.New("product external ids are unavailable")

// ValidateProductExternalIdentity applies the customer rules (#212) to a
// product triple: source 1-64, scope 0-128 and external_id 1-255 characters
// (runes), each valid UTF-8 with no control characters and no leading or
// trailing whitespace. The first refused field is returned as a
// *CustomerExternalIDFieldError naming the field and the rule, never the
// value. The column widths of product_external_ids equal those of
// customer_external_ids, so the shared validator applies unchanged.
func ValidateProductExternalIdentity(source, scope, externalID string) error {
	return ValidateCustomerExternalIdentity(source, scope, externalID)
}

type productExternalIDService struct {
	repo      repository.ProductExternalIDRepository
	txManager *utils.TransactionManager
}

// NewProductExternalIDService wires the service. A nil repository or
// transaction manager leaves a service whose every method returns
// ErrProductExternalIDsUnavailable.
func NewProductExternalIDService(repo repository.ProductExternalIDRepository, txManager *utils.TransactionManager) ProductExternalIDService {
	return &productExternalIDService{repo: repo, txManager: txManager}
}

// The log lines of this service carry the operation, product_id, mapping_id,
// counts and presence flags only, never a source, scope or external_id value.
func (s *productExternalIDService) logger(operation string) *logrus.Entry {
	return utils.LogServiceCall(utils.Logger.WithField("operation", operation), "ProductExternalIDService", operation)
}

func (s *productExternalIDService) wired() bool {
	return s.repo != nil && s.txManager != nil
}

func (s *productExternalIDService) AddExternalID(ctx context.Context, productID uint, source, scope, externalID string) (*models.ProductExternalID, error) {
	logger := s.logger("AddExternalID").WithField("product_id", productID)
	if !s.wired() {
		logger.Error("Product external ids are unavailable: no repository or transaction manager is wired")
		return nil, ErrProductExternalIDsUnavailable
	}
	if err := ValidateProductExternalIdentity(source, scope, externalID); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Product external id refused by validation")
		return nil, err
	}

	mapping := &models.ProductExternalID{ProductID: productID, Source: source, Scope: scope, ExternalID: externalID}
	err := s.txManager.WithTransaction(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		repo := s.repo.WithTx(tx)
		// The lock comes first: a concurrent product delete either commits
		// before it (the product reads as gone) or waits behind this
		// transaction and then purges the mapping inserted here.
		if err := repo.LockLiveProduct(ctx, productID); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("product %d: %w", productID, apperrors.ErrNotFound)
			}
			return err
		}
		return repo.Create(ctx, mapping)
	})
	if err != nil {
		switch {
		case errors.Is(err, apperrors.ErrNotFound):
			logger.Warn("Product external id refused: no live product")
		case errors.Is(err, apperrors.ErrProductExternalIDTaken):
			logger.Warn("Product external id refused: the identity is already mapped")
		default:
			logger.WithError(utils.LogError(err)).Error("Failed to add product external id")
		}
		return nil, err
	}

	logger.WithField("mapping_id", mapping.ID).Info("Product external id added")
	return mapping, nil
}

func (s *productExternalIDService) RemoveExternalID(ctx context.Context, productID, mappingID uint) error {
	logger := s.logger("RemoveExternalID").WithFields(logrus.Fields{"product_id": productID, "mapping_id": mappingID})
	if !s.wired() {
		logger.Error("Product external ids are unavailable: no repository or transaction manager is wired")
		return ErrProductExternalIDsUnavailable
	}
	if err := s.repo.WithTx(s.txManager.DB(ctx)).DeleteForProduct(ctx, productID, mappingID); err != nil {
		if isNotFound(err) {
			logger.Warn("Product external id not found for this product")
			return fmt.Errorf("product %d mapping %d: %w", productID, mappingID, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to remove product external id")
		return err
	}
	logger.Info("Product external id removed")
	return nil
}

func (s *productExternalIDService) LookupProduct(ctx context.Context, source, scope, externalID string) (*models.Product, error) {
	logger := s.logger("LookupProduct").WithField("scope_present", scope != "")
	if !s.wired() {
		logger.Error("Product external ids are unavailable: no repository or transaction manager is wired")
		return nil, ErrProductExternalIDsUnavailable
	}
	if err := ValidateProductExternalIdentity(source, scope, externalID); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Product external id lookup refused by validation")
		return nil, err
	}
	product, err := s.repo.WithTx(s.txManager.DB(ctx)).FindProductByIdentity(ctx, source, scope, externalID)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to look up product by external id")
		return nil, err
	}
	if product == nil {
		logger.WithField("count", 0).Debug("Product external id lookup found no product")
		return nil, nil
	}
	logger.WithFields(logrus.Fields{"count": 1, "product_id": product.ID}).Debug("Product external id lookup found a product")
	return product, nil
}
