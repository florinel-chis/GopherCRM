package service

// Issue #526: one step, revokeAllCredentials, ends every credential of an
// account inside the transaction of a password change, a password-reset
// confirm, a governed deactivation (SetActiveAs false, UpdateAs with
// is_active false) and a SCIM deactivation (Patch or Replace with active
// false, Delete). It increments users.token_version, revokes every refresh
// token and revokes every API key; a later reactivation brings none back.
//
// Each subtest seeds an owner with two live API keys, one key revoked
// earlier, one legacy inactive key without revoked_at and two live refresh
// tokens, next to a bystander with a key and a refresh token, then asserts
// the committed rows and what ValidateAPIKey answers. A failing
// APIKeyRepository.RevokeAllForUser rolls each caller's whole write back, and
// a service built without the API key store refuses and writes nothing.
//
// TestCredentialRevocationSQLite runs on a SQLite file opened by the
// production connector (one pooled connection); TestCredentialRevocationMySQL
// on a schema built by the real migrations when TEST_MYSQL_DSN is set
// (skipped without it, failing when it points at an unreachable server; also
// pointed at MariaDB 10.11).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	crvSecret      = "credential-revocation-secret-526-0123456789"
	crvPassword    = "Current-Passw0rd!526"
	crvNewPassword = "Changed-Passw0rd!526"
	crvIssuer      = "https://issuer.credential-revocation.test"
	crvSCIMRole    = "CRM.Sales"
)

var crvSeq atomic.Uint64

func TestCredentialRevocationSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runCredentialRevocation(t, db)
}

func TestCredentialRevocationMySQL(t *testing.T) {
	runCredentialRevocation(t, openMigratedTestMySQL(t, 3))
}

// failingAPIKeyRevocation is the real API key repository whose
// RevokeAllForUser fails, so a caller fails after its users write and its
// refresh token revocation.
type failingAPIKeyRevocation struct {
	repository.APIKeyRepository
}

var errAPIKeyRevocationFailed = errors.New("api key revocation failed")

func (r failingAPIKeyRevocation) RevokeAllForUser(uint, time.Time) (int64, error) {
	return 0, errAPIKeyRevocationFailed
}

func (r failingAPIKeyRevocation) WithTx(tx *gorm.DB) repository.APIKeyRepository {
	return failingAPIKeyRevocation{r.APIKeyRepository.WithTx(tx)}
}

// crvEnv is the database and the services of one root.
type crvEnv struct {
	db       *gorm.DB
	keyRepo  repository.APIKeyRepository
	auth     AuthService
	keys     APIKeyService
	salesID  uint
	adminID  uint
	actor    Actor
	source   *scimWritesSource
	earlier  time.Time
	failKeys repository.APIKeyRepository
}

// crvSeed is one subtest's owner and bystander with their credentials.
type crvSeed struct {
	owner, bystander      *models.User
	liveKeys              []string // plaintext of the two live keys
	revokedKey, legacy    string
	revokedID, legacyID   uint
	liveIDs               []uint
	refresh               []string // the owner's two live refresh tokens
	bystanderKey          string
	bystanderKeyID        uint
	bystanderRefresh      string
	passwordHash          string
	tokenVersion          uint
	bystanderTokenVersion uint
}

func (e *crvEnv) authWith(keys repository.APIKeyRepository) AuthService {
	return NewAuthServiceWithSessions(repository.NewUserRepository(e.db), keys, repository.NewRefreshTokenRepository(e.db),
		repository.NewPasswordResetTokenRepository(e.db), utils.NewTransactionManager(e.db), nil,
		config.JWTConfig{Secret: crvSecret, ExpiryHours: 1, RefreshTokenDays: 30}, "http://localhost:5173", crvSecret)
}

// users builds the governed user service; keys nil leaves
// WithUserCredentialRevocation out.
func (e *crvEnv) users(keys repository.APIKeyRepository) UserService {
	opts := []UserServiceOption{WithUserRoleGovernance(repository.NewRoleRepository(e.db), repository.NewRoleAuditRepository(e.db),
		utils.NewTransactionManager(e.db))}
	if keys != nil {
		opts = append(opts, WithUserCredentialRevocation(keys, repository.NewRefreshTokenRepository(e.db)))
	}
	return NewUserService(repository.NewUserRepository(e.db), opts...)
}

