package repository

import (
	"fmt"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #532: every start gives every API key without an expiry, or with one later
// than one year from now, the expiry models.APIKeyExpiryCap(now), and revokes
// every key of a customer account that is not revoked yet. Soft-deleted rows
// are included, a key already revoked keeps its revoked_at, and a second run
// changes nothing.

func TestAPIKeyLifetimeStepSQLite(t *testing.T) {
	runAPIKeyLifetimeStep(t, migrateRoleDB(t, testsupport.Open(t, testsupport.SQLite, "api_key_lifetime", roleDB)))
}

func TestAPIKeyLifetimeStepMySQL(t *testing.T) {
	runAPIKeyLifetimeStep(t, migrateRoleDB(t, testsupport.Open(t, testsupport.MySQL, "api_key_lifetime", roleDB)))
}

// lifetimeKeyRow is every column the step may write.
type lifetimeKeyRow struct {
	ID        uint
	ExpiresAt *time.Time
	IsActive  bool
	RevokedAt *time.Time
	UpdatedAt time.Time
}

func readLifetimeKeys(t *testing.T, db *gorm.DB) map[uint]lifetimeKeyRow {
	t.Helper()
	var rows []lifetimeKeyRow
	require.NoError(t, db.Raw("SELECT id, expires_at, is_active, revoked_at, updated_at FROM api_keys ORDER BY id").Scan(&rows).Error)
	out := map[uint]lifetimeKeyRow{}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out
}

func runAPIKeyLifetimeStep(t *testing.T, db *gorm.DB) {
	user := func(email string, role models.UserRole) uint {
		u := &models.User{Email: email, Password: "x", FirstName: "Key", LastName: "Lifetime", Role: role, IsActive: true}
		require.NoError(t, db.Create(u).Error)
		return u.ID
	}
	staff := user("lifetime-staff@example.com", models.RoleSales)
	customer := user("lifetime-customer@example.com", models.RoleCustomer)

	seq := 0
	key := func(owner uint, expires *time.Time) uint {
		seq++
		k := &models.APIKey{Name: fmt.Sprintf("key %d", seq), KeyHash: fmt.Sprintf("lifetime-hash-%d", seq),
			Prefix: "abcd1234", UserID: owner, ExpiresAt: expires, IsActive: true}
		require.NoError(t, db.Create(k).Error)
		return k.ID
	}
	at := func(t time.Time) *time.Time { return &t }
	now := time.Now()
	nearExpiry := now.AddDate(0, 0, 30).Truncate(time.Second)
	oldRevokedAt := time.Date(2020, 1, 2, 3, 4, 5, 0, time.Local)

	nullKey := key(staff, nil)
	farKey := key(staff, at(now.AddDate(3, 0, 0)))
	nearKey := key(staff, at(nearExpiry))
	deletedKey := key(staff, nil)
	require.NoError(t, db.Delete(&models.APIKey{}, deletedKey).Error)
	customerLive := key(customer, at(nearExpiry))
	customerRevoked := key(customer, at(nearExpiry))
	require.NoError(t, db.Exec("UPDATE api_keys SET is_active = ?, revoked_at = ? WHERE id = ?", false, oldRevokedAt, customerRevoked).Error)
	before := readLifetimeKeys(t, db)

	lo := models.APIKeyExpiryCap(time.Now())
	require.NoError(t, models.MigrateDatabaseWith(db))
	hi := models.APIKeyExpiryCap(time.Now())
	got := readLifetimeKeys(t, db)

	for name, id := range map[string]uint{"null": nullKey, "far": farKey, "soft-deleted": deletedKey} {
		expires := got[id].ExpiresAt
		if assert.NotNil(t, expires, "the %s key gets an expiry", name) {
			assert.False(t, expires.Before(lo.Add(-time.Second)), "the %s key: %v before %v", name, *expires, lo)
			assert.False(t, expires.After(hi.Add(time.Second)), "the %s key: %v after %v", name, *expires, hi)
		}
	}
	if assert.NotNil(t, got[nearKey].ExpiresAt) {
		assert.True(t, got[nearKey].ExpiresAt.Equal(*before[nearKey].ExpiresAt), "an earlier expiry is unchanged")
	}

	assert.False(t, got[customerLive].IsActive, "the live customer key is revoked")
	if assert.NotNil(t, got[customerLive].RevokedAt, "and stamped") {
		assert.WithinDuration(t, now, *got[customerLive].RevokedAt, time.Minute)
	}
	if assert.NotNil(t, got[customerRevoked].RevokedAt) {
		assert.True(t, got[customerRevoked].RevokedAt.Equal(*before[customerRevoked].RevokedAt), "an earlier revoked_at is kept")
	}
	for _, id := range []uint{nullKey, farKey, nearKey, deletedKey} {
		assert.True(t, got[id].IsActive, "staff key %d stays active", id)
		assert.Nil(t, got[id].RevokedAt, "staff key %d is not revoked", id)
	}
	for id, row := range got {
		assert.True(t, row.UpdatedAt.Equal(before[id].UpdatedAt), "updated_at of key %d is not touched", id)
	}

	require.NoError(t, models.MigrateDatabaseWith(db))
	assert.Equal(t, got, readLifetimeKeys(t, db), "a second start changes nothing")
}
