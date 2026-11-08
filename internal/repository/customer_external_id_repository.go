package repository

import (
	"context"
	"errors"
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// customerExternalIDRepository stores the external identity mappings of
// customers (#212). The table's only unique index is the identity triple, so a
// duplicate-key error on it can be attributed to the triple. Every comparison
// is exact: SQLite compares text with BINARY, and MySQL/MariaDB carry
// utf8mb4_bin on the three columns (migration 20261111120000 and the collation
// guard of models.MigrateDatabase).
type customerExternalIDRepository struct {
	db *gorm.DB
}

func NewCustomerExternalIDRepository(db *gorm.DB) CustomerExternalIDRepository {
	return &customerExternalIDRepository{db: db}
}

func (r *customerExternalIDRepository) WithTx(tx *gorm.DB) CustomerExternalIDRepository {
	return &customerExternalIDRepository{db: tx}
}

// LockLiveCustomer takes the row lock of the live customer: a mapping created
// in the same transaction afterwards cannot land on a customer whose erasure
// commits in between, because the erasure's scrub waits for the lock (or the
// lock waits for the erasure, which then reads as not found).
func (r *customerExternalIDRepository) LockLiveCustomer(ctx context.Context, customerID uint) error {
	var customer models.Customer
	return lockingRead(r.db.WithContext(ctx)).Select("id").Where("id = ?", customerID).Take(&customer).Error
}

func (r *customerExternalIDRepository) Create(ctx context.Context, mapping *models.CustomerExternalID) error {
	if err := r.db.WithContext(ctx).Omit(clause.Associations).Create(mapping).Error; err != nil {
		if isDuplicateKeyError(err) {
			// The driver's message quotes the duplicate value; it is not kept.
			return fmt.Errorf("customer %d external id: %w", mapping.CustomerID, apperrors.ErrCustomerExternalIDTaken)
		}
		return err
	}
	return nil
}

func (r *customerExternalIDRepository) FindCustomerByIdentity(ctx context.Context, source, scope, externalID string) (*models.Customer, error) {
	var customer models.Customer
	err := r.db.WithContext(ctx).Model(&models.Customer{}).
		Joins("JOIN customer_external_ids ON customer_external_ids.customer_id = customers.id").
		Where("customer_external_ids.source = ? AND customer_external_ids.scope = ? AND customer_external_ids.external_id = ?",
			source, scope, externalID).
		Take(&customer).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (r *customerExternalIDRepository) DeleteForCustomer(ctx context.Context, customerID, mappingID uint) error {
	result := r.db.WithContext(ctx).
		Where("id = ? AND customer_id = ?", mappingID, customerID).
		Delete(&models.CustomerExternalID{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
