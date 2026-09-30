package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// setupDealDB gives each test a private schema on the shared in-memory DSN,
// with foreign keys on, the way the company repository tests do.
func setupDealDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{
		Logger: logger.Discard,
	})
	require.NoError(t, err)

	require.NoError(t, db.Migrator().DropTable(&models.DealStageChange{}, &models.Deal{}, &models.Lead{}, &models.Customer{}, &models.Company{}, &models.User{}))
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Lead{}, &models.Customer{}, &models.Deal{}, &models.DealStageChange{}))

	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func createDealTestUser(t *testing.T, db *gorm.DB, email string) *models.User {
	t.Helper()
	user := &models.User{Email: email, Password: "x", FirstName: "Sales", LastName: "Rep", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(user).Error)
	return user
}

func createDeal(t *testing.T, db *gorm.DB, deal *models.Deal) *models.Deal {
	t.Helper()
	if deal.Currency == "" {
		deal.Currency = "EUR"
	}
	if deal.Stage == "" {
		deal.Stage = models.DealStageQualification
	}
	require.NoError(t, db.Create(deal).Error)
	return deal
}

func dealTitles(deals []models.Deal) []string {
	titles := make([]string, 0, len(deals))
	for _, deal := range deals {
		titles = append(titles, deal.Title)
	}
	return titles
}

func TestDealRepository_GetByIDPreloadsTheAssociations(t *testing.T) {
	db := setupDealDB(t)
	repo := NewDealRepository(db)
	owner := createDealTestUser(t, db, "owner@example.com")
	company := &models.Company{Name: "Acme"}
	require.NoError(t, db.Create(company).Error)
	customer := &models.Customer{FirstName: "C", LastName: "One", Email: "c@example.com"}
	require.NoError(t, db.Create(customer).Error)
	lead := &models.Lead{FirstName: "L", LastName: "One", Email: "l@example.com", OwnerID: owner.ID}
	require.NoError(t, db.Create(lead).Error)

	date, _ := models.ParseDealDate("2026-11-30")
	deal := &models.Deal{
		Title: "Acme rollout", Currency: "EUR", AmountCents: 1250000, Probability: 40, Stage: models.DealStageProposal,
		ExpectedCloseDate: &date, OwnerID: owner.ID, CompanyID: &company.ID, CustomerID: &customer.ID, LeadID: &lead.ID,
		// A stale association on the struct must not be written back.
		Owner: &models.User{BaseModel: models.BaseModel{ID: owner.ID}, Email: "tampered@example.com", Password: "x", FirstName: "X", LastName: "Y"},
	}
	require.NoError(t, repo.Create(deal))

	got, err := repo.GetByID(deal.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Owner)
	assert.Equal(t, "owner@example.com", got.Owner.Email, "Create must not write the association back over users")
	require.NotNil(t, got.Company)
	assert.Equal(t, "Acme", got.Company.Name)
	require.NotNil(t, got.Customer)
	assert.Equal(t, "c@example.com", got.Customer.Email)
	require.NotNil(t, got.Lead)
	assert.Equal(t, "l@example.com", got.Lead.Email)
	assert.Equal(t, int64(1250000), got.AmountCents)
	require.NotNil(t, got.ExpectedCloseDate)
	assert.Equal(t, "2026-11-30", got.ExpectedCloseDate.Format(models.DealDateLayout), "the DATE column round-trips as the same calendar day")

	_, err = repo.GetByID(999)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}

// Update is a full save: clearing closed_at, lost_reason and a link has to
// reach the database as NULL and "", not be skipped as zero values.
func TestDealRepository_UpdateWritesClearedValues(t *testing.T) {
	db := setupDealDB(t)
	repo := NewDealRepository(db)
	owner := createDealTestUser(t, db, "owner@example.com")
	company := &models.Company{Name: "Acme"}
	require.NoError(t, db.Create(company).Error)
	now := time.Now()
	deal := createDeal(t, db, &models.Deal{Title: "Lost one", Stage: models.DealStageLost, Probability: 0, ClosedAt: &now, LostReason: "budget", OwnerID: owner.ID, CompanyID: &company.ID})

	deal.Stage = models.DealStageProposal
	deal.Probability = 40
	deal.ClosedAt = nil
	deal.LostReason = ""
	deal.CompanyID = nil
	require.NoError(t, repo.Update(deal))

	var reloaded models.Deal
	require.NoError(t, db.First(&reloaded, deal.ID).Error)
	assert.Equal(t, models.DealStageProposal, reloaded.Stage)
	assert.Equal(t, 40, reloaded.Probability)
	assert.Nil(t, reloaded.ClosedAt)
	assert.Equal(t, "", reloaded.LostReason)
	assert.Nil(t, reloaded.CompanyID)

	// ...and a probability of 0 is stored as 0, not replaced by a default.
	deal.Stage = models.DealStageLost
	deal.Probability = 0
	require.NoError(t, repo.Update(deal))
	require.NoError(t, db.First(&reloaded, deal.ID).Error)
	assert.Equal(t, 0, reloaded.Probability)
}