// scim builds the SCIM user service; keys may be nil.
func (e *crvEnv) scim(keys repository.APIKeyRepository) SCIMUserService {
	return NewSCIMUserService(repository.NewSCIMUserRepository(e.db), e.source,
		WithSCIMUserWrites(repository.NewUserRepository(e.db), repository.NewRoleRepository(e.db), repository.NewRoleAuditRepository(e.db),
			repository.NewUserIdentityRepository(e.db), repository.NewRefreshTokenRepository(e.db), keys,
			utils.NewTransactionManager(e.db), nil))
}

func (e *crvEnv) createUser(t *testing.T, prefix string, role models.UserRole, roleID uint) *models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(crvPassword), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{Email: fmt.Sprintf("%s-%d@credentials.test", prefix, crvSeq.Add(1)), Password: string(hash),
		FirstName: "Cred", LastName: "Owner", Role: role, RoleID: &roleID, IsActive: true}
	require.NoError(t, e.db.Create(user).Error)
	return user
}

func (e *crvEnv) issueKey(t *testing.T, userID uint) (string, uint) {
	t.Helper()
	full, key, err := e.keys.Generate(userID, fmt.Sprintf("Key %d", crvSeq.Add(1)), nil)
	require.NoError(t, err)
	return full, key.ID
}

func (e *crvEnv) login(t *testing.T, email string) string {
	t.Helper()
	tokens, err := e.auth.LoginWithTokens(email, crvPassword)
	require.NoError(t, err)
	require.NotEmpty(t, tokens.RefreshToken)
	return tokens.RefreshToken
}

// seed creates the owner (linked under the SCIM issuer when managed) and the
// bystander with every credential the subtests look at.
func (e *crvEnv) seed(t *testing.T, managed bool) *crvSeed {
	t.Helper()
	s := &crvSeed{
		owner:     e.createUser(t, "owner", models.RoleSales, e.salesID),
		bystander: e.createUser(t, "bystander", models.RoleSales, e.salesID),
	}
	if managed {
		require.NoError(t, e.db.Create(&models.UserIdentity{UserID: s.owner.ID, Issuer: crvIssuer,
			Subject: fmt.Sprintf("sub-%d", s.owner.ID), Source: models.UserIdentitySourceSCIM}).Error)
	}
	for i := 0; i < 2; i++ {
		full, id := e.issueKey(t, s.owner.ID)
		s.liveKeys = append(s.liveKeys, full)
		s.liveIDs = append(s.liveIDs, id)
	}
	s.revokedKey, s.revokedID = e.issueKey(t, s.owner.ID)
	n, err := e.keyRepo.Revoke(s.revokedID, s.owner.ID, e.earlier)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	s.legacy, s.legacyID = e.issueKey(t, s.owner.ID)
	require.NoError(t, e.db.Model(&models.APIKey{}).Where("id = ?", s.legacyID).Update("is_active", false).Error)
	s.refresh = []string{e.login(t, s.owner.Email), e.login(t, s.owner.Email)}
	s.bystanderKey, s.bystanderKeyID = e.issueKey(t, s.bystander.ID)
	s.bystanderRefresh = e.login(t, s.bystander.Email)

	stored := e.user(t, s.owner.ID)
	s.passwordHash, s.tokenVersion = stored.Password, stored.TokenVersion
	s.bystanderTokenVersion = e.user(t, s.bystander.ID).TokenVersion
	return s
}

func (e *crvEnv) user(t *testing.T, id uint) models.User {
	t.Helper()
	var user models.User
	require.NoError(t, e.db.Unscoped().First(&user, id).Error)
	return user
}

func (e *crvEnv) key(t *testing.T, id uint) models.APIKey {
	t.Helper()
	var key models.APIKey
	require.NoError(t, e.db.Unscoped().First(&key, id).Error)
	return key
}

func (e *crvEnv) liveRefresh(t *testing.T, userID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db.Model(&models.RefreshToken{}).Where("user_id = ? AND is_revoked = ?", userID, false).Count(&n).Error)
	return n
}

func (e *crvEnv) keyValid(full string) error {
	_, err := e.auth.ValidateAPIKey(strings.TrimPrefix(full, "gcrm_"))
	return err
}

// assertEarlierKeyKept checks that the key revoked before keeps its first
// revoked_at.
func (e *crvEnv) assertEarlierKeyKept(t *testing.T, s *crvSeed) {
	t.Helper()
	k := e.key(t, s.revokedID)
	assert.False(t, k.IsActive)
	if assert.NotNil(t, k.RevokedAt) {
		assert.WithinDuration(t, e.earlier, *k.RevokedAt, time.Second, "the key revoked before keeps its first revoked_at")
	}
}

