package repository

import (
	"strings"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
)

type companyRepository struct {
	db *gorm.DB
}

func NewCompanyRepository(db *gorm.DB) CompanyRepository {
	return &companyRepository{db: db}
}

func (r *companyRepository) Create(company *models.Company) error {
	return r.db.Create(company).Error
}

// GetByID returns the company with its owner preloaded and its customer and
// lead counts filled in.
func (r *companyRepository) GetByID(id uint) (*models.Company, error) {
	var company models.Company
	if err := r.db.Preload("Owner").First(&company, id).Error; err != nil {
		return nil, err
	}
	if err := r.fillCounts([]*models.Company{&company}); err != nil {
		return nil, err
	}
	return &company, nil
}

func (r *companyRepository) Update(company *models.Company) error {
	// Omit the association so a stale Owner copy on the struct is never written
	// back over the users table; the owner is carried by OwnerID alone.
	return r.db.Omit("Owner").Save(company).Error
}

// Delete soft-deletes the company and reports gorm.ErrRecordNotFound when no
// live row matched. The links from leads and customers are cleared by the
// service, in the same transaction, before this runs.
func (r *companyRepository) Delete(id uint) error {
	result := r.db.Delete(&models.Company{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// companySearchClause is the LIKE filter behind ?search=. Plain LIKE with a
// bound pattern, as the customers repository does it: it means the same thing
// on MySQL, MariaDB and SQLite, and the pattern is never interpolated.
const companySearchClause = "name LIKE ? OR domain LIKE ? OR industry LIKE ? OR city LIKE ?"

func (r *companyRepository) scopeSearch(db *gorm.DB, search string) *gorm.DB {
	if search == "" {
		return db
	}
	pattern := "%" + search + "%"
	return db.Where(companySearchClause, pattern, pattern, pattern, pattern)
}

// List returns one page of companies with their owners and counts, plus the
// total matching the same search. sortBy goes through the companies allowlist
// in utils.AllowedSortColumns; an unknown column is an error, never
// interpolated, and an empty one means newest first. The id follows the sort
// column in the same direction so tied rows page deterministically.
func (r *companyRepository) List(offset, limit int, search, sortBy, sortOrder string) ([]models.Company, int64, error) {
	column, direction, err := utils.ValidateSort("companies", sortBy, sortOrder)
	if err != nil {
		return nil, 0, err
	}

	var total int64
	if err := r.scopeSearch(r.db.Model(&models.Company{}), search).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	companies := []models.Company{}
	err = orderByColumnThenID(r.scopeSearch(r.db.Preload("Owner"), search), "companies", column, direction == "desc").
		Offset(offset).Limit(limit).
		Find(&companies).Error
	if err != nil {
		return nil, 0, err
	}

	refs := make([]*models.Company, 0, len(companies))
	for i := range companies {
		refs = append(refs, &companies[i])
	}
	if err := r.fillCounts(refs); err != nil {
		return nil, 0, err
	}
	return companies, total, nil
}

// ExistsByDomain reports whether another LIVE company already uses the domain,
// compared case-insensitively. excludeID lets an update ignore the row it is
// about to write; pass 0 for a create.
//
// Live rows only, on purpose: a soft-deleted company must not reserve its
// domain forever, which is why the index on the column is not unique.
//
// The comparison is a plain equality against the lower-cased argument so the
// planner can use idx_companies_domain (#109); LOWER(domain) hid the column
// from every index on SQLite and on MySQL/MariaDB alike. That is safe because
// the stored value is always lower case: the service normalises it on every
// create and update (NormalizeCompanyDomain), and the migration that added the
// index lower-cases any MySQL row written another way. MySQL and MariaDB also
// compare under a case-insensitive collation, so there the result is the same
// either way.
func (r *companyRepository) ExistsByDomain(domain string, excludeID uint) (bool, error) {
	query := r.db.Model(&models.Company{}).Where("domain = ?", strings.ToLower(domain))
	if excludeID != 0 {
		query = query.Where("id <> ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// UnlinkLeads clears company_id on every lead that points at the company,
// soft-deleted leads included: a deleted company must not dangle behind any
// row, live or not.
func (r *companyRepository) UnlinkLeads(companyID uint) error {
	return r.db.Unscoped().Model(&models.Lead{}).
		Where("company_id = ?", companyID).
		Update("company_id", nil).Error
}

// UnlinkCustomers is UnlinkLeads for customers.
func (r *companyRepository) UnlinkCustomers(companyID uint) error {
	return r.db.Unscoped().Model(&models.Customer{}).
		Where("company_id = ?", companyID).
		Update("company_id", nil).Error
}

// UnlinkDeals is UnlinkLeads for deals.
func (r *companyRepository) UnlinkDeals(companyID uint) error {
	return r.db.Unscoped().Model(&models.Deal{}).
		Where("company_id = ?", companyID).
		Update("company_id", nil).Error
}

// ListCustomers returns one page of the live customers linked to the company,
// newest first (id breaking ties), plus the total.
func (r *companyRepository) ListCustomers(companyID uint, offset, limit int) ([]models.Customer, int64, error) {
	base := r.db.Model(&models.Customer{}).Where("company_id = ?", companyID)

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	customers := []models.Customer{}
	err := orderByColumnThenID(r.db.Where("company_id = ?", companyID), "customers", "created_at", true).
		Offset(offset).Limit(limit).
		Find(&customers).Error
	return customers, total, err
}

// ListLeads returns one page of the live leads linked to the company, newest
// first (id breaking ties), with their owners preloaded, plus the total. A nil ownerID means every
// owner; a non-nil one narrows to that owner's leads, which is how the sales
// scoping is pushed down to SQL rather than filtered after the fact.
func (r *companyRepository) ListLeads(companyID uint, ownerID *uint, offset, limit int) ([]models.Lead, int64, error) {
	scope := func(db *gorm.DB) *gorm.DB {
		db = db.Where("company_id = ?", companyID)
		if ownerID != nil {
			db = db.Where("owner_id = ?", *ownerID)
		}
		return db
	}

	var total int64
	if err := scope(r.db.Model(&models.Lead{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	leads := []models.Lead{}
	err := orderByColumnThenID(scope(r.db.Preload("Owner")), "leads", "created_at", true).
		Offset(offset).Limit(limit).
		Find(&leads).Error
	return leads, total, err
}

// fillCounts sets CustomerCount, LeadCount and DealCount on each company from
// three grouped queries over the live rows. GORM's soft-delete scope applies
// because the queries go through Model(), so erased customers and leads and
// deleted deals are not counted. Separate queries rather than a LEFT JOIN …
// GROUP BY on the companies query, for
// the reason given on labelRepository.List: that shape is only legal under
// MySQL 8's ONLY_FULL_GROUP_BY by way of functional-dependency detection.
func (r *companyRepository) fillCounts(companies []*models.Company) error {
	if len(companies) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(companies))
	for _, company := range companies {
		ids = append(ids, company.ID)
	}

	customers, err := r.countByCompany(&models.Customer{}, ids)
	if err != nil {
		return err
	}
	leads, err := r.countByCompany(&models.Lead{}, ids)
	if err != nil {
		return err
	}
	deals, err := r.countByCompany(&models.Deal{}, ids)
	if err != nil {
		return err
	}
	for _, company := range companies {
		company.CustomerCount = customers[company.ID]
		company.LeadCount = leads[company.ID]
		company.DealCount = deals[company.ID]
	}
	return nil
}

func (r *companyRepository) countByCompany(model interface{}, ids []uint) (map[uint]int64, error) {
	type companyCount struct {
		CompanyID uint
		Total     int64
	}
	rows := []companyCount{}
	err := r.db.Model(model).
		Select("company_id AS company_id, COUNT(id) AS total").
		Where("company_id IN ?", ids).
		Group("company_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	counts := make(map[uint]int64, len(rows))
	for _, row := range rows {
		counts[row.CompanyID] = row.Total
	}
	return counts, nil
}

func (r *companyRepository) WithTx(tx *gorm.DB) CompanyRepository {
	return &companyRepository{db: tx}
}
