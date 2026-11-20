package service

// Regression roots for #525: revoking an API key is final. Revoke stamps
// revoked_at with one conditional UPDATE, Update writes each field with its
// own statement (Rename writes only name), and is_active=true never switches
// a revoked key on again. Each subtest commits a revocation in a given order
// against a rename or an is_active=true request, through the instance-local
// seam apiKeyService.beforeWrite (between Update's ownership read and its
// first write), and asserts the committed row and that ValidateAPIKey refuses
// the plaintext key as key_revoked.
//
// TestAPIKeyRevocationFinalSQLite runs on a file database with the production
// one-connection pool; TestAPIKeyRevocationFinalMySQL on a disposable
// MySQL/MariaDB schema built by the real migrations when TEST_MYSQL_DSN is set
// (skipped without it, failing when it points at an unreachable server).

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const akrSecret = "api-key-revocation-final-secret"

func TestAPIKeyRevocationFinalSQLite(t *testing.T) {
	runAPIKeyRevocationFinal(t, openAutomationSQLite(t))
}

func TestAPIKeyRevocationFinalMySQL(t *testing.T) {
	runAPIKeyRevocationFinal(t, openMigratedTestMySQL(t, 4))
}

// akrRow is the committed state of one key.
type akrRow struct {
	Name      string
	IsActive  bool
	RevokedAt *time.Time
	UpdatedAt time.Time
}

type akrEnv struct {
	db     *gorm.DB
	keys   *apiKeyService
	auth   AuthService
	userID uint
	seq    int
}

// issue creates a live key for the owner and returns its id and the
// plaintext as the client sends it.
func (e *akrEnv) issue(t *testing.T) (uint, string) {
	t.Helper()
	e.seq++
	full, key, err := e.keys.Generate(e.userID, fmt.Sprintf("Key %d", e.seq), nil)
	require.NoError(t, err)
	return key.ID, full
}

func (e *akrEnv) row(t *testing.T, id uint) akrRow {
	t.Helper()
	var key models.APIKey
	require.NoError(t, e.db.Unscoped().First(&key, id).Error)
	return akrRow{Name: key.Name, IsActive: key.IsActive, RevokedAt: key.RevokedAt, UpdatedAt: key.UpdatedAt}
}

// assertRefused checks that the plaintext key no longer authenticates and
// that the refusal is key_revoked.
func (e *akrEnv) assertRefused(t *testing.T, full string) {
	t.Helper()
	_, err := e.auth.ValidateAPIKey(strings.TrimPrefix(full, "gcrm_"))
	require.Error(t, err)
	var refusal *authRefusalError
	require.True(t, errors.As(err, &refusal), "a refusal with a reason, got %v", err)
	assert.Equal(t, AuthAuditReasonKeyRevoked, refusal.reason)
}

func (e *akrEnv) assertRevoked(t *testing.T, id uint) akrRow {
	t.Helper()
	got := e.row(t, id)
	assert.False(t, got.IsActive, "the key stays revoked")
	assert.NotNil(t, got.RevokedAt, "revoked_at is set")
	return got
}

