package service

// Issue #454: the sign-in audit log. TestAuthAuditOutcomes{SQLite,MySQL} and
// TestAuthAuditPrivacy{SQLite,MySQL} run one table of checks through the real
// NewAuthServiceWithSessions, the OIDC service and the audit writer on a
// SQLite file opened by the production connector and on a schema built by the
// real migrations (TEST_MYSQL_DSN, also pointed at MariaDB 10.11).

import (
	"bytes"
	"context"
	"encoding/json"
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
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	authAuditTestPassword = "Audit-Passw0rd!-0454"
	authAuditTestIP       = "198.51.100.77"
	authAuditTestNetwork  = "198.51.100.0/24"
	authAuditTestUA       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	authAuditTestFamily   = "Chrome on Windows"
)

// authAuditCtx is the context the handlers build: request id, client IP and
// user agent.
func authAuditCtx(requestID string) context.Context {
	return utils.WithClientInfo(utils.WithRequestID(context.Background(), requestID), authAuditTestIP, authAuditTestUA)
}

// authAuditEnv is the real auth service and audit writer over one database.
type authAuditEnv struct {
	db    *gorm.DB
	auth  *authService
	audit AuthAuditService
}

func newAuthAuditEnv(t *testing.T, db *gorm.DB) *authAuditEnv {
	t.Helper()
	return &authAuditEnv{
		db:    db,
		auth:  newAuthAuditAuthService(db, repository.NewAPIKeyRepository(db), oidcTestAPIKeySecret),
		audit: NewAuthAuditService(repository.NewAuthAuditRepository(db)),
	}
}

func newAuthAuditAuthService(db *gorm.DB, apiKeys repository.APIKeyRepository, apiKeySecret string) *authService {
	return NewAuthServiceWithSessions(repository.NewUserRepository(db), apiKeys,
		repository.NewRefreshTokenRepository(db), repository.NewPasswordResetTokenRepository(db),
		utils.NewTransactionManager(db), nil, oidcTestJWTConfig(), oidcTestAppBaseURL, apiKeySecret,
		WithAuthIdentities(repository.NewUserIdentityRepository(db))).(*authService)
}

// newAuthAuditResetService is newAuthAuditAuthService with mail as its
// mailer, so the reset flow is wired (#550). A nil mailer turns every reset
// request into an unavailable refusal.
func newAuthAuditResetService(db *gorm.DB, mail *ciMailer) *authService {
	return NewAuthServiceWithSessions(repository.NewUserRepository(db), repository.NewAPIKeyRepository(db),
		repository.NewRefreshTokenRepository(db), repository.NewPasswordResetTokenRepository(db),
		utils.NewTransactionManager(db), mail, oidcTestJWTConfig(), oidcTestAppBaseURL, oidcTestAPIKeySecret,
		WithAuthIdentities(repository.NewUserIdentityRepository(db))).(*authService)
}

// lastResetToken is the raw token of the newest recorded reset mail.
func lastResetToken(t *testing.T, mail *ciMailer) string {
	t.Helper()
	mails := mail.mails()
	require.NotEmpty(t, mails, "a reset mail was sent")
	raw := ciTokenOf(mails[len(mails)-1].url)
	require.NotEmpty(t, raw)
	return raw
}

// authAuditResetTokens returns stored instead of the lookup's answer when
// stored is set, and fails every claim with a not-found when lostClaim is set,
// as a concurrent confirmation that claimed first would.
type authAuditResetTokens struct {
	repository.PasswordResetTokenRepository
	stored    *models.PasswordResetToken
	lostClaim bool
}

func (r *authAuditResetTokens) WithTx(tx *gorm.DB) repository.PasswordResetTokenRepository {
	return &authAuditResetTokens{PasswordResetTokenRepository: r.PasswordResetTokenRepository.WithTx(tx), stored: r.stored, lostClaim: r.lostClaim}
}

func (r *authAuditResetTokens) GetByTokenHash(hash string) (*models.PasswordResetToken, error) {
	if r.stored != nil {
		copied := *r.stored
		return &copied, nil
	}
	return r.PasswordResetTokenRepository.GetByTokenHash(hash)
}

func (r *authAuditResetTokens) MarkUsed(id uint) error {
	if r.lostClaim {
		return gorm.ErrRecordNotFound
	}
	return r.PasswordResetTokenRepository.MarkUsed(id)
}

func (e *authAuditEnv) seedUser(t *testing.T, email string) *models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(authAuditTestPassword), bcrypt.MinCost)
	require.NoError(t, err)
	user := &models.User{Email: email, Password: string(hash), FirstName: "Audit", LastName: "User", Role: models.RoleSales, IsActive: true}
	require.NoError(t, e.db.Create(user).Error)
	return user
}

func (e *authAuditEnv) update(t *testing.T, userID uint, column string, value interface{}) {
	t.Helper()
	require.NoError(t, e.db.Model(&models.User{}).Where("id = ?", userID).Update(column, value).Error)
}

func (e *authAuditEnv) erase(t *testing.T, userID uint) {
	t.Helper()
	require.NoError(t, e.db.Delete(&models.User{}, userID).Error)
}

// seedAPIKey stores a key of the user and returns the raw key.
func (e *authAuditEnv) seedAPIKey(t *testing.T, userID uint, tag string, expiresAt *time.Time) (string, *models.APIKey) {
	t.Helper()
	raw := "gcrm_" + tag + "0454abcdef0123456789"
	key := &models.APIKey{Name: "audit " + tag, KeyHash: utils.HashAPIKeyHMAC(raw, oidcTestAPIKeySecret),
		Prefix: "0454abcd", UserID: userID, ExpiresAt: expiresAt, IsActive: true}
	require.NoError(t, e.db.Create(key).Error)
	return raw, key
}

func (e *authAuditEnv) maxAuditID(t *testing.T) uint {
	t.Helper()
	var id uint
	require.NoError(t, e.db.Raw("SELECT COALESCE(MAX(id), 0) FROM auth_audit_log").Scan(&id).Error)
	return id
}

// onlyNewRow asserts that exactly one row was written after afterID and
// returns it.
func (e *authAuditEnv) onlyNewRow(t *testing.T, afterID uint) models.AuthAuditLog {
	t.Helper()
	var rows []models.AuthAuditLog
	require.NoError(t, e.db.Where("id > ?", afterID).Order("id").Find(&rows).Error)
	require.Len(t, rows, 1, "exactly one audit row")
	return rows[0]
}

// assertRow checks method, outcome, reason and user id (0 = NULL), and the
// request facts of authAuditCtx.
func assertAuditRow(t *testing.T, row models.AuthAuditLog, method, outcome, reason string, userID uint, requestID string) {
	t.Helper()
	assert.Equal(t, method, row.Method)
	assert.Equal(t, outcome, row.Outcome)
	assert.Equal(t, reason, row.Reason)
	if userID == 0 {
		assert.Nil(t, row.UserID, "user_id is NULL")
	} else if assert.NotNil(t, row.UserID, "user_id is set") {
		assert.Equal(t, userID, *row.UserID)
	}
	assert.Equal(t, requestID, row.RequestID)
	assert.Equal(t, authAuditTestNetwork, row.ClientNetwork)
	assert.Equal(t, authAuditTestFamily, row.UserAgentFamily)
	assert.Nil(t, row.SubjectRef)
	assert.Nil(t, row.AttemptID)
	assert.EqualValues(t, 1, row.Attempts, "one outcome")
	switch {
	case outcome == AuthAuditOutcomeRefused && (method == AuthAuditMethodAPIKey || method == AuthAuditMethodPassword ||
		method == AuthAuditMethodRefresh || method == AuthAuditMethodSCIM):
		assert.NotNil(t, row.AggregateKey, "a refused api_key, password, refresh or scim outcome is counted in its bucket (#466, #553)")
	default:
		assert.Nil(t, row.AggregateKey, "a success row and every other method's row is not aggregated")
	}
	assert.WithinDuration(t, time.Now().UTC(), row.CreatedAt.UTC(), time.Minute, "created_at is now, in UTC")
}

