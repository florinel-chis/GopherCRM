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

// productExternalIDRepository stores the external identity mappings of
// products (#239), on the pattern of the customer mappings (#212). The table's
// only unique index is the identity triple, so a duplicate-key error on it can
// be attributed to the triple. Every comparison is exact: SQLite compares text
// with BINARY, and MySQL/MariaDB carry utf8mb4_bin on the three columns
// (migration 20261112120000 and the collation guard of models.MigrateDatabase).
type productExternalIDRepository struct {
	db *gorm.DB
}

func NewProductExternalIDRepository(db *gorm.DB) ProductExternalIDRepository {
	return &productExternalIDRepository{db: db}
}

func (r *productExternalIDRepository) WithTx(tx *gorm.DB) ProductExternalIDRepository {
	return &productExternalIDRepository{db: tx}
}

// LockLiveProduct takes the row lock of the live product. A mapping create and
// a product delete both take it first, so a mapping created in one transaction
// cannot land on a product whose delete commits in between: either the create
// commits first and the delete then purges its mapping, or the delete commits
// first and the create's lock reads no live row.
func (r *productExternalIDRepository) LockLiveProduct(ctx context.Context, productID uint) error {
	var product models.Product
	return lockingRead(r.db.WithContext(ctx)).Select("id").Where("id = ?", productID).Take(&product).Error
}

func (r *productExternalIDRepository) Create(ctx context.Context, mapping *models.ProductExternalID) error {
	if err := r.db.WithContext(ctx).Omit(clause.Associations).Create(mapping).Error; err != nil {
		if isDuplicateKeyError(err) {
			// The driver's message quotes the duplicate value; it is not kept.
			return fmt.Errorf("product %d external id: %w", mapping.ProductID, apperrors.ErrProductExternalIDTaken)
		}
		return err
	}
	return nil
}

func (r *productExternalIDRepository) FindProductByIdentity(ctx context.Context, source, scope, externalID string) (*models.Product, error) {
	var product models.Product
	err := r.db.WithContext(ctx).Model(&models.Product{}).
		Joins("JOIN product_external_ids ON product_external_ids.product_id = products.id").
		Where("product_external_ids.source = ? AND product_external_ids.scope = ? AND product_external_ids.external_id = ?",
			source, scope, externalID).
		Take(&product).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &product, nil
}

func (r *productExternalIDRepository) DeleteForProduct(ctx context.Context, productID, mappingID uint) error {
	result := r.db.WithContext(ctx).
		Where("id = ? AND product_id = ?", mappingID, productID).
		Delete(&models.ProductExternalID{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *productExternalIDRepository) PurgeForProduct(ctx context.Context, productID uint) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("product_id = ?", productID).
		Delete(&models.ProductExternalID{})
	if result.Error != nil {
		return 0, fmt.Errorf("purge product_external_ids of product %d: %w", productID, result.Error)
	}
	return result.RowsAffected, nil
}
