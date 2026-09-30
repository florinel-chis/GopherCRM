package repository

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// setupCompanyDB gives each test a private schema on the shared in-memory
// DSN, the same way the label and customer export tests do.
func setupCompanyDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{
		Logger: logger.Discard,
	})
	require.NoError(t, err)

	require.NoError(t, db.Migrator().DropTable(&models.Deal{}, &models.Lead{}, &models.Customer{}, &models.Company{}, &models.User{}))
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Lead{}, &models.Customer{}, &models.Deal{}))

	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func createCompanyTestUser(t *testing.T, db *gorm.DB, email string) *models.User {
	t.Helper()
	user := &models.User{Email: email, Password: "x", FirstName: "Sales", LastName: "Rep", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(user).Error)
	return user
}

func createCompany(t *testing.T, db *gorm.DB, name, domain, industry, city string) *models.Company {
	t.Helper()
	company := &models.Company{Name: name, Domain: domain, Industry: industry, City: city}
	require.NoError(t, db.Create(company).Error)
	return company
}

func TestCompanyRepository_GetByIDPreloadsOwnerAndCounts(t *testing.T) {
	db := setupCompanyDB(t)
	repo := NewCompanyRepository(db)
	owner := createCompanyTestUser(t, db, "owner@example.com")

	company := &models.Company{Name: "Acme", OwnerID: &owner.ID}
	require.NoError(t, repo.Create(company))
	other := createCompany(t, db, "Other", "", "", "")

	for i, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		require.NoError(t, db.Create(&models.Customer{FirstName: "C", LastName: "X", Email: email, CompanyID: &company.ID}).Error)
		if i == 0 {
			// An erased customer no longer counts.
			require.NoError(t, db.Exec("UPDATE customers SET deleted_at = CURRENT_TIMESTAMP WHERE email = ?", email).Error)
		}
	}
	require.NoError(t, db.Create(&models.Lead{FirstName: "L", LastName: "X", Email: "l@example.com", OwnerID: owner.ID, CompanyID: &company.ID}).Error)
	require.NoError(t, db.Create(&models.Lead{FirstName: "L", LastName: "Y", Email: "m@example.com", OwnerID: owner.ID, CompanyID: &other.ID}).Error)

	got, err := repo.GetByID(company.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Owner)
	assert.Equal(t, "owner@example.com", got.Owner.Email)
	assert.Equal(t, int64(2), got.CustomerCount)
	assert.Equal(t, int64(1), got.LeadCount)

	unlinked, err := repo.GetByID(other.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), unlinked.CustomerCount)
	assert.Equal(t, int64(1), unlinked.LeadCount)

	_, err = repo.GetByID(999)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}

func TestCompanyRepository_ListSearchSortAndPaginate(t *testing.T) {
	db := setupCompanyDB(t)
	repo := NewCompanyRepository(db)

	createCompany(t, db, "Zeta Works", "zeta.example", "Manufacturing", "Cluj")
	createCompany(t, db, "Acme Software", "acme.example", "Software", "Berlin")
	createCompany(t, db, "Beta Labs", "beta.example", "Biotech", "Munich")

	// Default order is newest first and every row carries its counts.
	companies, total, err := repo.List(0, 20, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, companies, 3)
	assert.Equal(t, "Beta Labs", companies[0].Name)

	// Sort by name ascending, paginated: page 2 of size 2 holds the last one.
	companies, total, err = repo.List(2, 2, "", "name", "asc")
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, companies, 1)
	assert.Equal(t, "Zeta Works", companies[0].Name)

	// Search hits name, domain, industry and city; the total follows the search.
	for needle, want := range map[string]string{
		"acme":    "Acme Software",
		"beta.ex": "Beta Labs",
		"Manufac": "Zeta Works",
		"Munich":  "Beta Labs",
	} {
		companies, total, err = repo.List(0, 20, needle, "", "")
		require.NoErrorf(t, err, needle)
		assert.Equalf(t, int64(1), total, needle)
		require.Lenf(t, companies, 1, needle)
		assert.Equalf(t, want, companies[0].Name, needle)
	}

	// Nothing matches: an empty array, not nil.
	companies, total, err = repo.List(0, 20, "nothing-here", "", "")
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.NotNil(t, companies)
	assert.Empty(t, companies)
}

func TestCompanyRepository_ListRejectsUnknownSortColumns(t *testing.T) {
	db := setupCompanyDB(t)
	repo := NewCompanyRepository(db)

	for _, sortBy := range []string{"owner_id", "customer_count", "name; DROP TABLE companies", "phone"} {
		_, _, err := repo.List(0, 20, "", sortBy, "asc")
		assert.Errorf(t, err, "%q must be refused", sortBy)
	}
	for _, sortBy := range []string{"id", "name", "domain", "industry", "created_at", "updated_at"} {
		_, _, err := repo.List(0, 20, "", sortBy, "desc")
		assert.NoErrorf(t, err, "%q is allowed", sortBy)
	}
}

