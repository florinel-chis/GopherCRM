package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

func seedDealUser(t *testing.T, db *gorm.DB, email string, role models.UserRole) *models.User {
	t.Helper()
	user := &models.User{Email: email, Password: "x", FirstName: "F", LastName: "L", Role: role, IsActive: true}
	require.NoError(t, db.Create(user).Error)
	return user
}

func seedDealLead(t *testing.T, db *gorm.DB, email string, ownerID uint) *models.Lead {
	t.Helper()
	lead := &models.Lead{FirstName: "L", LastName: "Ead", Email: email, OwnerID: ownerID}
	require.NoError(t, db.Create(lead).Error)
	return lead
}

func storedDeal(t *testing.T, db *gorm.DB, id uint) models.Deal {
	t.Helper()
	var deal models.Deal
	require.NoError(t, db.First(&deal, id).Error)
	return deal
}

// A sales user may link a deal only to a lead it owns; another rep's lead is
// refused exactly like a missing one and nothing is written. The admin may
// link any lead, to any owner's deal.
func TestDealService_LeadLinkFollowsTheLeadVisibilityPolicy(t *testing.T) {
	db := setupDealServiceDB(t)
	admin := seedDealUser(t, db, "deal-vis-admin@example.com", models.RoleAdmin)
	alice := seedDealUser(t, db, "deal-vis-alice@example.com", models.RoleSales)
	bob := seedDealUser(t, db, "deal-vis-bob@example.com", models.RoleSales)
	support := seedDealUser(t, db, "deal-vis-support@example.com", models.RoleSupport)
	alicesLead := seedDealLead(t, db, "alices-lead@example.com", alice.ID)
	bobsLead := seedDealLead(t, db, "bobs-lead@example.com", bob.ID)
	svc := newRealDealService(t, db, repository.NewDealRepository(db), &stubCurrency{value: "EUR"})

	// Create: Alice cannot attach Bob's lead to her own deal.
	err := svc.Create(&models.Deal{Title: "Poach", OwnerID: alice.ID, LeadID: &bobsLead.ID}, nil, alice.ID)
	assert.True(t, errors.Is(err, apperrors.ErrLeadNotFound), "got %v", err)
	var count int64
	require.NoError(t, db.Model(&models.Deal{}).Count(&count).Error)
	assert.Equal(t, int64(0), count, "a refused create writes no deal")
	require.NoError(t, db.Model(&models.DealStageChange{}).Count(&count).Error)
	assert.Equal(t, int64(0), count, "and no history row")

	// The refusal reads exactly like a lead that does not exist.
	missing := uint(999999)
	errMissing := svc.Create(&models.Deal{Title: "Ghost", OwnerID: alice.ID, LeadID: &missing}, nil, alice.ID)
	assert.True(t, errors.Is(errMissing, apperrors.ErrLeadNotFound))

	// A role without lead access is refused even through the service.
	err = svc.Create(&models.Deal{Title: "Support", OwnerID: support.ID, LeadID: &alicesLead.ID}, nil, support.ID)
	assert.True(t, errors.Is(err, apperrors.ErrLeadNotFound), "got %v", err)

	// Her own lead is fine.
	own := &models.Deal{Title: "Own lead", OwnerID: alice.ID, LeadID: &alicesLead.ID}
	require.NoError(t, svc.Create(own, nil, alice.ID))

	// Update: moving her deal onto Bob's lead is refused and changes nothing.
	edit, err := svc.GetByID(own.ID)
	require.NoError(t, err)
	edit.Owner, edit.Lead = nil, nil
	edit.Title = "Moved"
	edit.LeadID = &bobsLead.ID
	err = svc.Update(edit, nil, alice.ID)
	assert.True(t, errors.Is(err, apperrors.ErrLeadNotFound), "got %v", err)
	after := storedDeal(t, db, own.ID)
	assert.Equal(t, "Own lead", after.Title, "a refused update writes nothing")
	require.NotNil(t, after.LeadID)
	assert.Equal(t, alicesLead.ID, *after.LeadID)

	// The admin may link any lead to anybody's deal.
	adminMade := &models.Deal{Title: "Admin linked", OwnerID: alice.ID, LeadID: &bobsLead.ID}
	require.NoError(t, svc.Create(adminMade, nil, admin.ID))
	edit, err = svc.GetByID(own.ID)
	require.NoError(t, err)
	edit.Owner, edit.Lead = nil, nil
	edit.LeadID = &bobsLead.ID
	require.NoError(t, svc.Update(edit, nil, admin.ID))
	assert.Equal(t, bobsLead.ID, *storedDeal(t, db, own.ID).LeadID)

	// Alice can still edit the deal that carries a link she cannot see: the
	// link is kept as stored, not re-checked (reads hide it instead).
	edit, err = svc.GetByID(adminMade.ID)
	require.NoError(t, err)
	edit.Owner, edit.Lead = nil, nil
	edit.Title = "Renamed by the owner"
	require.NoError(t, svc.Update(edit, nil, alice.ID))
	after = storedDeal(t, db, adminMade.ID)
	assert.Equal(t, "Renamed by the owner", after.Title)
	require.NotNil(t, after.LeadID)
	assert.Equal(t, bobsLead.ID, *after.LeadID)

	// ...and may clear it.
	edit.LeadID = nil
	edit.Title = "Cleared"
	require.NoError(t, svc.Update(edit, nil, alice.ID))
	assert.Nil(t, storedDeal(t, db, adminMade.ID).LeadID)
}