// failingUserLookup fails the plain email lookup with a database error.
type failingUserLookup struct {
	repository.UserRepository
}

func (failingUserLookup) GetByEmail(string) (*models.User, error) {
	return nil, errors.New("connection refused")
}

func runAuthAuditOutcomes(t *testing.T, db *gorm.DB) {
	e := newAuthAuditEnv(t, db)
	var lastID uint
	login := func(t *testing.T, email, password, requestID string) (*AuthTokens, error) {
		t.Helper()
		tokens, err := e.auth.LoginWithTokens(email, password)
		lastID = e.audit.RecordPassword(authAuditCtx(requestID), tokens, err)
		return tokens, err
	}
	refresh := func(t *testing.T, token, requestID string) (*AuthTokens, error) {
		t.Helper()
		tokens, err := e.auth.RefreshAccessToken(token)
		lastID = e.audit.RecordRefresh(authAuditCtx(requestID), tokens, err)
		return tokens, err
	}
	apiKey := func(t *testing.T, svc *authService, key, requestID string) error {
		t.Helper()
		_, err := svc.ValidateAPIKey(key)
		require.Error(t, err)
		e.audit.RecordAPIKeyFailure(authAuditCtx(requestID), err)
		return err
	}

	t.Run("password_success", func(t *testing.T) {
		user := e.seedUser(t, "pw-ok-0454@crm.example.test")
		before := e.maxAuditID(t)
		tokens, err := login(t, user.Email, authAuditTestPassword, "req-pw-ok")
		require.NoError(t, err)
		require.NotEmpty(t, tokens.AccessToken)
		row := e.onlyNewRow(t, before)
		assert.Equal(t, row.ID, lastID, "Record returns the row id")
		assertAuditRow(t, row, AuthAuditMethodPassword, AuthAuditOutcomeSuccess, "", user.ID, "req-pw-ok")
	})
	t.Run("password_invalid_credentials_unknown_email", func(t *testing.T) {
		before := e.maxAuditID(t)
		_, err := login(t, "nobody-0454@crm.example.test", authAuditTestPassword, "req-pw-unknown")
		require.ErrorIs(t, err, errLoginInvalidCredentials)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "invalid_credentials", 0, "req-pw-unknown")
	})
	t.Run("password_invalid_credentials_wrong_password", func(t *testing.T) {
		user := e.seedUser(t, "pw-wrong-0454@crm.example.test")
		before := e.maxAuditID(t)
		_, err := login(t, user.Email, "Wrong-Passw0rd!", "req-pw-wrong")
		require.ErrorIs(t, err, errLoginInvalidCredentials)
		assert.Equal(t, "invalid credentials", err.Error(), "the message is unchanged")
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "invalid_credentials", user.ID, "req-pw-wrong")
	})
	t.Run("password_account_locked", func(t *testing.T) {
		user := e.seedUser(t, "pw-locked-0454@crm.example.test")
		e.update(t, user.ID, "locked_until", time.Now().Add(time.Hour))
		before := e.maxAuditID(t)
		_, err := login(t, user.Email, authAuditTestPassword, "req-pw-locked")
		require.ErrorIs(t, err, errLoginAccountLocked)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "account_locked", user.ID, "req-pw-locked")
	})
	t.Run("password_account_inactive", func(t *testing.T) {
		user := e.seedUser(t, "pw-inactive-0454@crm.example.test")
		e.update(t, user.ID, "is_active", false)
		before := e.maxAuditID(t)
		_, err := login(t, user.Email, authAuditTestPassword, "req-pw-inactive")
		require.ErrorIs(t, err, errLoginInvalidCredentials)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "account_inactive", user.ID, "req-pw-inactive")
	})
	t.Run("password_sso_account", func(t *testing.T) {
		user := e.seedUser(t, "pw-sso-0454@crm.example.test")
		require.NoError(t, e.db.Create(&models.UserIdentity{UserID: user.ID, Issuer: "https://idp.example.test", Subject: "pw-sso-0454"}).Error)
		before := e.maxAuditID(t)
		_, err := login(t, user.Email, authAuditTestPassword, "req-pw-sso")
		require.ErrorIs(t, err, errLoginInvalidCredentials)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "sso_account", user.ID, "req-pw-sso")
	})
	t.Run("password_password_changed", func(t *testing.T) {
		user := e.seedUser(t, "pw-changed-0454@crm.example.test")
		e.auth.loginBeforeLock = func(id uint) { e.update(t, id, "password", "changed-meanwhile") }
		t.Cleanup(func() { e.auth.loginBeforeLock = nil })
		before := e.maxAuditID(t)
		_, err := login(t, user.Email, authAuditTestPassword, "req-pw-changed")
		require.ErrorIs(t, err, errLoginInvalidCredentials)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "password_changed", user.ID, "req-pw-changed")
	})
	t.Run("password_account_erased", func(t *testing.T) {
		user := e.seedUser(t, "pw-erased-0454@crm.example.test")
		e.auth.loginBeforeLock = func(id uint) { e.erase(t, id) }
		t.Cleanup(func() { e.auth.loginBeforeLock = nil })
		before := e.maxAuditID(t)
		_, err := login(t, user.Email, authAuditTestPassword, "req-pw-erased")
		require.ErrorIs(t, err, errLoginInvalidCredentials)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "account_erased", user.ID, "req-pw-erased")
	})
	t.Run("password_internal_error", func(t *testing.T) {
		broken := newAuthAuditAuthService(db, repository.NewAPIKeyRepository(db), oidcTestAPIKeySecret)
		broken.userRepo = failingUserLookup{broken.userRepo}
		before := e.maxAuditID(t)
		tokens, err := broken.LoginWithTokens("pw-internal-0454@crm.example.test", authAuditTestPassword)
		require.ErrorIs(t, err, errLoginInvalidCredentials, "the answer is still the generic refusal")
		e.audit.RecordPassword(authAuditCtx("req-pw-internal"), tokens, err)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPassword, AuthAuditOutcomeRefused, "internal_error", 0, "req-pw-internal")
	})

	t.Run("refresh_success", func(t *testing.T) {
		user := e.seedUser(t, "rf-ok-0454@crm.example.test")
		tokens, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
		require.NoError(t, err)
		before := e.maxAuditID(t)
		rotated, err := refresh(t, tokens.RefreshToken, "req-rf-ok")
		require.NoError(t, err)
		require.NotEmpty(t, rotated.RefreshToken)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodRefresh, AuthAuditOutcomeSuccess, "", user.ID, "req-rf-ok")
	})
	t.Run("refresh_token_invalid", func(t *testing.T) {
		before := e.maxAuditID(t)
		_, err := refresh(t, "unknown-refresh-token-0454", "req-rf-invalid")
		require.ErrorIs(t, err, ErrInvalidRefreshToken)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodRefresh, AuthAuditOutcomeRefused, "token_invalid", 0, "req-rf-invalid")
	})
	t.Run("refresh_account_inactive", func(t *testing.T) {
		user := e.seedUser(t, "rf-inactive-0454@crm.example.test")
		tokens, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
		require.NoError(t, err)
		e.update(t, user.ID, "is_active", false)
		before := e.maxAuditID(t)
		_, err = refresh(t, tokens.RefreshToken, "req-rf-inactive")
		require.ErrorIs(t, err, ErrInvalidRefreshToken)
		assert.Equal(t, "refresh token rejected: invalid refresh token", err.Error(), "the message is unchanged")
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodRefresh, AuthAuditOutcomeRefused, "account_inactive", user.ID, "req-rf-inactive")
	})
	t.Run("refresh_account_erased", func(t *testing.T) {
		user := e.seedUser(t, "rf-erased-0454@crm.example.test")
		tokens, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
		require.NoError(t, err)
		e.erase(t, user.ID)
		before := e.maxAuditID(t)
		_, err = refresh(t, tokens.RefreshToken, "req-rf-erased")
		require.ErrorIs(t, err, ErrInvalidRefreshToken)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodRefresh, AuthAuditOutcomeRefused, "account_erased", user.ID, "req-rf-erased")
	})

	t.Run("refresh_token_reuse", func(t *testing.T) {
		user := e.seedUser(t, "rf-reuse-0547@crm.example.test")
		tokens, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
		require.NoError(t, err)
		rotated, err := e.auth.RefreshAccessToken(tokens.RefreshToken)
		require.NoError(t, err)
		secret := e.auth.sessionTokenSecret()
		firstHash := hashOpaqueToken(tokens.RefreshToken, secret)
		successorHash := hashOpaqueToken(rotated.RefreshToken, secret)
		var familyID string
		require.NoError(t, e.db.Raw("SELECT family_id FROM refresh_tokens WHERE token_hash = ?", firstHash).Scan(&familyID).Error)
		require.NotEmpty(t, familyID)

		output := captureAuthAuditLogs(t)
		before := e.maxAuditID(t)
		_, err = refresh(t, tokens.RefreshToken, "req-rf-reuse")
		require.ErrorIs(t, err, ErrInvalidRefreshToken)
		assert.Equal(t, "refresh token rejected: invalid refresh token", err.Error(), "the message is unchanged")
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodRefresh, AuthAuditOutcomeRefused, AuthAuditReasonTokenReuse, user.ID, "req-rf-reuse")
		logged := output.String()
		assert.Contains(t, logged, "Refresh token presented again after rotation", "the reuse is logged")
		for _, secretValue := range []string{tokens.RefreshToken, rotated.RefreshToken, firstHash, successorHash, familyID} {
			assert.NotContains(t, logged, secretValue, "the log carries no token, hash or family id")
		}

		// The successor's refusal is token_invalid with no user, the bucket
		// refresh_token_invalid counted in (#553): it goes into the next
		// minute, which no earlier subtest reached, so it writes a row of its
		// own.
		audit := e.audit.(*authAuditService)
		previousNow := audit.now
		t.Cleanup(func() { audit.now = previousNow })
		nextMinute := time.Now().UTC().Truncate(time.Minute).Add(time.Minute)
		audit.now = func() time.Time { return nextMinute }
		before = e.maxAuditID(t)
		_, err = refresh(t, rotated.RefreshToken, "req-rf-reuse-successor")
		require.ErrorIs(t, err, ErrInvalidRefreshToken)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodRefresh, AuthAuditOutcomeRefused, AuthAuditReasonTokenInvalid, 0, "req-rf-reuse-successor")
	})

	t.Run("api_key_key_invalid", func(t *testing.T) {
		before := e.maxAuditID(t)
		err := apiKey(t, e.auth, "gcrm_bogus0454", "req-ak-invalid")
		assert.Equal(t, "invalid API key", err.Error(), "the message is unchanged")
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, "key_invalid", 0, "req-ak-invalid")
	})
	t.Run("api_key_key_revoked", func(t *testing.T) {
		user := e.seedUser(t, "ak-revoked-0454@crm.example.test")
		raw, key := e.seedAPIKey(t, user.ID, "revoked", nil)
		require.NoError(t, e.db.Model(&models.APIKey{}).Where("id = ?", key.ID).Update("is_active", false).Error)
		var stored models.APIKey
		require.NoError(t, e.db.First(&stored, key.ID).Error)
		require.Nil(t, stored.LastUsedAt, "the seeded key has never been used")
		before := e.maxAuditID(t)
		err := apiKey(t, e.auth, raw, "req-ak-revoked")
		assert.Equal(t, "API key has been revoked", err.Error())
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, "key_revoked", user.ID, "req-ak-revoked")
		var after models.APIKey
		require.NoError(t, e.db.First(&after, key.ID).Error)
		assert.Nil(t, after.LastUsedAt, "a revoked key is never stamped as used")
	})
	t.Run("api_key_key_expired", func(t *testing.T) {
		user := e.seedUser(t, "ak-expired-0454@crm.example.test")
		expired := time.Now().Add(-time.Hour)
		raw, _ := e.seedAPIKey(t, user.ID, "expired", &expired)
		before := e.maxAuditID(t)
		err := apiKey(t, e.auth, raw, "req-ak-expired")
		assert.Equal(t, "API key expired", err.Error())
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, "key_expired", user.ID, "req-ak-expired")
	})
	t.Run("api_key_account_inactive", func(t *testing.T) {
		user := e.seedUser(t, "ak-inactive-0454@crm.example.test")
		raw, _ := e.seedAPIKey(t, user.ID, "inactive", nil)
		e.update(t, user.ID, "is_active", false)
		before := e.maxAuditID(t)
		err := apiKey(t, e.auth, raw, "req-ak-inactive")
		assert.Equal(t, "API key owner is not active", err.Error())
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, "account_inactive", user.ID, "req-ak-inactive")
	})
	t.Run("api_key_customer_account", func(t *testing.T) {
		user := e.seedUser(t, "ak-customer-0532@crm.example.test")
		raw, _ := e.seedAPIKey(t, user.ID, "customer", nil)
		e.update(t, user.ID, "role", models.RoleCustomer)
		before := e.maxAuditID(t)
		err := apiKey(t, e.auth, raw, "req-ak-customer")
		assert.Equal(t, "API key owner is a customer account", err.Error())
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, "customer_login_role", user.ID, "req-ak-customer")
	})
	t.Run("api_key_account_erased", func(t *testing.T) {
		user := e.seedUser(t, "ak-erased-0454@crm.example.test")
		raw, _ := e.seedAPIKey(t, user.ID, "erased", nil)
		// A key that escaped the erasure purge: the owner row alone is gone.
		e.erase(t, user.ID)
		before := e.maxAuditID(t)
		err := apiKey(t, e.auth, raw, "req-ak-erased")
		assert.Equal(t, "invalid API key", err.Error())
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, "account_erased", user.ID, "req-ak-erased")
	})
	t.Run("request_id_filtered", func(t *testing.T) {
		// Three key_invalid refusals of one network would share the row of
		// their minute (#466): each goes one minute later, so each writes a
		// row of its own and the filter is seen on every one.
		audit := e.audit.(*authAuditService)
		previousNow := audit.now
		t.Cleanup(func() { audit.now = previousNow })
		at := time.Now().UTC().Add(time.Hour)
		for _, bad := range []string{strings.Repeat("r", 100), "has space", "mail@example.test"} {
			at = at.Add(time.Minute)
			fixed := at
			audit.now = func() time.Time { return fixed }
			before := e.maxAuditID(t)
			apiKey(t, e.auth, "gcrm_bogus0454", bad)
			row := e.onlyNewRow(t, before)
			assert.Equal(t, "", row.RequestID, "a request id outside the pattern is not stored")
		}
	})

	// #550: password change, password reset request and completion, logout.
	const newPassword = "Audit-New-Passw0rd!-0550"
	mail := &ciMailer{}
	resets := newAuthAuditResetService(db, mail)
	changePassword := func(t *testing.T, userID uint, current, refreshToken, requestID string) error {
		t.Helper()
		_, err := e.auth.ChangePassword(userID, current, newPassword, refreshToken)
		lastID = e.audit.RecordPasswordChange(authAuditCtx(requestID), userID, err)
		return err
	}
	resetRequest := func(t *testing.T, email, requestID string) (uint, error) {
		t.Helper()
		id, err := resets.IssuePasswordReset(email)
		lastID = e.audit.RecordPasswordResetRequest(authAuditCtx(requestID), id, err)
		return id, err
	}
	resetConfirm := func(t *testing.T, svc *authService, token, requestID string) error {
		t.Helper()
		id, err := svc.CompletePasswordReset(token, newPassword)
		lastID = e.audit.RecordPasswordResetConfirm(authAuditCtx(requestID), id, err)
		return err
	}
	assertResetRefusal := func(t *testing.T, err error) {
		t.Helper()
		require.ErrorIs(t, err, ErrInvalidResetToken)
		assert.Equal(t, "reset token rejected: invalid or expired reset token", err.Error(), "the message is unchanged")
	}
	passwordIs := func(t *testing.T, userID uint, password string) bool {
		t.Helper()
		var stored models.User
		require.NoError(t, db.Unscoped().First(&stored, userID).Error)
		return bcrypt.CompareHashAndPassword([]byte(stored.Password), []byte(password)) == nil
	}

	t.Run("password_change_success", func(t *testing.T) {
		user := e.seedUser(t, "pc-ok-0550@crm.example.test")
		tokens, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
		require.NoError(t, err)
		before := e.maxAuditID(t)
		require.NoError(t, changePassword(t, user.ID, authAuditTestPassword, tokens.RefreshToken, "req-pc-ok"))
		row := e.onlyNewRow(t, before)
		assert.Equal(t, row.ID, lastID)
		assertAuditRow(t, row, AuthAuditMethodPasswordChange, AuthAuditOutcomeSuccess, "", user.ID, "req-pc-ok")
	})
	t.Run("password_change_invalid_credentials", func(t *testing.T) {
		user := e.seedUser(t, "pc-wrong-0550@crm.example.test")
		before := e.maxAuditID(t)
		err := changePassword(t, user.ID, "Wrong-Passw0rd!", "", "req-pc-wrong")
		require.ErrorIs(t, err, ErrInvalidCurrentPassword)
		assert.Equal(t, ErrInvalidCurrentPassword.Error(), err.Error(), "the message is unchanged")
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPasswordChange, AuthAuditOutcomeRefused, "invalid_credentials", user.ID, "req-pc-wrong")
	})
	t.Run("password_change_session_stale", func(t *testing.T) {
		user := e.seedUser(t, "pc-stale-0550@crm.example.test")
		other := e.seedUser(t, "pc-stale-other-0550@crm.example.test")
		foreign, err := e.auth.LoginWithTokens(other.Email, authAuditTestPassword)
		require.NoError(t, err)
		for name, token := range map[string]string{"foreign": foreign.RefreshToken, "unknown": "unknown-refresh-token-0550"} {
			before := e.maxAuditID(t)
			err := changePassword(t, user.ID, authAuditTestPassword, token, "req-pc-stale")
			require.ErrorIs(t, err, ErrSessionStale, name)
			assert.Equal(t, ErrSessionStale.Error(), err.Error(), "the message is unchanged")
			assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPasswordChange, AuthAuditOutcomeRefused, "session_stale", user.ID, "req-pc-stale")
		}
		assert.True(t, passwordIs(t, user.ID, authAuditTestPassword), "the password is unchanged")
	})
	t.Run("password_change_account_inactive", func(t *testing.T) {
		user := e.seedUser(t, "pc-inactive-0550@crm.example.test")
		e.update(t, user.ID, "is_active", false)
		before := e.maxAuditID(t)
		err := changePassword(t, user.ID, authAuditTestPassword, "", "req-pc-inactive")
		require.ErrorIs(t, err, ErrAccountUnavailable)
		assert.Equal(t, ErrAccountUnavailable.Error(), err.Error(), "the message is unchanged")
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPasswordChange, AuthAuditOutcomeRefused, "account_inactive", user.ID, "req-pc-inactive")
	})
	t.Run("password_change_account_erased", func(t *testing.T) {
		user := e.seedUser(t, "pc-erased-0550@crm.example.test")
		e.erase(t, user.ID)
		before := e.maxAuditID(t)
		err := changePassword(t, user.ID, authAuditTestPassword, "", "req-pc-erased")
		require.ErrorIs(t, err, ErrAccountUnavailable)
		assert.Equal(t, ErrAccountUnavailable.Error(), err.Error(), "the message is unchanged")
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodPasswordChange, AuthAuditOutcomeRefused, "account_erased", user.ID, "req-pc-erased")
	})

	t.Run("reset_request_success", func(t *testing.T) {
		user := e.seedUser(t, "rr-ok-0550@crm.example.test")
		before := e.maxAuditID(t)
		id, err := resetRequest(t, user.Email, "req-rr-ok")
		require.NoError(t, err)
		assert.Equal(t, user.ID, id)
		require.NoError(t, resets.RequestPasswordReset(user.Email), "RequestPasswordReset still answers nil")
		rows := []models.AuthAuditLog{}
		require.NoError(t, db.Where("id > ?", before).Find(&rows).Error)
		require.Len(t, rows, 1, "RequestPasswordReset itself writes no row")
		assert.Equal(t, rows[0].ID, lastID)
		assertAuditRow(t, rows[0], AuthAuditMethodResetRequest, AuthAuditOutcomeSuccess, "", user.ID, "req-rr-ok")
	})
	t.Run("reset_request_account_unknown", func(t *testing.T) {
		before := e.maxAuditID(t)
		id, err := resetRequest(t, "nobody-0550@crm.example.test", "req-rr-unknown")
		require.ErrorIs(t, err, errPasswordResetNotIssued)
		assert.Zero(t, id)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodResetRequest, AuthAuditOutcomeRefused, "account_unknown", 0, "req-rr-unknown")
	})
	t.Run("reset_request_account_inactive", func(t *testing.T) {
		user := e.seedUser(t, "rr-inactive-0550@crm.example.test")
		e.update(t, user.ID, "is_active", false)
		before := e.maxAuditID(t)
		_, err := resetRequest(t, user.Email, "req-rr-inactive")
		require.ErrorIs(t, err, errPasswordResetNotIssued)
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodResetRequest, AuthAuditOutcomeRefused, "account_inactive", user.ID, "req-rr-inactive")
	})
	t.Run("reset_request_sso_account", func(t *testing.T) {
		user := e.seedUser(t, "rr-sso-0550@crm.example.test")
		require.NoError(t, e.db.Create(&models.UserIdentity{UserID: user.ID, Issuer: "https://idp.example.test", Subject: "rr-sso-0550"}).Error)
		sent := len(mail.mails())
		before := e.maxAuditID(t)
		_, err := resetRequest(t, user.Email, "req-rr-sso")
		require.ErrorIs(t, err, errPasswordResetNotIssued)
		assert.Len(t, mail.mails(), sent, "no mail")
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodResetRequest, AuthAuditOutcomeRefused, "sso_account", user.ID, "req-rr-sso")
	})

	t.Run("reset_confirm_success", func(t *testing.T) {
		user := e.seedUser(t, "rc-ok-0550@crm.example.test")
		_, err := resets.IssuePasswordReset(user.Email)
		require.NoError(t, err)
		before := e.maxAuditID(t)
		require.NoError(t, resetConfirm(t, resets, lastResetToken(t, mail), "req-rc-ok"))
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodResetConfirm, AuthAuditOutcomeSuccess, "", user.ID, "req-rc-ok")
		assert.True(t, passwordIs(t, user.ID, newPassword))
	})
	t.Run("reset_confirm_token_invalid_unknown", func(t *testing.T) {
		before := e.maxAuditID(t)
		assertResetRefusal(t, resetConfirm(t, resets, "unknown-reset-token-0550", "req-rc-unknown"))
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodResetConfirm, AuthAuditOutcomeRefused, "token_invalid", 0, "req-rc-unknown")
	})
	t.Run("reset_confirm_token_invalid_spent", func(t *testing.T) {
		user := e.seedUser(t, "rc-spent-0550@crm.example.test")
		_, err := resets.IssuePasswordReset(user.Email)
		require.NoError(t, err)
		raw := lastResetToken(t, mail)
		require.NoError(t, resetConfirm(t, resets, raw, "req-rc-first"))
		before := e.maxAuditID(t)
		assertResetRefusal(t, resetConfirm(t, resets, raw, "req-rc-spent"))
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodResetConfirm, AuthAuditOutcomeRefused, "token_invalid", 0, "req-rc-spent")
	})
	t.Run("reset_confirm_account_inactive", func(t *testing.T) {
		user := e.seedUser(t, "rc-inactive-0550@crm.example.test")
		_, err := resets.IssuePasswordReset(user.Email)
		require.NoError(t, err)
		e.update(t, user.ID, "is_active", false)
		before := e.maxAuditID(t)
		assertResetRefusal(t, resetConfirm(t, resets, lastResetToken(t, mail), "req-rc-inactive"))
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodResetConfirm, AuthAuditOutcomeRefused, "account_inactive", user.ID, "req-rc-inactive")
		assert.True(t, passwordIs(t, user.ID, authAuditTestPassword), "the password is unchanged")
	})
	// The stored-token refusals: the shipped lookup filters used and expired
	// tokens, so the owner is named only when a concurrent confirmation
	// claimed the token first, or a lookup returned a used row anyway.
	t.Run("reset_confirm_token_invalid_lost_claim", func(t *testing.T) {
		user := e.seedUser(t, "rc-lost-0550@crm.example.test")
		_, err := resets.IssuePasswordReset(user.Email)
		require.NoError(t, err)
		stub := newAuthAuditResetService(db, mail)
		stub.resetTokenRepo = &authAuditResetTokens{PasswordResetTokenRepository: stub.resetTokenRepo, lostClaim: true}
		before := e.maxAuditID(t)
		assertResetRefusal(t, resetConfirm(t, stub, lastResetToken(t, mail), "req-rc-lost"))
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodResetConfirm, AuthAuditOutcomeRefused, "token_invalid", user.ID, "req-rc-lost")
		assert.True(t, passwordIs(t, user.ID, authAuditTestPassword), "the password is unchanged")
	})
	t.Run("reset_confirm_token_invalid_used_row", func(t *testing.T) {
		user := e.seedUser(t, "rc-used-0550@crm.example.test")
		usedAt := time.Now().Add(-time.Minute)
		stub := newAuthAuditResetService(db, mail)
		stub.resetTokenRepo = &authAuditResetTokens{PasswordResetTokenRepository: stub.resetTokenRepo,
			stored: &models.PasswordResetToken{UserID: user.ID, TokenHash: "used", UsedAt: &usedAt, ExpiresAt: time.Now().Add(time.Hour)}}
		before := e.maxAuditID(t)
		assertResetRefusal(t, resetConfirm(t, stub, "used-reset-token-0550", "req-rc-used"))
		assertAuditRow(t, e.onlyNewRow(t, before), AuthAuditMethodResetConfirm, AuthAuditOutcomeRefused, "token_invalid", user.ID, "req-rc-used")
		assert.True(t, passwordIs(t, user.ID, authAuditTestPassword), "the password is unchanged")
	})

	t.Run("logout_success", func(t *testing.T) {
		user := e.seedUser(t, "lo-ok-0550@crm.example.test")
		tokens, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
		require.NoError(t, err)
		before := e.maxAuditID(t)
		err = e.auth.Logout(user.ID, tokens.RefreshToken)
		require.NoError(t, err)
		lastID = e.audit.RecordLogout(authAuditCtx("req-lo-ok"), user.ID, err)
		row := e.onlyNewRow(t, before)
		assert.Equal(t, row.ID, lastID)
		assertAuditRow(t, row, AuthAuditMethodLogout, AuthAuditOutcomeSuccess, "", user.ID, "req-lo-ok")
		assert.Nil(t, row.APIKeyID, "a row of another method names no API key")
	})

	// API key management (#551): one row each, with the caller and the key
	// id in api_key_id, read back from the database.
	t.Run("api_key_change_rows", func(t *testing.T) {
		user := e.seedUser(t, "ak-change-0551@crm.example.test")
		const keyID = uint(4242)
		cases := []struct {
			record    func(ctx context.Context, userID, keyID uint) uint
			method    string
			outcome   string
			reason    string
			requestID string
		}{
			{e.audit.RecordAPIKeyCreate, AuthAuditMethodAPIKeyCreate, AuthAuditOutcomeSuccess, "", "req-ak-create"},
			{e.audit.RecordAPIKeyRevoke, AuthAuditMethodAPIKeyRevoke, AuthAuditOutcomeSuccess, "", "req-ak-revoke"},
			{e.audit.RecordAPIKeyEnableRefused, AuthAuditMethodAPIKeyEnable, AuthAuditOutcomeRefused, "key_revoked", "req-ak-enable"},
		}
		for _, tc := range cases {
			before := e.maxAuditID(t)
			id := tc.record(authAuditCtx(tc.requestID), user.ID, keyID)
			row := e.onlyNewRow(t, before)
			assert.Equal(t, row.ID, id, "the method returns the row id")
			assertAuditRow(t, row, tc.method, tc.outcome, tc.reason, user.ID, tc.requestID)
			if assert.NotNil(t, row.APIKeyID, "api_key_id is set") {
				assert.Equal(t, keyID, *row.APIKeyID)
			}
		}
	})
}

func TestAuthAuditOutcomesSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runAuthAuditOutcomes(t, db)
}

func TestAuthAuditOutcomesMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runAuthAuditOutcomes(t, db)
}

// TestAuthAuditUnavailableRefresh: an operational refresh failure (the 503
// path) is recorded as unavailable with the owner it named.
func TestAuthAuditUnavailableRefresh(t *testing.T) {
	entry := outcomeEntry(AuthAuditMethodRefresh, nil, withAuthAuditUser(fmt.Errorf("commit: %w", utils.ErrCommitFailed), 7), AuthAuditReasonUnavailable)
	assert.Equal(t, AuthAuditReasonUnavailable, entry.Reason)
	assert.Equal(t, uint(7), entry.UserID)
	assert.Equal(t, AuthAuditOutcomeRefused, entry.Outcome)
	reason, _ := AuthAuditReason(nil)
	assert.Equal(t, "", reason)
}

// authAuditLogLines returns the JSON log lines written after from.
func authAuditLogLines(output *bytes.Buffer, from int) []map[string]interface{} {
	var lines []map[string]interface{}
	for _, raw := range strings.Split(strings.TrimSpace(output.String()[from:]), "\n") {
		var line map[string]interface{}
		if raw != "" && json.Unmarshal([]byte(raw), &line) == nil {
			lines = append(lines, line)
		}
	}
	return lines
}

func captureAuthAuditLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var output bytes.Buffer
	utils.Logger.SetOutput(&output)
	t.Cleanup(func() { utils.Logger = previous })
	return &output
}

// stubSSO answers SSOConfig with a fixed configuration.
type stubSSO struct {
	SSOSettingsService
	cfg EffectiveSSOConfig
}

func (s stubSSO) SSOConfig(context.Context) (EffectiveSSOConfig, error) { return s.cfg, nil }

// failingClaim fails every claim with a database error.
type failingClaim struct {
	repository.OIDCLoginAttemptRepository
}

func (failingClaim) Claim(string, time.Time) (*models.OIDCLoginAttempt, error) {
	return nil, errors.New("connection refused")
}

// TestAuthAuditOIDCReasonCoverage: every registry constant an
// oidcRefuse/oidcRefuseWith site passes (the writer scan of
// auth_audit_vocabulary_test.go), recorded through the step Callback finishes with,
// yields a row with exactly that reason and a refusal log line naming the
// row; the 503s, sso_disabled and internal_error are driven through Callback
// itself.
func TestAuthAuditOIDCReasonCoverage(t *testing.T) {
	parsed := authAuditOIDCRefusalCodes(t)
	require.Len(t, parsed, 25, "the refusal codes come from the writer scan")
	t.Logf("parsed %d refusal codes: %s", len(parsed), strings.Join(parsed, " "))

	db, competitor := openOIDCTestSQLite(t)
	f := newOIDCFixture(t, db, competitor)
	output := captureAuthAuditLogs(t)

	lineFor := func(t *testing.T, from int, msg string) map[string]interface{} {
		t.Helper()
		var found map[string]interface{}
		for _, line := range authAuditLogLines(output, from) {
			if line["msg"] == msg {
				require.Nil(t, found, "one %q line", msg)
				found = line
			}
		}
		require.NotNil(t, found, "a %q line is logged", msg)
		return found
	}

	for i, code := range parsed {
		t.Run("refusal_"+code, func(t *testing.T) {
			f.reset(t)
			from := output.Len()
			attemptID := uint(4540 + i)
			_, err := f.svc.finishCallback(context.Background(), nil, oidcCallbackFacts{attemptID: attemptID}, oidcRefuse(code))
			require.ErrorIs(t, err, ErrSSOSignInFailed)
			row := f.lastAudit(t)
			assert.Equal(t, AuthAuditMethodOIDC, row.Method)
			assert.Equal(t, AuthAuditOutcomeRefused, row.Outcome)
			assert.Equal(t, code, row.Reason)
			assert.EqualValues(t, 1, row.Attempts)
			assert.Nil(t, row.AggregateKey)
			require.NotNil(t, row.AttemptID)
			assert.Equal(t, attemptID, *row.AttemptID)
			line := lineFor(t, from, "Single sign-on refused")
			assert.Equal(t, code, line["error_code"])
			assert.Equal(t, float64(row.ID), line["audit_id"])
		})
	}

	// assertUnavailable checks the row and the line of a 503 callback.
	assertOutcome := func(t *testing.T, from int, reason, msg string) {
		t.Helper()
		require.Equal(t, int64(1), f.count(t, "auth_audit_log", ""), "one row per callback")
		row := f.lastAudit(t)
		assert.Equal(t, AuthAuditOutcomeRefused, row.Outcome)
		assert.Equal(t, reason, row.Reason)
		if msg != "" {
			line := lineFor(t, from, msg)
			assert.Equal(t, reason, line["error_code"])
			assert.Equal(t, float64(row.ID), line["audit_id"], "the line names the audit row")
		}
	}
	t.Run("not_configured", func(t *testing.T) {
		f.reset(t)
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-cov-nc", "cov-nc@idp.example.test"))
		cfg, err := f.sso.SSOConfig(context.Background())
		require.NoError(t, err)
		cfg.ClientID = ""
		f.useSSO(stubSSO{f.defaultSSO, cfg})
		from := output.Len()
		_, err = f.svc.Callback(context.Background(), code, state, state)
		require.ErrorIs(t, err, ErrSSOUnavailable)
		assert.Equal(t, ErrSSOUnavailable.Error(), err.Error())
		assertOutcome(t, from, "not_configured", "Single sign-on unavailable")
	})
	t.Run("discovery_failed", func(t *testing.T) {
		f.reset(t)
		code, state := f.issuer.authorize(t, f.start(t), oidcValid("subject-cov-df", "cov-df@idp.example.test"))
		// The Start cached the discovery; drop it so the callback discovers.
		f.clearProviderCache()
		f.issuer.setDiscoveryRedirect(true)
		from := output.Len()
		_, err := f.svc.Callback(context.Background(), code, state, state)
		require.ErrorIs(t, err, ErrSSOUnavailable)
		assertOutcome(t, from, "discovery_failed", "Single sign-on unavailable")
	})
	t.Run("token_exchange_failed", func(t *testing.T) {
		f.reset(t)
		f.issuer.setTokenStatus(500)
		from := output.Len()
		_, err := f.signIn(t, oidcValid("subject-cov-te", "cov-te@idp.example.test"))
		require.ErrorIs(t, err, ErrSSOUnavailable)
		assertOutcome(t, from, "token_exchange_failed", "Single sign-on unavailable")
	})
	t.Run("sso_disabled", func(t *testing.T) {
		f.reset(t)
		f.useSSO(stubSSO{f.defaultSSO, EffectiveSSOConfig{Enabled: false}})
		_, err := f.svc.Callback(context.Background(), "code-cov-disabled", "state-cov-disabled", "state-cov-disabled")
		require.ErrorIs(t, err, ErrSSODisabled)
		assertOutcome(t, 0, "sso_disabled", "")
	})
	t.Run("internal_error", func(t *testing.T) {
		f.reset(t)
		f.svc.attempts = failingClaim{f.svc.attempts}
		_, err := f.svc.Callback(context.Background(), "code-cov-internal", "state-cov-internal", "state-cov-internal")
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrSSOSignInFailed)
		assertOutcome(t, 0, "internal_error", "")
	})
}

