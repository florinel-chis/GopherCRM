package repository

import (
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
)

type dealRepository struct {
	db *gorm.DB
}

func NewDealRepository(db *gorm.DB) DealRepository {
	return &dealRepository{db: db}
}

func (r *dealRepository) Create(deal *models.Deal) error {
	// Omit the associations so a caller that set Company or Owner on the struct
	// for its own use never has them written back over their tables.
	return r.db.Omit("Owner", "Company", "Customer", "Lead").Create(deal).Error
}

// dealPreloads are the associations the detail endpoint and the lists carry:
// ids plus display names.
var dealPreloads = []string{"Owner", "Company", "Customer", "Lead"}

func (r *dealRepository) withPreloads(db *gorm.DB) *gorm.DB {
	for _, preload := range dealPreloads {
		db = db.Preload(preload)
	}
	return db
}

func (r *dealRepository) GetByID(id uint) (*models.Deal, error) {
	var deal models.Deal
	if err := r.withPreloads(r.db).First(&deal, id).Error; err != nil {
		return nil, err
	}
	return &deal, nil
}

// Update is a full save: Save writes every column, zero values and NULLs
// included, which is what clearing closed_at, lost_reason or a link needs.
func (r *dealRepository) Update(deal *models.Deal) error {
	return r.db.Omit("Owner", "Company", "Customer", "Lead").Save(deal).Error
}

func (r *dealRepository) Delete(id uint) error {
	result := r.db.Delete(&models.Deal{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// dealSearchClause is the LIKE filter behind ?search=: a bound pattern, never
// interpolated, meaning the same thing on MySQL, MariaDB and SQLite (case
// sensitivity follows the collation, as on the other searches).
const dealSearchClause = "title LIKE ? OR notes LIKE ?"

// scope applies the filter to a query. Every predicate is a bound parameter;
// the stage set behind Open is a fixed NOT IN over the two closed stages.
func (r *dealRepository) scope(db *gorm.DB, filter DealListFilter) *gorm.DB {
	if filter.Search != "" {
		pattern := "%" + filter.Search + "%"
		db = db.Where(dealSearchClause, pattern, pattern)
	}
	if filter.Stage != "" {
		db = db.Where("stage = ?", filter.Stage)
	}
	if filter.Open {
		db = db.Where("stage NOT IN ?", []models.DealStage{models.DealStageWon, models.DealStageLost})
	}
	if filter.CompanyID != nil {
		db = db.Where("company_id = ?", *filter.CompanyID)
	}
	if filter.CustomerID != nil {
		db = db.Where("customer_id = ?", *filter.CustomerID)
	}
	if filter.OwnerID != nil {
		db = db.Where("owner_id = ?", *filter.OwnerID)
	}
	return db
}

// List returns one page of deals with their associations, plus the total
// matching the same filter. The sort column goes through the deals allowlist;
// an unknown column is an error, never interpolated. The primary key is added
// as a tie-breaker so pages are stable when the sort column repeats.
func (r *dealRepository) List(offset, limit int, filter DealListFilter) ([]models.Deal, int64, error) {
	column, direction, err := utils.ValidateSort("deals", filter.SortBy, filter.SortOrder)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	if err := r.scope(r.db.Model(&models.Deal{}), filter).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	deals := []models.Deal{}
	err = r.scope(r.withPreloads(r.db), filter).
		Order(column + " " + direction).
		Order("id " + direction).
		Offset(offset).Limit(limit).
		Find(&deals).Error
	if err != nil {
		return nil, 0, err
	}
	return deals, total, nil
}

func (r *dealRepository) CreateStageChange(change *models.DealStageChange) error {
	return r.db.Omit("ChangedBy").Create(change).Error
}

// ListStageChanges returns the history oldest first. changed_at is the
// ordering, with the id as tie-breaker for two rows written in the same
// instant.
func (r *dealRepository) ListStageChanges(dealID uint) ([]models.DealStageChange, error) {
	changes := []models.DealStageChange{}
	err := r.db.Preload("ChangedBy").
		Where("deal_id = ?", dealID).
		Order("changed_at ASC").Order("id ASC").
		Find(&changes).Error
	return changes, err
}

// dealPipelineSelect is the aggregate behind Pipeline. Only the grouped
// columns and aggregates are selected, so it is valid under MySQL's
// ONLY_FULL_GROUP_BY; the arithmetic is integer on every engine (BIGINT ×
// INT). MySQL and MariaDB return SUM over an integer column as DECIMAL with
// no fractional part, which scans into int64 as SQLite's INTEGER does. The
// aliases avoid the names of existing columns and any reserved word.
const dealPipelineSelect = "stage, currency, COUNT(*) AS deal_count, " +
	"SUM(amount_cents) AS amount_sum, SUM(amount_cents * probability) AS weighted_sum"

// Pipeline groups the live deals (soft-deleted ones are excluded by the model
// scope) by stage and currency. The owner and company filters are the same
// bound predicates as the list's.
func (r *dealRepository) Pipeline(filter DealPipelineFilter) ([]models.DealPipelineRow, error) {
	rows := []models.DealPipelineRow{}
	err := r.scope(r.db.Model(&models.Deal{}), DealListFilter{OwnerID: filter.OwnerID, CompanyID: filter.CompanyID}).
		Select(dealPipelineSelect).
		Group("stage").Group("currency").
		Order("stage").Order("currency").
		Scan(&rows).Error
	return rows, err
}

// WonBetween groups the live won deals closed in [from, to) by currency. The
// range is two bound parameters and no date function, so it means the same on
// MySQL, MariaDB and SQLite. On SQLite closed_at is text; the service writes
// it in UTC and the bounds are converted to UTC here, so the text comparison
// orders the same way the instants do.
func (r *dealRepository) WonBetween(ownerID *uint, from, to time.Time) ([]models.DealWonRow, error) {
	rows := []models.DealWonRow{}
	err := r.scope(r.db.Model(&models.Deal{}), DealListFilter{OwnerID: ownerID}).
		Select("currency, COUNT(*) AS deal_count, SUM(amount_cents) AS amount_sum").
		Where("stage = ?", models.DealStageWon).
		Where("closed_at >= ? AND closed_at < ?", from.UTC(), to.UTC()).
		Group("currency").
		Order("currency").
		Scan(&rows).Error
	return rows, err
}

func (r *dealRepository) WithTx(tx *gorm.DB) DealRepository {
	return &dealRepository{db: tx}
}