func TestDealRepository_ListFiltersSortsAndPaginates(t *testing.T) {
	db := setupDealDB(t)
	repo := NewDealRepository(db)
	alice := createDealTestUser(t, db, "alice@example.com")
	bob := createDealTestUser(t, db, "bob@example.com")
	acme := &models.Company{Name: "Acme"}
	require.NoError(t, db.Create(acme).Error)
	globex := &models.Company{Name: "Globex"}
	require.NoError(t, db.Create(globex).Error)
	customer := &models.Customer{FirstName: "C", LastName: "One", Email: "c@example.com"}
	require.NoError(t, db.Create(customer).Error)

	d1, _ := models.ParseDealDate("2026-10-01")
	d2, _ := models.ParseDealDate("2026-12-01")
	createDeal(t, db, &models.Deal{Title: "Alpha rollout", Notes: "phase one", Stage: models.DealStageQualification, AmountCents: 100, Probability: 10, OwnerID: alice.ID, CompanyID: &acme.ID, ExpectedCloseDate: &d2})
	createDeal(t, db, &models.Deal{Title: "Beta renewal", Notes: "keep the discount", Stage: models.DealStageNegotiation, AmountCents: 300, Probability: 70, OwnerID: alice.ID, CompanyID: &globex.ID, CustomerID: &customer.ID, ExpectedCloseDate: &d1})
	createDeal(t, db, &models.Deal{Title: "Gamma upsell", Stage: models.DealStageWon, AmountCents: 200, Probability: 100, OwnerID: bob.ID, CompanyID: &acme.ID})
	createDeal(t, db, &models.Deal{Title: "Delta pilot", Stage: models.DealStageLost, AmountCents: 50, Probability: 0, OwnerID: bob.ID})
	erased := createDeal(t, db, &models.Deal{Title: "Epsilon gone", Stage: models.DealStageProposal, AmountCents: 999, Probability: 40, OwnerID: bob.ID, CompanyID: &acme.ID})
	require.NoError(t, repo.Delete(erased.ID))

	// Default order is newest first; soft-deleted rows are out; associations
	// come along.
	deals, total, err := repo.List(0, 20, DealListFilter{})
	require.NoError(t, err)
	assert.Equal(t, int64(4), total)
	assert.Equal(t, []string{"Delta pilot", "Gamma upsell", "Beta renewal", "Alpha rollout"}, dealTitles(deals))
	require.NotNil(t, deals[3].Company)
	assert.Equal(t, "Acme", deals[3].Company.Name)
	require.NotNil(t, deals[3].Owner)

	// Sorting through the allowlist, ascending and descending.
	deals, _, err = repo.List(0, 20, DealListFilter{SortBy: "amount_cents", SortOrder: "asc"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Delta pilot", "Alpha rollout", "Gamma upsell", "Beta renewal"}, dealTitles(deals))
	deals, _, err = repo.List(0, 20, DealListFilter{SortBy: "title", SortOrder: "desc"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Gamma upsell", "Delta pilot", "Beta renewal", "Alpha rollout"}, dealTitles(deals))
	deals, _, err = repo.List(0, 20, DealListFilter{SortBy: "expected_close_date", SortOrder: "asc", Open: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"Beta renewal", "Alpha rollout"}, dealTitles(deals), "a date column sorts as a date on SQLite too")

	// Pagination: page 2 of size 3 holds the last one, the total is unchanged.
	deals, total, err = repo.List(3, 3, DealListFilter{SortBy: "title", SortOrder: "asc"})
	require.NoError(t, err)
	assert.Equal(t, int64(4), total)
	assert.Equal(t, []string{"Gamma upsell"}, dealTitles(deals))

	// Search covers title and notes; the total follows the filter.
	deals, total, err = repo.List(0, 20, DealListFilter{Search: "discount"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, []string{"Beta renewal"}, dealTitles(deals))
	deals, _, err = repo.List(0, 20, DealListFilter{Search: "renewal"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Beta renewal"}, dealTitles(deals))

	// Stage, open, company, customer and owner, alone and combined.
	deals, _, err = repo.List(0, 20, DealListFilter{Stage: models.DealStageWon})
	require.NoError(t, err)
	assert.Equal(t, []string{"Gamma upsell"}, dealTitles(deals))
	deals, total, err = repo.List(0, 20, DealListFilter{Open: true})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.ElementsMatch(t, []string{"Alpha rollout", "Beta renewal"}, dealTitles(deals), "open excludes won and lost")
	deals, _, err = repo.List(0, 20, DealListFilter{CompanyID: &acme.ID})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"Alpha rollout", "Gamma upsell"}, dealTitles(deals))
	deals, _, err = repo.List(0, 20, DealListFilter{CustomerID: &customer.ID})
	require.NoError(t, err)
	assert.Equal(t, []string{"Beta renewal"}, dealTitles(deals))
	deals, total, err = repo.List(0, 20, DealListFilter{OwnerID: &bob.ID})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.ElementsMatch(t, []string{"Gamma upsell", "Delta pilot"}, dealTitles(deals), "per-owner scoping is pushed into SQL")
	deals, _, err = repo.List(0, 20, DealListFilter{CompanyID: &acme.ID, OwnerID: &alice.ID})
	require.NoError(t, err)
	assert.Equal(t, []string{"Alpha rollout"}, dealTitles(deals), "filters combine")
	deals, _, err = repo.List(0, 20, DealListFilter{CompanyID: &acme.ID, Open: true, Search: "phase"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Alpha rollout"}, dealTitles(deals))

	// Nothing matches: an empty array, not nil.
	deals, total, err = repo.List(0, 20, DealListFilter{Search: "nothing-here"})
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.NotNil(t, deals)
	assert.Empty(t, deals)
}

func TestDealRepository_ListRejectsUnknownSortColumns(t *testing.T) {
	db := setupDealDB(t)
	repo := NewDealRepository(db)

	for _, sortBy := range []string{"owner_id", "currency", "title; DROP TABLE deals", "notes"} {
		_, _, err := repo.List(0, 20, DealListFilter{SortBy: sortBy, SortOrder: "asc"})
		assert.Errorf(t, err, "%q must be refused", sortBy)
	}
	for _, sortBy := range []string{"id", "title", "stage", "amount_cents", "probability", "expected_close_date", "closed_at", "created_at", "updated_at"} {
		_, _, err := repo.List(0, 20, DealListFilter{SortBy: sortBy, SortOrder: "desc"})
		assert.NoErrorf(t, err, "%q is allowed", sortBy)
	}
}

func TestDealRepository_DeleteIsSoftAndKeepsTheHistory(t *testing.T) {
	db := setupDealDB(t)
	repo := NewDealRepository(db)
	owner := createDealTestUser(t, db, "owner@example.com")
	deal := createDeal(t, db, &models.Deal{Title: "Doomed", Probability: 10, OwnerID: owner.ID})
	require.NoError(t, repo.CreateStageChange(&models.DealStageChange{DealID: deal.ID, ToStage: deal.Stage, ChangedByID: owner.ID, ChangedAt: time.Now()}))

	require.NoError(t, repo.Delete(deal.ID))

	var gone models.Deal
	assert.True(t, errors.Is(db.First(&gone, deal.ID).Error, gorm.ErrRecordNotFound), "hidden from ordinary queries")
	require.NoError(t, db.Unscoped().First(&gone, deal.ID).Error)
	assert.True(t, gone.DeletedAt.Valid, "soft-deleted, not destroyed")

	changes, err := repo.ListStageChanges(deal.ID)
	require.NoError(t, err)
	assert.Len(t, changes, 1, "history rows survive the delete")

	assert.True(t, errors.Is(repo.Delete(deal.ID), gorm.ErrRecordNotFound), "deleting twice is a miss")
}

func TestDealRepository_StageChangesAreListedOldestFirstWithTheUser(t *testing.T) {
	db := setupDealDB(t)
	repo := NewDealRepository(db)
	alice := createDealTestUser(t, db, "alice@example.com")
	bob := createDealTestUser(t, db, "bob@example.com")
	deal := createDeal(t, db, &models.Deal{Title: "History", Probability: 10, OwnerID: alice.ID})
	other := createDeal(t, db, &models.Deal{Title: "Other", Probability: 10, OwnerID: alice.ID})

	base := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	qualification := models.DealStageQualification
	proposal := models.DealStageProposal
	// Written out of order on purpose: the listing sorts by changed_at.
	require.NoError(t, repo.CreateStageChange(&models.DealStageChange{DealID: deal.ID, FromStage: &qualification, ToStage: proposal, ChangedByID: bob.ID, ChangedAt: base.Add(time.Hour)}))
	require.NoError(t, repo.CreateStageChange(&models.DealStageChange{DealID: deal.ID, FromStage: nil, ToStage: qualification, ChangedByID: alice.ID, ChangedAt: base}))
	require.NoError(t, repo.CreateStageChange(&models.DealStageChange{DealID: deal.ID, FromStage: &proposal, ToStage: models.DealStageWon, ChangedByID: alice.ID, ChangedAt: base.Add(2 * time.Hour)}))
	require.NoError(t, repo.CreateStageChange(&models.DealStageChange{DealID: other.ID, FromStage: nil, ToStage: qualification, ChangedByID: alice.ID, ChangedAt: base}))

	changes, err := repo.ListStageChanges(deal.ID)
	require.NoError(t, err)
	require.Len(t, changes, 3, "only this deal's rows")
	assert.Nil(t, changes[0].FromStage, "the creating row has no from_stage")
	assert.Equal(t, qualification, changes[0].ToStage)
	require.NotNil(t, changes[0].ChangedBy)
	assert.Equal(t, "alice@example.com", changes[0].ChangedBy.Email)
	require.NotNil(t, changes[1].FromStage)
	assert.Equal(t, qualification, *changes[1].FromStage)
	assert.Equal(t, proposal, changes[1].ToStage)
	assert.Equal(t, "bob@example.com", changes[1].ChangedBy.Email)
	assert.Equal(t, models.DealStageWon, changes[2].ToStage)

	changes, err = repo.ListStageChanges(999)
	require.NoError(t, err)
	assert.NotNil(t, changes)
	assert.Empty(t, changes)
}

// The company side of deals: the count on a company and the unlink on delete.
func TestCompanyRepository_DealCountAndUnlinkDeals(t *testing.T) {
	db := setupDealDB(t)
	companies := NewCompanyRepository(db)
	deals := NewDealRepository(db)
	owner := createDealTestUser(t, db, "owner@example.com")
	acme := &models.Company{Name: "Acme"}
	require.NoError(t, db.Create(acme).Error)
	other := &models.Company{Name: "Other"}
	require.NoError(t, db.Create(other).Error)

	live := createDeal(t, db, &models.Deal{Title: "Live", Probability: 10, OwnerID: owner.ID, CompanyID: &acme.ID})
	gone := createDeal(t, db, &models.Deal{Title: "Gone", Probability: 10, OwnerID: owner.ID, CompanyID: &acme.ID})
	elsewhere := createDeal(t, db, &models.Deal{Title: "Elsewhere", Probability: 10, OwnerID: owner.ID, CompanyID: &other.ID})
	require.NoError(t, deals.Delete(gone.ID))

	got, err := companies.GetByID(acme.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.DealCount, "live deals only")
	listed, _, err := companies.List(0, 20, "", "name", "asc")
	require.NoError(t, err)
	require.Len(t, listed, 2)
	assert.Equal(t, int64(1), listed[0].DealCount)
	assert.Equal(t, int64(1), listed[1].DealCount)

	require.NoError(t, companies.UnlinkDeals(acme.ID))

	var reloadedLive, reloadedGone, reloadedElsewhere models.Deal
	require.NoError(t, db.First(&reloadedLive, live.ID).Error)
	assert.Nil(t, reloadedLive.CompanyID)
	require.NoError(t, db.Unscoped().First(&reloadedGone, gone.ID).Error)
	assert.Nil(t, reloadedGone.CompanyID, "soft-deleted deals are unlinked too")
	require.NoError(t, db.First(&reloadedElsewhere, elsewhere.ID).Error)
	require.NotNil(t, reloadedElsewhere.CompanyID)
	assert.Equal(t, other.ID, *reloadedElsewhere.CompanyID, "other companies' links are untouched")
}