func runAuthAuditPrivacy(t *testing.T, db, competitor *gorm.DB) {
	f := newOIDCFixture(t, db, competitor)
	f.reset(t)
	output := captureAuthAuditLogs(t)
	e := &authAuditEnv{db: db, auth: f.auth, audit: f.audit}

	const (
		ssoSub      = "subject-privacy-ok-0454"
		ssoEmail    = "privacy-ok-0454@idp.example.test"
		refusedSub  = "subject-privacy-refused-0454"
		refusedMail = "privacy-refused-0454@idp.example.test"
		pwEmail     = "privacy-pw-0454@crm.example.test"
		wrongPass   = "Wr0ng-Privacy-Passw0rd!-0454"
	)
	var secrets []string
	callback := func(spec oidcTokenSpec, requestID string) (*AuthTokens, error) {
		authURL, err := oidcStartURL(f.svc.Start(authAuditCtx(requestID)))
		require.NoError(t, err)
		code, state := f.issuer.authorize(t, authURL, spec)
		var attempt models.OIDCLoginAttempt
		require.NoError(t, f.db.Where("state_hash = ?", hashOpaqueToken(state, f.auth.sessionTokenSecret())).First(&attempt).Error)
		secrets = append(secrets, code, state, attempt.Nonce, attempt.CodeVerifier)
		tokens, err := f.svc.Callback(authAuditCtx(requestID), code, state, state)
		secrets = append(secrets, f.issuer.lastIDToken(), f.issuer.lastAccessToken())
		return tokens, err
	}

	// An SSO success, and a refusal after the ID token verified.
	ssoTokens, err := callback(oidcValid(ssoSub, ssoEmail), "req-privacy-sso-ok")
	require.NoError(t, err)
	secrets = append(secrets, ssoTokens.AccessToken, ssoTokens.RefreshToken, ssoSub, ssoEmail)
	_, err = callback(oidcTokenSpec{sub: refusedSub, email: refusedMail, groups: []string{"nobody-privacy-0454"}}, "req-privacy-sso-no")
	require.ErrorIs(t, err, ErrSSOSignInFailed)
	secrets = append(secrets, refusedSub, refusedMail)

	// A password failure and a refresh.
	user := e.seedUser(t, pwEmail)
	tokens, err := e.auth.LoginWithTokens(pwEmail, wrongPass)
	require.Error(t, err)
	e.audit.RecordPassword(authAuditCtx("req-privacy-pw"), tokens, err)
	tokens, err = e.auth.LoginWithTokens(pwEmail, authAuditTestPassword)
	require.NoError(t, err)
	rotated, err := e.auth.RefreshAccessToken(tokens.RefreshToken)
	require.NoError(t, err)
	e.audit.RecordRefresh(authAuditCtx("req-privacy-rf"), rotated, err)
	secrets = append(secrets, pwEmail, wrongPass, authAuditTestPassword, tokens.AccessToken, tokens.RefreshToken,
		rotated.AccessToken, rotated.RefreshToken, user.FirstName+" "+user.LastName)

	// An API-key failure.
	const badKey = "gcrm_privacy0454deadbeefcafe"
	_, err = e.auth.ValidateAPIKey(badKey)
	require.Error(t, err)
	e.audit.RecordAPIKeyFailure(authAuditCtx("req-privacy-ak"), err)
	secrets = append(secrets, badKey, authAuditTestIP, authAuditTestUA)

	// #550: a reset request for the known and an unknown email, its
	// completion, a password change that renews the session, and a logout,
	// on a service whose mailer records the link and logs nothing.
	const (
		unknownMail   = "privacy-unknown-0550@crm.example.test"
		resetPassword = "Privacy-Reset-Passw0rd!-0550"
		changedPass   = "Privacy-Changed-Passw0rd!-0550"
	)
	mail := &ciMailer{}
	resets := newAuthAuditResetService(db, mail)
	id, err := resets.IssuePasswordReset(pwEmail)
	require.NoError(t, err)
	e.audit.RecordPasswordResetRequest(authAuditCtx("req-privacy-rr"), id, err)
	id, err = resets.IssuePasswordReset(unknownMail)
	require.Error(t, err)
	e.audit.RecordPasswordResetRequest(authAuditCtx("req-privacy-rr-unknown"), id, err)
	resetURL := mail.mails()[0].url
	rawReset := lastResetToken(t, mail)
	id, err = resets.CompletePasswordReset(rawReset, resetPassword)
	require.NoError(t, err)
	e.audit.RecordPasswordResetConfirm(authAuditCtx("req-privacy-rc"), id, err)
	session, err := resets.LoginWithTokens(pwEmail, resetPassword)
	require.NoError(t, err)
	renewed, err := resets.ChangePassword(user.ID, resetPassword, changedPass, session.RefreshToken)
	require.NoError(t, err)
	e.audit.RecordPasswordChange(authAuditCtx("req-privacy-pc"), user.ID, err)
	err = resets.Logout(user.ID, renewed.RefreshToken)
	require.NoError(t, err)
	e.audit.RecordLogout(authAuditCtx("req-privacy-lo"), user.ID, err)
	secrets = append(secrets, unknownMail, resetPassword, changedPass, rawReset, resetURL,
		session.AccessToken, session.RefreshToken, renewed.AccessToken, renewed.RefreshToken)

	var rows []map[string]interface{}
	require.NoError(t, db.Table("auth_audit_log").Find(&rows).Error)
	require.Len(t, rows, 10, "one row per outcome")
	var stored strings.Builder
	for _, row := range rows {
		for column, value := range row {
			if raw, ok := value.([]byte); ok {
				value = string(raw)
			}
			fmt.Fprintf(&stored, "%s=%v\n", column, value)
		}
	}
	logs := output.String()
	for _, secret := range secrets {
		require.NotEmpty(t, secret)
		assert.NotContains(t, stored.String(), secret, "the audit rows hold no %q", secret)
		assert.NotContains(t, logs, secret, "the logs hold no %q", secret)
	}
	assert.Contains(t, stored.String(), authAuditTestNetwork)
	assert.Contains(t, stored.String(), authAuditTestFamily)
}