// assertBystanderUntouched checks that the other user's credentials are as
// they were.
func (e *crvEnv) assertBystanderUntouched(t *testing.T, s *crvSeed) {
	t.Helper()
	k := e.key(t, s.bystanderKeyID)
	assert.True(t, k.IsActive, "the bystander's key stays active")
	assert.Nil(t, k.RevokedAt, "the bystander's key carries no revoked_at")
	assert.NoError(t, e.keyValid(s.bystanderKey), "the bystander's key still validates")
	assert.Equal(t, int64(1), e.liveRefresh(t, s.bystander.ID), "the bystander's refresh token stays live")
	assert.Equal(t, s.bystanderTokenVersion, e.user(t, s.bystander.ID).TokenVersion)
}

// assertEnded checks that every credential of the owner ended in one step:
// token_version one up, every refresh token revoked but keepRefresh, every
// key revoked and refused.
func (e *crvEnv) assertEnded(t *testing.T, s *crvSeed, keepRefresh int64) {
	t.Helper()
	assert.Equal(t, s.tokenVersion+1, e.user(t, s.owner.ID).TokenVersion, "token_version is incremented once")
	assert.Equal(t, keepRefresh, e.liveRefresh(t, s.owner.ID), "every earlier refresh token is revoked")
	for _, id := range append(append([]uint{}, s.liveIDs...), s.legacyID) {
		k := e.key(t, id)
		assert.False(t, k.IsActive, "key %d is switched off", id)
		if assert.NotNil(t, k.RevokedAt, "key %d carries revoked_at", id) {
			assert.True(t, k.RevokedAt.After(e.earlier), "key %d is stamped by this write", id)
		}
	}
	e.assertEarlierKeyKept(t, s)
	for _, full := range append(append([]string{}, s.liveKeys...), s.revokedKey, s.legacy) {
		assert.Error(t, e.keyValid(full), "every key of the owner is refused")
	}
	if keepRefresh == 0 {
		for _, raw := range s.refresh {
			_, err := e.auth.RefreshAccessToken(raw)
			assert.Error(t, err, "an earlier refresh token is refused")
		}
	}
	e.assertBystanderUntouched(t, s)
}

// assertUnchanged checks that nothing of the owner changed: password,
// is_active, token_version, refresh tokens and keys.
func (e *crvEnv) assertUnchanged(t *testing.T, s *crvSeed, active bool) {
	t.Helper()
	stored := e.user(t, s.owner.ID)
	assert.Equal(t, s.passwordHash, stored.Password, "the password is unchanged")
	assert.Equal(t, active, stored.IsActive, "is_active is unchanged")
	assert.Equal(t, s.tokenVersion, stored.TokenVersion, "token_version is unchanged")
	assert.Equal(t, int64(2), e.liveRefresh(t, s.owner.ID), "the refresh tokens stay live")
	for _, id := range s.liveIDs {
		k := e.key(t, id)
		assert.True(t, k.IsActive, "key %d stays active", id)
		assert.Nil(t, k.RevokedAt, "key %d carries no revoked_at", id)
	}
	assert.Nil(t, e.key(t, s.legacyID).RevokedAt, "the legacy key is not stamped")
	e.assertEarlierKeyKept(t, s)
	if active {
		for _, full := range s.liveKeys {
			assert.NoError(t, e.keyValid(full), "a live key still validates")
		}
	}
	e.assertBystanderUntouched(t, s)
}

