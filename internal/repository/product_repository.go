package repository

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
)

type productRepository struct {
	db *gorm.DB
}

func NewProductRepository(db *gorm.DB) ProductRepository {
	return &productRepository{db: db}
}

func (r *productRepository) Create(product *models.Product) error {
	return r.db.Create(product).Error
}

func (r *productRepository) GetByID(id uint) (*models.Product, error) {
	var product models.Product
	if err := r.db.First(&product, id).Error; err != nil {
		return nil, err
	}
	return &product, nil
}

// Update is a full save: Save writes every column, zero values included, which
// is what a price of 0 or an is_active of false needs.
func (r *productRepository) Update(product *models.Product) error {
	return r.db.Save(product).Error
}

func (r *productRepository) Delete(id uint) error {
	result := r.db.Delete(&models.Product{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// productSearchClause is the LIKE filter behind ?search=: a bound pattern,
// never interpolated, meaning the same thing on MySQL, MariaDB and SQLite
// (case sensitivity follows the collation, as on the other searches).
const productSearchClause = "name LIKE ? OR sku LIKE ?"

// scope applies the filter to a query. Every predicate is a bound parameter.
func (r *productRepository) scope(db *gorm.DB, filter ProductListFilter) *gorm.DB {
	if filter.Search != "" {
		pattern := "%" + filter.Search + "%"
		db = db.Where(productSearchClause, pattern, pattern)
	}
	if filter.IsActive != nil {
		db = db.Where("is_active = ?", *filter.IsActive)
	}
	if filter.Currency != "" {
		db = db.Where("currency = ?", filter.Currency)
	}
	return db
}

// List returns one page of live products plus the total matching the same
// filter. The sort column goes through the products allowlist; an unknown
// column is an error, never interpolated. The primary key is added as a
// tie-breaker so pages are stable when the sort column repeats.
func (r *productRepository) List(offset, limit int, filter ProductListFilter) ([]models.Product, int64, error) {
	column, direction, err := utils.ValidateSort("products", filter.SortBy, filter.SortOrder)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	if err := r.scope(r.db.Model(&models.Product{}), filter).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	products := []models.Product{}
	err = r.scope(r.db, filter).
		Order(column + " " + direction).
		Order("id " + direction).
		Offset(offset).Limit(limit).
		Find(&products).Error
	if err != nil {
		return nil, 0, err
	}
	return products, total, nil
}

func (r *productRepository) WithTx(tx *gorm.DB) ProductRepository {
	return &productRepository{db: tx}
}