func TestAuthAuditPrivacySQLite(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	runAuthAuditPrivacy(t, db, competitor)
}

func TestAuthAuditPrivacyMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runAuthAuditPrivacy(t, db, db)
}

// TestAuthAuditSubjectReference: the subject_ref of a refused identity is a
// stable keyed hash of (issuer, subject).
func TestAuthAuditSubjectReference(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	f := newOIDCFixture(t, db, competitor)
	f.reset(t)
	refuse := func(t *testing.T, sub string) string {
		t.Helper()
		_, err := f.signIn(t, oidcTokenSpec{sub: sub, email: sub + "@idp.example.test", groups: []string{"nobody-ref-0454"}})
		require.ErrorIs(t, err, ErrSSOSignInFailed)
		row := f.lastAudit(t)
		require.NotNil(t, row.SubjectRef)
		require.Regexp(t, `^[0-9a-f]{64}$`, *row.SubjectRef)
		return *row.SubjectRef
	}
	first := refuse(t, "subject-ref-a-0454")
	assert.Equal(t, first, refuse(t, "subject-ref-a-0454"), "the same identity gives the same reference")
	assert.NotEqual(t, first, refuse(t, "subject-ref-b-0454"), "another subject gives another reference")
	assert.Equal(t, AuthAuditSubjectRef(oidcTestAPIKeySecret, f.issuer.URL(), "subject-ref-a-0454"), first)
	assert.NotEqual(t, first, AuthAuditSubjectRef(oidcTestAPIKeySecret, f.issuer.URL()+"/other", "subject-ref-a-0454"),
		"another issuer gives another reference")
	assert.NotEqual(t, AuthAuditSubjectRef("k", "ab", "c"), AuthAuditSubjectRef("k", "a", "bc"),
		"the issuer length keeps concatenations apart")

	// Another issuer through the service: a second fixture on its own issuer.
	db2, competitor2 := openOIDCTestSQLite(t)
	other := newOIDCFixture(t, db2, competitor2)
	other.reset(t)
	require.NotEqual(t, f.issuer.URL(), other.issuer.URL())
	_, err := other.signIn(t, oidcTokenSpec{sub: "subject-ref-a-0454", email: "ref-other@idp.example.test", groups: []string{"nobody-ref-0454"}})
	require.ErrorIs(t, err, ErrSSOSignInFailed)
	otherRow := other.lastAudit(t)
	require.NotNil(t, otherRow.SubjectRef)
	assert.NotEqual(t, first, *otherRow.SubjectRef, "the same subject at another issuer gives another reference")

	// Another API_KEY_SECRET.
	f.svc.auth = newAuthAuditAuthService(db, repository.NewAPIKeyRepository(db), "another-api-key-secret-0454-0123456789abcdef")
	t.Cleanup(func() { f.svc.auth = f.auth })
	assert.NotEqual(t, first, refuse(t, "subject-ref-a-0454"), "another key gives another reference")
}