func runCredentialRevocation(t *testing.T, db *gorm.DB) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	ctx := context.Background()
	keyRepo := repository.NewAPIKeyRepository(db)
	e := &crvEnv{
		db:       db,
		keyRepo:  keyRepo,
		keys:     NewAPIKeyService(keyRepo, repository.NewUserRepository(db), utils.NewTransactionManager(db), crvSecret),
		earlier:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		failKeys: failingAPIKeyRevocation{keyRepo},
	}
	e.auth = e.authWith(keyRepo)
	var roles []models.Role
	require.NoError(t, db.Where("system_key IS NOT NULL").Find(&roles).Error)
	for _, role := range roles {
		switch models.UserRole(*role.SystemKey) {
		case models.RoleSales:
			e.salesID = role.ID
		case models.RoleAdmin:
			e.adminID = role.ID
		}
	}
	require.NotZero(t, e.salesID)
	require.NotZero(t, e.adminID)
	admin := e.createUser(t, "admin", models.RoleAdmin, e.adminID)
	e.actor = Actor{UserID: admin.ID}
	e.source = &scimWritesSource{cfg: EffectiveSSOConfig{IssuerURL: crvIssuer,
		RoleMapping: []SSORoleMapping{{Claim: SSOClaimRoles, Value: crvSCIMRole, RoleID: e.salesID}}}}

	users := e.users(keyRepo)
	scim := e.scim(keyRepo)
	falseValue, trueValue := false, true

	// --- password change and reset -----------------------------------------

	t.Run("change_password_with_a_refresh_token", func(t *testing.T) {
		s := e.seed(t, false)
		tokens, err := e.auth.ChangePassword(s.owner.ID, crvPassword, crvNewPassword, s.refresh[0])
		require.NoError(t, err)
		require.NotNil(t, tokens)
		e.assertEnded(t, s, 1)
		_, err = e.auth.RefreshAccessToken(tokens.RefreshToken)
		assert.NoError(t, err, "the renewed successor is live")
		// The successor was rotated by that refresh: its own successor is live.
		assert.Equal(t, int64(1), e.liveRefresh(t, s.owner.ID))
	})

	t.Run("change_password_without_a_refresh_token", func(t *testing.T) {
		s := e.seed(t, false)
		tokens, err := e.auth.ChangePassword(s.owner.ID, crvPassword, crvNewPassword, "")
		require.NoError(t, err)
		assert.Nil(t, tokens)
		e.assertEnded(t, s, 0)
	})

	t.Run("reset_confirm", func(t *testing.T) {
		s := e.seed(t, false)
		link := e.resetLink(t, s.owner.ID)
		require.NoError(t, e.auth.ConfirmPasswordReset(link, crvNewPassword))
		e.assertEnded(t, s, 0)
	})

	// --- governed deactivation ---------------------------------------------

	t.Run("set_active_false", func(t *testing.T) {
		s := e.seed(t, false)
		_, err := users.SetActiveAs(ctx, e.actor, s.owner.ID, false)
		require.NoError(t, err)
		assert.False(t, e.user(t, s.owner.ID).IsActive)
		e.assertEnded(t, s, 0)
	})

	t.Run("update_is_active_false", func(t *testing.T) {
		s := e.seed(t, false)
		_, err := users.UpdateAs(ctx, e.actor, s.owner.ID, map[string]interface{}{"is_active": false})
		require.NoError(t, err)
		assert.False(t, e.user(t, s.owner.ID).IsActive)
		e.assertEnded(t, s, 0)
	})

	t.Run("set_active_true_after_a_deactivation_restores_nothing", func(t *testing.T) {
		s := e.seed(t, false)
		_, err := users.SetActiveAs(ctx, e.actor, s.owner.ID, false)
		require.NoError(t, err)
		_, err = users.SetActiveAs(ctx, e.actor, s.owner.ID, true)
		require.NoError(t, err)
		assert.True(t, e.user(t, s.owner.ID).IsActive)
		e.assertEnded(t, s, 0)
	})

	// --- SCIM --------------------------------------------------------------

	t.Run("scim_patch_active_false", func(t *testing.T) {
		s := e.seed(t, true)
		_, err := scim.Patch(ctx, uintString(s.owner.ID), SCIMUserPatch{Active: &falseValue})
		require.NoError(t, err)
		assert.False(t, e.user(t, s.owner.ID).IsActive)
		e.assertEnded(t, s, 0)
	})

	t.Run("scim_replace_active_false", func(t *testing.T) {
		s := e.seed(t, true)
		in := SCIMUserInput{UserName: s.owner.Email, GivenName: "Cred", FamilyName: "Owner", Active: &falseValue, Roles: []string{crvSCIMRole}}
		_, err := scim.Replace(ctx, uintString(s.owner.ID), in)
		require.NoError(t, err)
		assert.False(t, e.user(t, s.owner.ID).IsActive)
		e.assertEnded(t, s, 0)
	})

	t.Run("scim_delete", func(t *testing.T) {
		s := e.seed(t, true)
		require.NoError(t, scim.Delete(ctx, uintString(s.owner.ID)))
		assert.False(t, e.user(t, s.owner.ID).IsActive)
		e.assertEnded(t, s, 0)
	})

	t.Run("scim_patch_active_true_after_a_deactivation_restores_nothing", func(t *testing.T) {
		s := e.seed(t, true)
		_, err := scim.Patch(ctx, uintString(s.owner.ID), SCIMUserPatch{Active: &falseValue})
		require.NoError(t, err)
		_, err = scim.Patch(ctx, uintString(s.owner.ID), SCIMUserPatch{Active: &trueValue})
		require.NoError(t, err)
		assert.True(t, e.user(t, s.owner.ID).IsActive)
		e.assertEnded(t, s, 0)
	})

	t.Run("scim_delete_on_an_inactive_account_writes_nothing", func(t *testing.T) {
		s := e.seed(t, true)
		require.NoError(t, e.db.Model(&models.User{}).Where("id = ?", s.owner.ID).Update("is_active", false).Error)
		require.NoError(t, scim.Delete(ctx, uintString(s.owner.ID)))
		e.assertUnchanged(t, s, false)
	})

	// --- a failing key revocation rolls each caller back ---------------------

	t.Run("change_password_rolls_back_when_the_key_revocation_fails", func(t *testing.T) {
		s := e.seed(t, false)
		_, err := e.authWith(e.failKeys).ChangePassword(s.owner.ID, crvPassword, crvNewPassword, s.refresh[0])
		require.ErrorIs(t, err, errAPIKeyRevocationFailed)
		e.assertUnchanged(t, s, true)
	})

	t.Run("reset_confirm_rolls_back_when_the_key_revocation_fails", func(t *testing.T) {
		s := e.seed(t, false)
		link := e.resetLink(t, s.owner.ID)
		require.ErrorIs(t, e.authWith(e.failKeys).ConfirmPasswordReset(link, crvNewPassword), errAPIKeyRevocationFailed)
		e.assertUnchanged(t, s, true)
		require.NoError(t, e.auth.ConfirmPasswordReset(link, crvNewPassword), "the link is still usable")
		e.assertEnded(t, s, 0)
	})

	t.Run("governed_deactivation_rolls_back_when_the_key_revocation_fails", func(t *testing.T) {
		s := e.seed(t, false)
		_, err := e.users(e.failKeys).SetActiveAs(ctx, e.actor, s.owner.ID, false)
		require.ErrorIs(t, err, errAPIKeyRevocationFailed)
		e.assertUnchanged(t, s, true)
	})

	t.Run("scim_deactivation_rolls_back_when_the_key_revocation_fails", func(t *testing.T) {
		s := e.seed(t, true)
		failing := e.scim(e.failKeys)
		_, err := failing.Patch(ctx, uintString(s.owner.ID), SCIMUserPatch{Active: &falseValue})
		require.ErrorIs(t, err, errAPIKeyRevocationFailed)
		e.assertUnchanged(t, s, true)
		require.ErrorIs(t, failing.Delete(ctx, uintString(s.owner.ID)), errAPIKeyRevocationFailed)
		e.assertUnchanged(t, s, true)
	})

	// --- unwired services refuse and write nothing ---------------------------

	t.Run("governed_deactivation_without_the_credential_stores_refuses", func(t *testing.T) {
		s := e.seed(t, false)
		unwired := e.users(nil)
		_, err := unwired.SetActiveAs(ctx, e.actor, s.owner.ID, false)
		require.ErrorIs(t, err, errCredentialRevocationUnavailable)
		_, err = unwired.UpdateAs(ctx, e.actor, s.owner.ID, map[string]interface{}{"is_active": false})
		require.ErrorIs(t, err, errCredentialRevocationUnavailable)
		e.assertUnchanged(t, s, true)
	})

	t.Run("scim_writes_without_the_api_key_store_refuse", func(t *testing.T) {
		s := e.seed(t, true)
		unwired := e.scim(nil)
		_, err := unwired.Patch(ctx, uintString(s.owner.ID), SCIMUserPatch{Active: &falseValue})
		require.ErrorIs(t, err, errSCIMWritesNotWired)
		in := SCIMUserInput{UserName: s.owner.Email, GivenName: "Cred", FamilyName: "Owner", Active: &falseValue, Roles: []string{crvSCIMRole}}
		_, err = unwired.Replace(ctx, uintString(s.owner.ID), in)
		require.ErrorIs(t, err, errSCIMWritesNotWired)
		require.ErrorIs(t, unwired.Delete(ctx, uintString(s.owner.ID)), errSCIMWritesNotWired)
		e.assertUnchanged(t, s, true)
	})
}

// resetLink stores a spendable reset token of userID and returns the raw
// token as the mailed link carries it.
func (e *crvEnv) resetLink(t *testing.T, userID uint) string {
	t.Helper()
	raw := fmt.Sprintf("credential-revocation-reset-%d", crvSeq.Add(1))
	require.NoError(t, e.db.Create(&models.PasswordResetToken{UserID: userID, TokenHash: hashOpaqueToken(raw, crvSecret),
		ExpiresAt: time.Now().Add(30 * time.Minute)}).Error)
	return raw
}
