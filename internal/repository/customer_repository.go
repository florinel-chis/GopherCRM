package repository

import (
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type customerRepository struct {
	db *gorm.DB
}

func NewCustomerRepository(db *gorm.DB) CustomerRepository {
	return &customerRepository{db: db}
}

func (r *customerRepository) Create(customer *models.Customer) error {
	if err := r.db.Create(customer).Error; err != nil {
		// Defense in depth: the unique index on customers.email is NOT scoped to
		// deleted_at, so a soft-deleted row still occupies the address. Any
		// duplicate-key violation here must surface as the ErrDuplicateEmail
		// sentinel rather than as a raw driver string, which would leak the
		// index name and driver error code to the caller.
		if isDuplicateKeyError(err) {
			return fmt.Errorf("customer with this email already exists: %w", apperrors.ErrDuplicateEmail)
		}
		return err
	}
	return nil
}

func (r *customerRepository) GetByID(id uint) (*models.Customer, error) {
	var customer models.Customer
	err := r.db.Preload("User").Preload("CompanyRecord").First(&customer, id).Error
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (r *customerRepository) GetByEmail(email string) (*models.Customer, error) {
	var customer models.Customer
	err := r.db.Where("email = ?", email).First(&customer).Error
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

// GetByEmailUnscoped looks up a customer by email including soft-deleted rows.
// Use it for duplicate-email pre-checks: the database unique index does not
// ignore soft-deleted rows, so an ordinary (scoped) lookup would report "free"
// for an address that the database will still reject on insert.
func (r *customerRepository) GetByEmailUnscoped(email string) (*models.Customer, error) {
	var customer models.Customer
	err := r.db.Unscoped().Where("email = ?", email).First(&customer).Error
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (r *customerRepository) GetByUserID(userID uint) (*models.Customer, error) {
	var customer models.Customer
	err := r.db.Where("user_id = ?", userID).First(&customer).Error
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (r *customerRepository) Update(customer *models.Customer) error {
	if err := r.db.Save(customer).Error; err != nil {
		if isDuplicateKeyError(err) {
			return fmt.Errorf("customer with this email already exists: %w", apperrors.ErrDuplicateEmail)
		}
		return err
	}
	return nil
}

// Delete erases a customer under the GDPR right to erasure (Article 17).
//
// As with users, this is NOT deactivation: it is irreversible. Every
// personal-data field on the customer — names, email, phone, company, website, position,
// the full postal address and the free-text notes — is overwritten in place,
// and only then is the row soft-deleted, in the SAME transaction, so a crash
// cannot leave a live anonymised record behind.
//
// The row survives (soft-deleted) because tickets reference customers by
// foreign key; erasure is achieved by making the retained row non-personal, not
// by destroying business history.
//
// The customer is the same person as every lead converted into it and, when
// customers.user_id names a customer-role account, as that login: all of them
// are erased with it in the same unit (erasure_cascade.go). A staff account
// in user_id is never erased this way, and AssignedToID (the account manager)
// and CompanyID are business links that stay. UserID itself is kept: it is an
// id, not personal data, and it lets a repeated erasure find the login again.
//
// The free-text Notes column is the highest-risk field here: it is the one
// place where unstructured personal data accumulates, so it is cleared
// outright rather than trimmed.
//
// As with users, the unit is a savepoint of the caller's transaction when this
// repository was obtained through WithTx and a fresh transaction otherwise.
// See erasure.go for the shared machinery and the full GDPR rationale.
func (r *customerRepository) Delete(id uint) error {
	return eraseGraph(r.db, true, func(c *erasureCascade, tx *gorm.DB) error {
		return c.eraseCustomer(tx, id)
	})
}

// customerErasurePlan lists what counts as personal data on a customer. It is
// the only copy.
func customerErasurePlan() erasurePlan {
	return erasurePlan{
		Model:       &models.Customer{},
		EmailColumn: "email",
		Scrub: map[string]interface{}{
			"first_name":  "",
			"last_name":   "",
			"phone":       "",
			"company":     "",
			"website":     "",
			"position":    "",
			"address":     "",
			"city":        "",
			"state":       "",
			"country":     "",
			"postal_code": "",
			"notes":       "",
		},
	}
}

func (r *customerRepository) List(offset, limit int) ([]models.Customer, error) {
	var customers []models.Customer
	err := r.db.Preload("User").Offset(offset).Limit(limit).Find(&customers).Error
	return customers, err
}

func (r *customerRepository) ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Customer, error) {
	var customers []models.Customer
	query := r.db
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	if sortBy != "" {
		orderClause, err := utils.SafeOrderClause("customers", sortBy, sortOrder)
		if err != nil {
			return nil, err
		}
		if orderClause != "" {
			query = query.Order(orderClause)
		}
	}
	err := query.Offset(offset).Limit(limit).Find(&customers).Error
	return customers, err
}

func (r *customerRepository) Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Customer, error) {
	var customers []models.Customer
	db := r.db
	for _, preload := range preloads {
		db = db.Preload(preload)
	}
	searchPattern := "%" + query + "%"
	db = db.Where(
		"first_name LIKE ? OR last_name LIKE ? OR email LIKE ? OR company LIKE ? OR phone LIKE ? OR notes LIKE ?",
		searchPattern, searchPattern, searchPattern, searchPattern, searchPattern, searchPattern,
	)
	if sortBy != "" {
		orderClause, err := utils.SafeOrderClause("customers", sortBy, sortOrder)
		if err != nil {
			return nil, err
		}
		if orderClause != "" {
			db = db.Order(orderClause)
		}
	}
	err := db.Offset(offset).Limit(limit).Find(&customers).Error
	return customers, err
}

// ListAllForExport returns every customer matching the optional search term,
// with no pagination at all — the export is a whole-book download, and a page
// boundary in the middle of it would produce a file that looks complete and is
// not.
//
// The rows are read by ONE ordered statement. An earlier version read them in
// primary-key batches through GORM's FindInBatches while also applying the
// requested ORDER BY; FindInBatches continues each batch with `id > last id of
// the previous batch`, which is only a valid cursor when the rows are ordered
// by id. Under any other ordering the continuation skipped every customer whose
// id was below the previous batch's last id and could repeat others, so a
// sorted export of more than one batch silently lost rows (#105). A single
// statement has no batch boundary to get wrong, is one consistent read on both
// MySQL (InnoDB) and SQLite, and costs the server one sort instead of one per
// batch. The result still lands in one slice before the handler writes a byte,
// which is the same memory footprint the batched read accumulated; streaming
// the file is deliberately not done, because a mid-stream failure would arrive
// after a 200 and half a file.
//
// Ordering is deterministic. With a sort column the rows are ordered by that
// column and then by id, both in the requested direction, so duplicate values
// (two customers at the same company, two rows created in the same millisecond,
// two emails equal under MySQL's case-insensitive collation, NULLs in a legacy
// nullable column) always come out in the same order and a descending export is
// the exact reverse of the ascending one. Without a sort column the rows are
// ordered by id ascending, which is what the primary-key batching produced.
//
// Soft-deleted rows are excluded by GORM's own scope: an erased customer must
// never reappear in an export.
//
// sortBy goes through the same allowlist as every other sorted query. An
// unrecognised column is an error, never interpolated.
func (r *customerRepository) ListAllForExport(search, sortBy, sortOrder string) ([]models.Customer, error) {
	db := r.db.Model(&models.Customer{})

	if search != "" {
		searchPattern := "%" + search + "%"
		db = db.Where(
			"first_name LIKE ? OR last_name LIKE ? OR email LIKE ? OR company LIKE ? OR phone LIKE ? OR notes LIKE ?",
			searchPattern, searchPattern, searchPattern, searchPattern, searchPattern, searchPattern,
		)
	}

	descending := false
	if sortBy != "" {
		column, direction, err := utils.ValidateSort("customers", sortBy, sortOrder)
		if err != nil {
			return nil, err
		}
		descending = direction == "desc"
		db = db.Order(clause.OrderByColumn{
			Column: clause.Column{Table: clause.CurrentTable, Name: column},
			Desc:   descending,
		})
	}
	// The id tie-breaker makes the order total: no two live rows share one.
	db = db.Order(clause.OrderByColumn{
		Column: clause.Column{Table: clause.CurrentTable, Name: "id"},
		Desc:   descending,
	})

	customers := make([]models.Customer, 0)
	if err := db.Find(&customers).Error; err != nil {
		return nil, err
	}
	return customers, nil
}

func (r *customerRepository) CountSearch(query string) (int64, error) {
	var count int64
	searchPattern := "%" + query + "%"
	err := r.db.Model(&models.Customer{}).Where(
		"first_name LIKE ? OR last_name LIKE ? OR email LIKE ? OR company LIKE ? OR phone LIKE ? OR notes LIKE ?",
		searchPattern, searchPattern, searchPattern, searchPattern, searchPattern, searchPattern,
	).Count(&count).Error
	return count, err
}

func (r *customerRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.Customer{}).Count(&count).Error
	return count, err
}

func (r *customerRepository) WithTx(tx *gorm.DB) CustomerRepository {
	return &customerRepository{db: tx}
}