// failingAuditRepo fails every insert and every aggregated write.
type failingAuditRepo struct {
	repository.AuthAuditRepository
	creates    int
	aggregates int
}

func (r *failingAuditRepo) Create(*models.AuthAuditLog) error {
	r.creates++
	return errors.New("audit table unavailable")
}

func (r *failingAuditRepo) RecordAggregated(*models.AuthAuditLog) (uint, error) {
	r.aggregates++
	return 0, errors.New("audit table unavailable")
}

// TestAuthAuditWriteFailure: a failed audit write is logged once and changes
// no sign-in answer.
func TestAuthAuditWriteFailure(t *testing.T) {
	db, competitor := openOIDCTestSQLite(t)
	f := newOIDCFixture(t, db, competitor)
	f.reset(t)
	output := captureAuthAuditLogs(t)
	failing := &failingAuditRepo{}
	audit := NewAuthAuditService(failing)
	f.svc.audit = audit
	e := &authAuditEnv{db: db, auth: f.auth, audit: audit}
	failedLines := func(from int) int {
		n := 0
		for _, line := range authAuditLogLines(output, from) {
			if line["error_code"] == "audit_write_failed" {
				n++
				assert.Equal(t, "auth_audit", line["component"])
			}
		}
		return n
	}

	user := e.seedUser(t, "wf-0454@crm.example.test")
	from := output.Len()
	tokens, err := e.auth.LoginWithTokens(user.Email, authAuditTestPassword)
	require.NoError(t, err)
	assert.Zero(t, audit.RecordPassword(authAuditCtx("req-wf-ok"), tokens, err))
	assert.NotEmpty(t, tokens.AccessToken, "the login still returns its tokens")
	assert.Equal(t, 1, failedLines(from))

	from = output.Len()
	tokens, err = e.auth.LoginWithTokens(user.Email, "Wrong-Passw0rd!")
	assert.Zero(t, audit.RecordPassword(authAuditCtx("req-wf-no"), tokens, err))
	assert.ErrorIs(t, err, errLoginInvalidCredentials, "the refusal is unchanged")
	assert.Equal(t, 1, failedLines(from))

	from = output.Len()
	ssoTokens, err := f.signIn(t, oidcValid("subject-wf-0454", "wf-sso-0454@idp.example.test"))
	require.NoError(t, err)
	assert.NotEmpty(t, ssoTokens.AccessToken, "the single sign-on still returns its tokens")
	assert.Equal(t, 1, failedLines(from))

	from = output.Len()
	_, err = f.signIn(t, oidcTokenSpec{sub: "subject-wf-no-0454", email: "wf-no-0454@idp.example.test", groups: []string{"nobody"}})
	assert.ErrorIs(t, err, ErrSSOSignInFailed, "the refusal is unchanged")
	assert.Equal(t, 1, failedLines(from), "one audit_write_failed line")
	var refused map[string]interface{}
	for _, line := range authAuditLogLines(output, from) {
		if line["msg"] == "Single sign-on refused" {
			refused = line
		}
	}
	require.NotNil(t, refused)
	require.Contains(t, refused, "audit_id")
	assert.Equal(t, float64(0), refused["audit_id"], "no row, audit_id 0")
	assert.Equal(t, 3, failing.creates, "the two successes and the single sign-on refusal are plain inserts")
	assert.Equal(t, 1, failing.aggregates, "the refused login is an aggregated write (#553)")

	// #466: a refused API key's aggregated write fails the same way.
	from = output.Len()
	_, err = e.auth.ValidateAPIKey("gcrm_bogus0466")
	require.Error(t, err)
	assert.Zero(t, audit.RecordAPIKeyFailure(authAuditCtx("req-wf-ak"), err))
	assert.Equal(t, "invalid API key", err.Error(), "the refusal is unchanged")
	assert.Equal(t, 1, failedLines(from), "one audit_write_failed line")
	assert.Equal(t, 2, failing.aggregates)
	assert.Equal(t, 3, failing.creates, "no plain insert for an API key")
	assert.Zero(t, f.count(t, "auth_audit_log", ""))
}