func TestCompanyRepository_ExistsByDomainIsCaseInsensitiveAmongLiveRows(t *testing.T) {
	db := setupCompanyDB(t)
	repo := NewCompanyRepository(db)
	company := createCompany(t, db, "Acme", "acme.example", "", "")

	exists, err := repo.ExistsByDomain("ACME.example", 0)
	require.NoError(t, err)
	assert.True(t, exists, "LOWER() on both sides makes SQLite agree with MySQL")

	exists, err = repo.ExistsByDomain("acme.example", company.ID)
	require.NoError(t, err)
	assert.False(t, exists, "the row itself is excluded on update")

	// A soft-deleted company releases its domain.
	require.NoError(t, repo.Delete(company.ID))
	exists, err = repo.ExistsByDomain("acme.example", 0)
	require.NoError(t, err)
	assert.False(t, exists)

	assert.True(t, errors.Is(repo.Delete(company.ID), gorm.ErrRecordNotFound), "deleting twice is a miss")
}

func TestCompanyRepository_UnlinkClearsLiveAndSoftDeletedRows(t *testing.T) {
	db := setupCompanyDB(t)
	repo := NewCompanyRepository(db)
	owner := createCompanyTestUser(t, db, "owner@example.com")
	company := createCompany(t, db, "Acme", "", "", "")
	other := createCompany(t, db, "Other", "", "", "")

	live := &models.Lead{FirstName: "L", LastName: "1", Email: "l1@example.com", OwnerID: owner.ID, CompanyID: &company.ID}
	erased := &models.Lead{FirstName: "L", LastName: "2", Email: "l2@example.com", OwnerID: owner.ID, CompanyID: &company.ID}
	elsewhere := &models.Lead{FirstName: "L", LastName: "3", Email: "l3@example.com", OwnerID: owner.ID, CompanyID: &other.ID}
	require.NoError(t, db.Create(live).Error)
	require.NoError(t, db.Create(erased).Error)
	require.NoError(t, db.Create(elsewhere).Error)
	require.NoError(t, db.Delete(erased).Error)
	customer := &models.Customer{FirstName: "C", LastName: "1", Email: "c1@example.com", CompanyID: &company.ID}
	require.NoError(t, db.Create(customer).Error)

	require.NoError(t, repo.UnlinkLeads(company.ID))
	require.NoError(t, repo.UnlinkCustomers(company.ID))

	// Fresh structs for each lookup: First() would otherwise add the primary
	// key already set on the struct to the condition.
	var reloadedLive, reloadedErased, reloadedElsewhere models.Lead
	require.NoError(t, db.First(&reloadedLive, live.ID).Error)
	assert.Nil(t, reloadedLive.CompanyID)
	require.NoError(t, db.Unscoped().First(&reloadedErased, erased.ID).Error)
	assert.Nil(t, reloadedErased.CompanyID, "soft-deleted rows are unlinked too")
	require.NoError(t, db.First(&reloadedElsewhere, elsewhere.ID).Error)
	require.NotNil(t, reloadedElsewhere.CompanyID)
	assert.Equal(t, other.ID, *reloadedElsewhere.CompanyID, "other companies' links are untouched")
	var reloaded models.Customer
	require.NoError(t, db.First(&reloaded, customer.ID).Error)
	assert.Nil(t, reloaded.CompanyID)
}

func TestCompanyRepository_ListLeadsScopesByOwnerAndListCustomersPaginates(t *testing.T) {
	db := setupCompanyDB(t)
	repo := NewCompanyRepository(db)
	alice := createCompanyTestUser(t, db, "alice@example.com")
	bob := createCompanyTestUser(t, db, "bob@example.com")
	company := createCompany(t, db, "Acme", "", "", "")

	require.NoError(t, db.Create(&models.Lead{FirstName: "A", LastName: "1", Email: "a1@example.com", OwnerID: alice.ID, CompanyID: &company.ID}).Error)
	require.NoError(t, db.Create(&models.Lead{FirstName: "A", LastName: "2", Email: "a2@example.com", OwnerID: alice.ID, CompanyID: &company.ID}).Error)
	require.NoError(t, db.Create(&models.Lead{FirstName: "B", LastName: "1", Email: "b1@example.com", OwnerID: bob.ID, CompanyID: &company.ID}).Error)
	require.NoError(t, db.Create(&models.Lead{FirstName: "B", LastName: "0", Email: "b0@example.com", OwnerID: bob.ID}).Error)

	leads, total, err := repo.ListLeads(company.ID, nil, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, leads, 3)
	require.NotNil(t, leads[0].Owner, "owners are preloaded")

	leads, total, err = repo.ListLeads(company.ID, &alice.ID, 0, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total, "the total follows the owner scope")
	require.Len(t, leads, 1)
	assert.Equal(t, alice.ID, leads[0].OwnerID)

	for i, email := range []string{"c1@example.com", "c2@example.com", "c3@example.com"} {
		require.NoError(t, db.Create(&models.Customer{FirstName: "C", LastName: string(rune('a' + i)), Email: email, CompanyID: &company.ID}).Error)
	}
	customers, total, err := repo.ListCustomers(company.ID, 2, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, customers, 1)

	customers, total, err = repo.ListCustomers(999, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.NotNil(t, customers)
}