func runAPIKeyRevocationFinal(t *testing.T, db *gorm.DB) {
	keyRepo := repository.NewAPIKeyRepository(db)
	userRepo := repository.NewUserRepository(db)
	keys := NewAPIKeyService(keyRepo, userRepo, utils.NewTransactionManager(db), akrSecret).(*apiKeyService)
	owner := &models.User{Email: "revocation-final-owner@example.com", Password: "x", FirstName: "Rev", LastName: "Owner",
		Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(owner).Error)
	e := &akrEnv{
		db:     db,
		keys:   keys,
		auth:   NewAuthService(userRepo, keyRepo, config.JWTConfig{}, akrSecret),
		userID: owner.ID,
	}
	revokeInSeam := func(t *testing.T) {
		e.keys.beforeWrite = func(id uint) {
			require.NoError(t, e.keys.Revoke(id, e.userID), "the competing revocation commits")
			e.keys.beforeWrite = nil
		}
		t.Cleanup(func() { e.keys.beforeWrite = nil })
	}
	strPtr := func(s string) *string { return &s }
	boolPtr := func(b bool) *bool { return &b }

	t.Run("a_rename_read_before_a_revocation_committed", func(t *testing.T) {
		id, full := e.issue(t)
		revokeInSeam(t)

		_, err := e.keys.Update(id, e.userID, strPtr("Renamed after read"), nil)
		require.NoError(t, err)

		got := e.assertRevoked(t, id)
		assert.Equal(t, "Renamed after read", got.Name, "the rename applies")
		e.assertRefused(t, full)
	})

	t.Run("b_revoke_then_rename", func(t *testing.T) {
		id, full := e.issue(t)
		require.NoError(t, e.keys.Revoke(id, e.userID))
		revokedAt := e.assertRevoked(t, id).RevokedAt

		updated, err := e.keys.Update(id, e.userID, strPtr("Renamed when revoked"), nil)
		require.NoError(t, err)
		assert.False(t, updated.IsActive)

		got := e.assertRevoked(t, id)
		assert.Equal(t, "Renamed when revoked", got.Name)
		assert.True(t, revokedAt.Equal(*got.RevokedAt), "the rename keeps revoked_at")
		e.assertRefused(t, full)
	})

	t.Run("c_switch_on_read_before_a_revocation_committed", func(t *testing.T) {
		id, full := e.issue(t)
		before := e.row(t, id)
		revokeInSeam(t)

		updated, err := e.keys.Update(id, e.userID, strPtr("Renamed with switch on"), boolPtr(true))
		require.ErrorIs(t, err, ErrAPIKeyRevoked)
		assert.Nil(t, updated)

		got := e.assertRevoked(t, id)
		assert.Equal(t, before.Name, got.Name, "nothing of the refused request is written")
		e.assertRefused(t, full)
	})

	t.Run("c_switch_on_alone_read_before_a_revocation_committed", func(t *testing.T) {
		id, full := e.issue(t)
		revokeInSeam(t)

		updated, err := e.keys.Update(id, e.userID, nil, boolPtr(true))
		require.ErrorIs(t, err, ErrAPIKeyRevoked)
		assert.Nil(t, updated)

		e.assertRevoked(t, id)
		e.assertRefused(t, full)
	})

	t.Run("d_switch_on_a_revoked_key", func(t *testing.T) {
		id, full := e.issue(t)
		require.NoError(t, e.keys.Revoke(id, e.userID))
		before := e.assertRevoked(t, id)

		for _, name := range []*string{nil, strPtr("Renamed while refused")} {
			updated, err := e.keys.Update(id, e.userID, name, boolPtr(true))
			require.ErrorIs(t, err, ErrAPIKeyRevoked)
			assert.Nil(t, updated)
			assert.Equal(t, before, e.row(t, id), "name, is_active, revoked_at and updated_at are unchanged")
		}
		e.assertRefused(t, full)
	})

	t.Run("e_second_revoke_keeps_the_first_revoked_at", func(t *testing.T) {
		id, full := e.issue(t)
		require.NoError(t, e.keys.Revoke(id, e.userID))
		first := e.assertRevoked(t, id)

		// A later clock changes nothing either.
		changed, err := repository.NewAPIKeyRepository(db).Revoke(id, e.userID, first.RevokedAt.Add(time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(0), changed)
		require.NoError(t, e.keys.Revoke(id, e.userID), "revoking again succeeds")

		assert.Equal(t, first, e.row(t, id), "the first revocation is kept as it was")
		e.assertRefused(t, full)
	})

	t.Run("f_legacy_inactive_row_refuses_switch_on", func(t *testing.T) {
		id, full := e.issue(t)
		// As a release before revoked_at wrote a revocation.
		require.NoError(t, db.Model(&models.APIKey{}).Where("id = ?", id).UpdateColumn("is_active", false).Error)
		before := e.row(t, id)
		require.Nil(t, before.RevokedAt)

		updated, err := e.keys.Update(id, e.userID, nil, boolPtr(true))
		require.ErrorIs(t, err, ErrAPIKeyRevoked)
		assert.Nil(t, updated)
		assert.Equal(t, before, e.row(t, id), "the legacy row is unchanged")
		e.assertRefused(t, full)

		// Revoking it stamps revoked_at.
		require.NoError(t, e.keys.Revoke(id, e.userID))
		e.assertRevoked(t, id)
	})

	t.Run("g_switch_on_a_live_key_changes_nothing", func(t *testing.T) {
		id, full := e.issue(t)
		before := e.row(t, id)

		updated, err := e.keys.Update(id, e.userID, nil, boolPtr(true))
		require.NoError(t, err)
		assert.True(t, updated.IsActive)
		assert.Nil(t, updated.RevokedAt)
		assert.Equal(t, before, e.row(t, id), "the row is unchanged")

		user, err := e.auth.ValidateAPIKey(strings.TrimPrefix(full, "gcrm_"))
		require.NoError(t, err, "the live key still authenticates")
		assert.Equal(t, e.userID, user.ID)
	})
}
