package repository

import (
	"fmt"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIKeyRepository_RevokeAllForUser checks the one-statement revocation
// of every key of an account: live keys and inactive keys without
// revoked_at are stamped, a key revoked before keeps its first revoked_at,
// soft-deleted keys and other users' keys are untouched, and the count of
// changed rows is returned.
func TestAPIKeyRepository_RevokeAllForUser(t *testing.T) {
	db := testsupport.OpenSQLite(t, testsupport.File("apikey_revoke_all.db", testsupport.BusyTimeout),
		testsupport.Models(&models.User{}, &models.APIKey{}))

	owner := models.User{Email: "owner@example.com", Password: "x", Role: models.RoleSales, IsActive: true}
	other := models.User{Email: "other@example.com", Password: "x", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&other).Error)

	seq := 0
	key := func(userID uint, active bool, revokedAt *time.Time) models.APIKey {
		seq++
		k := models.APIKey{Name: fmt.Sprintf("key-%d", seq), KeyHash: fmt.Sprintf("hash-%d", seq), Prefix: "abcd1234", UserID: userID, IsActive: true}
		require.NoError(t, db.Create(&k).Error)
		// is_active carries a column default, so false is written afterwards.
		require.NoError(t, db.Model(&models.APIKey{}).Where("id = ?", k.ID).
			Updates(map[string]interface{}{"is_active": active, "revoked_at": revokedAt}).Error)
		return k
	}

	earlier := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	live1 := key(owner.ID, true, nil)
	live2 := key(owner.ID, true, nil)
	revoked := key(owner.ID, false, &earlier)
	legacy := key(owner.ID, false, nil)
	deleted := key(owner.ID, true, nil)
	require.NoError(t, db.Delete(&models.APIKey{}, deleted.ID).Error)
	foreign := key(other.ID, true, nil)

	at := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	n, err := NewAPIKeyRepository(db).RevokeAllForUser(owner.ID, at)
	require.NoError(t, err)
	assert.Equal(t, int64(3), n, "the two live keys and the legacy inactive key")

	load := func(id uint) models.APIKey {
		var k models.APIKey
		require.NoError(t, db.Unscoped().First(&k, id).Error)
		return k
	}
	for _, id := range []uint{live1.ID, live2.ID, legacy.ID} {
		k := load(id)
		assert.False(t, k.IsActive, "key %d", id)
		require.NotNil(t, k.RevokedAt, "key %d", id)
		assert.True(t, k.RevokedAt.Equal(at), "key %d revoked_at %v", id, k.RevokedAt)
	}
	k := load(revoked.ID)
	require.NotNil(t, k.RevokedAt)
	assert.True(t, k.RevokedAt.Equal(earlier), "a key revoked before keeps its first revoked_at")
	k = load(deleted.ID)
	assert.True(t, k.IsActive)
	assert.Nil(t, k.RevokedAt, "a soft-deleted key is outside the statement")
	k = load(foreign.ID)
	assert.True(t, k.IsActive)
	assert.Nil(t, k.RevokedAt, "another user's key is untouched")

	n, err = NewAPIKeyRepository(db).RevokeAllForUser(owner.ID, at.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "a second call changes nothing")
}
