package repository

import (
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func makeLead(t *testing.T, db *gorm.DB, firstName, email string, ownerID uint) models.Lead {
	t.Helper()
	lead := models.Lead{
		FirstName: firstName,
		LastName:  "Tester",
		Email:     email,
		Status:    models.LeadStatusNew,
		OwnerID:   ownerID,
	}
	require.NoError(t, db.Create(&lead).Error)
	return lead
}

// The same address can legitimately appear on several leads, and the form
// module has to attach to one of them: the most recent.
func TestLeadRepository_GetLatestByEmail_ReturnsNewest(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewLeadRepository(db)
	owner := makeUser(t, db, "owner@example.com")

	older := makeLead(t, db, "Older", "ada@example.com", owner.ID)
	stampTimes(t, db, &older, time.Now().Add(-72*time.Hour), time.Now().Add(-72*time.Hour))
	newer := makeLead(t, db, "Newer", "ada@example.com", owner.ID)
	makeLead(t, db, "Someone", "grace@example.com", owner.ID)

	found, err := repo.GetLatestByEmail("ada@example.com")

	require.NoError(t, err)
	assert.Equal(t, newer.ID, found.ID)
	assert.Equal(t, "Newer", found.FirstName)
}

func TestLeadRepository_GetLatestByEmail_MissIsRecordNotFound(t *testing.T) {
	repo := NewLeadRepository(setupAnalyticsTestDB(t))

	_, err := repo.GetLatestByEmail("nobody@example.com")

	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// An erased lead must stay erased: resurrecting a soft-deleted row here would
// re-attach form submissions to a person who asked to be forgotten.
func TestLeadRepository_GetLatestByEmail_SkipsSoftDeleted(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewLeadRepository(db)
	owner := makeUser(t, db, "owner@example.com")

	live := makeLead(t, db, "Live", "ada@example.com", owner.ID)
	stampTimes(t, db, &live, time.Now().Add(-72*time.Hour), time.Now().Add(-72*time.Hour))
	deleted := makeLead(t, db, "Deleted", "ada@example.com", owner.ID)
	require.NoError(t, db.Delete(&deleted).Error)

	found, err := repo.GetLatestByEmail("ada@example.com")

	require.NoError(t, err)
	assert.Equal(t, live.ID, found.ID)

	require.NoError(t, db.Delete(&live).Error)
	_, err = repo.GetLatestByEmail("ada@example.com")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestLeadRepository_GetLatestByEmail_UsesTheTransaction(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewLeadRepository(db)
	owner := makeUser(t, db, "owner@example.com")

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		txRepo := repo.WithTx(tx)
		makeLead(t, tx, "Uncommitted", "ada@example.com", owner.ID)

		found, err := txRepo.GetLatestByEmail("ada@example.com")
		require.NoError(t, err)
		assert.Equal(t, "Uncommitted", found.FirstName)
		return nil
	}))
}

// external_id is not unique: the lookup returns every live lead carrying the
// value, lowest id first, capped at limit, with the owner loaded.
func TestLeadRepository_ListByExternalID(t *testing.T) {
	db := setupAnalyticsTestDB(t)
	repo := NewLeadRepository(db)
	owner := makeUser(t, db, "owner@example.com")

	var ids []uint
	for _, name := range []string{"First", "Second", "Third"} {
		lead := makeLead(t, db, name, name+"@example.com", owner.ID)
		require.NoError(t, db.Model(&lead).Update("external_id", "crm-dup").Error)
		ids = append(ids, lead.ID)
	}
	erased := makeLead(t, db, "Erased", "erased@example.com", owner.ID)
	require.NoError(t, db.Model(&erased).Update("external_id", "crm-dup").Error)
	require.NoError(t, db.Delete(&erased).Error)
	other := makeLead(t, db, "Other", "other@example.com", owner.ID)
	require.NoError(t, db.Model(&other).Update("external_id", "crm-other").Error)

	leads, err := repo.ListByExternalID("crm-dup", 100)
	require.NoError(t, err)
	got := make([]uint, 0, len(leads))
	for _, lead := range leads {
		got = append(got, lead.ID)
		assert.Equal(t, owner.ID, lead.Owner.ID, "owner preloaded")
	}
	assert.Equal(t, ids, got, "live leads only, ascending id")

	capped, err := repo.ListByExternalID("crm-dup", 2)
	require.NoError(t, err)
	require.Len(t, capped, 2)
	assert.Equal(t, ids[:2], []uint{capped[0].ID, capped[1].ID})

	none, err := repo.ListByExternalID("crm-unknown", 100)
	require.NoError(t, err)
	assert.Empty(t, none)
}