func runAuthAuditPrune(t *testing.T, db *gorm.DB) {
	repo := repository.NewAuthAuditRepository(db)
	now := time.Now().UTC().Truncate(time.Millisecond)
	const days = 30
	retention := days * 24 * time.Hour
	insert := func(createdAt time.Time) uint {
		row := &models.AuthAuditLog{CreatedAt: createdAt, Method: AuthAuditMethodRefresh, Outcome: AuthAuditOutcomeSuccess}
		require.NoError(t, db.Create(row).Error)
		return row.ID
	}
	exists := func(id uint) bool {
		var n int64
		require.NoError(t, db.Model(&models.AuthAuditLog{}).Where("id = ?", id).Count(&n).Error)
		return n == 1
	}
	old := insert(now.Add(-retention - time.Minute))
	young := insert(now.Add(-retention + time.Minute))
	fresh := insert(now)
	pruned, err := PruneAuthAuditLog(context.Background(), repo, now, days)
	require.NoError(t, err)
	assert.Equal(t, int64(1), pruned)
	assert.False(t, exists(old), "a row older than the retention is deleted")
	assert.True(t, exists(young), "a row younger than the retention stays")
	assert.True(t, exists(fresh), "a new row stays")

	// Chunks: more rows than one statement deletes.
	for i := 0; i < authAuditPruneChunk+3; i++ {
		insert(now.Add(-retention - time.Hour))
	}
	pruned, err = PruneAuthAuditLog(context.Background(), repo, now, days)
	require.NoError(t, err)
	assert.Equal(t, int64(authAuditPruneChunk+3), pruned)
	assert.True(t, exists(young))
}

func TestAuthAuditPruneSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runAuthAuditPrune(t, db)
}

func TestAuthAuditPruneMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runAuthAuditPrune(t, db)
}

// TestAuthAuditClaimValues: the stored claim arrays are bounded.
func TestAuthAuditClaimValues(t *testing.T) {
	assert.Nil(t, authAuditClaimJSON(nil))
	assert.Equal(t, "[]", *authAuditClaimJSON([]string{}))
	many := make([]string, 150)
	for i := range many {
		many[i] = strings.Repeat("é", 200)
	}
	var decoded []string
	require.NoError(t, json.Unmarshal([]byte(*authAuditClaimJSON(many)), &decoded))
	assert.Len(t, decoded, authAuditMaxClaimValues)
	for _, value := range decoded {
		assert.LessOrEqual(t, len(value), authAuditClaimValueMaxBytes)
	}
	control := make([]string, 100)
	for i := range control {
		control[i] = strings.Repeat("\x01", 256)
	}
	assert.LessOrEqual(t, len(*authAuditClaimJSON(control)), authAuditClaimColumnMaxBytes, "the array fits a TEXT column")
}
