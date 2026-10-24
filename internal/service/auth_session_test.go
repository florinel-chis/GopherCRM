package service

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// mockMailer is a test double for mailer.Mailer that records the reset link.
type mockMailer struct {
	mock.Mock
}

func (m *mockMailer) SendPasswordReset(to, resetURL string) error {
	args := m.Called(to, resetURL)
	return args.Error(0)
}

func (m *mockMailer) Send(to, subject, body string) error {
	args := m.Called(to, subject, body)
	return args.Error(0)
}

type sessionTestDeps struct {
	userRepo    *mocks.UserRepository
	apiKeyRepo  *mocks.APIKeyRepository
	refreshRepo *mocks.RefreshTokenRepository
	resetRepo   *mocks.PasswordResetTokenRepository
	txManager   repository.TransactionManager
	mailer      *mockMailer
	svc         AuthService
}

const sessionTestSecret = "session-test-secret"

var sessionTestJWTConfig = config.JWTConfig{
	Secret:           sessionTestSecret,
	ExpiryHours:      1,
	RefreshTokenDays: 30,
}

// sessionTestTransactionManager is the real manager on an empty in-memory
// SQLite database: the mocks never touch it, but the reset flow needs a real
// transaction in its context to bind the repositories to.
func sessionTestTransactionManager(t *testing.T) repository.TransactionManager {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return utils.NewTransactionManager(db)
}

func newSessionAuthService(t *testing.T) *sessionTestDeps {
	t.Helper()
	d := &sessionTestDeps{
		userRepo:    &mocks.UserRepository{},
		apiKeyRepo:  &mocks.APIKeyRepository{},
		refreshRepo: &mocks.RefreshTokenRepository{},
		resetRepo:   &mocks.PasswordResetTokenRepository{},
		txManager:   sessionTestTransactionManager(t),
		mailer:      &mockMailer{},
	}
	d.svc = NewAuthServiceWithSessions(
		d.userRepo, d.apiKeyRepo, d.refreshRepo, d.resetRepo, d.txManager, d.mailer,
		sessionTestJWTConfig, "http://localhost:5173", "")
	return d
}

// expectTransactionBoundRepos lets the mocks stand in for their own
// transaction-bound views, which is what the reset flow asks for.
func (d *sessionTestDeps) expectTransactionBoundRepos() {
	d.resetRepo.On("WithTx", mock.Anything).Return(d.resetRepo)
	d.userRepo.On("WithTx", mock.Anything).Return(d.userRepo)
	d.refreshRepo.On("WithTx", mock.Anything).Return(d.refreshRepo)
}

func sessionActiveUser(id uint, password string) *models.User {
	hashed, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return &models.User{
		BaseModel: models.BaseModel{ID: id},
		Email:     "session@example.com",
		Password:  string(hashed),
		IsActive:  true,
		Role:      models.RoleCustomer,
	}
}

// The mock-based rotation cases pin the call sequence: lookup, conditional
// claim, owner read, successor insert, access token — all on the
// transaction-bound repositories. Whether the writes really share one
// transaction on SQL, and what concurrent callers see, is proven by the real
// database roots in auth_refresh_rotation_regression_test.go.
func TestAuthService_RefreshAccessToken_RotatesToken(t *testing.T) {
	d := newSessionAuthService(t)
	d.expectTransactionBoundRepos()
	user := sessionActiveUser(1, "OldPass123!")

	raw := "raw-refresh-token-value"
	hash := hashOpaqueToken(raw, sessionTestSecret)

	stored := &models.RefreshToken{
		BaseModel: models.BaseModel{ID: 5},
		UserID:    1,
		TokenHash: hash,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	d.refreshRepo.On("GetByTokenHash", hash).Return(stored, nil)
	d.refreshRepo.On("ClaimForRotation", uint(5)).Return(nil)
	d.userRepo.On("GetByID", uint(1)).Return(user, nil)

	var newStored *models.RefreshToken
	d.refreshRepo.On("Create", mock.MatchedBy(func(rt *models.RefreshToken) bool {
		newStored = rt
		return rt.UserID == 1 && rt.TokenHash != hash && rt.TokenHash != ""
	})).Return(nil)

	tokens, err := d.svc.RefreshAccessToken(raw)

	assert.NoError(t, err)
	assert.NotNil(t, tokens)
	assert.NotEmpty(t, tokens.AccessToken)
	assert.NotEmpty(t, tokens.RefreshToken)
	assert.NotEqual(t, raw, tokens.RefreshToken, "rotation must mint a new opaque token")
	assert.Equal(t, user, tokens.User)

	// The presented token is spent by the conditional claim, not by the
	// unconditional logout revocation, and every call went through the
	// transaction-bound repositories.
	d.refreshRepo.AssertCalled(t, "ClaimForRotation", uint(5))
	d.refreshRepo.AssertNotCalled(t, "RevokeByTokenHash", mock.Anything)
	d.refreshRepo.AssertCalled(t, "WithTx", mock.Anything)
	d.userRepo.AssertCalled(t, "WithTx", mock.Anything)

	// Only the hash of the new token is persisted, never the raw value.
	assert.NotNil(t, newStored)
	assert.Equal(t, hashOpaqueToken(tokens.RefreshToken, sessionTestSecret), newStored.TokenHash)
	assert.NotContains(t, newStored.TokenHash, tokens.RefreshToken)

	// Expiry honours REFRESH_TOKEN_EXPIRY_DAYS (30 days here).
	expected := time.Now().Add(30 * 24 * time.Hour)
	assert.WithinDuration(t, expected, newStored.ExpiresAt, time.Minute)
}

func TestAuthService_RefreshAccessToken_UnknownRevokedOrExpired(t *testing.T) {
	// The repository lookup already filters revoked and expired rows, so all
	// three cases surface identically as a record-not-found.
	d := newSessionAuthService(t)
	d.expectTransactionBoundRepos()
	hash := hashOpaqueToken("dead-token", sessionTestSecret)
	d.refreshRepo.On("GetByTokenHash", hash).Return(nil, gorm.ErrRecordNotFound)

	tokens, err := d.svc.RefreshAccessToken("dead-token")

	assert.Nil(t, tokens)
	assert.ErrorIs(t, err, ErrInvalidRefreshToken)
	d.refreshRepo.AssertNotCalled(t, "ClaimForRotation", mock.Anything)
	d.refreshRepo.AssertNotCalled(t, "Create", mock.Anything)
}

func TestAuthService_RefreshAccessToken_LostClaimIsRejectedBeforeAnySuccessor(t *testing.T) {
	// Both requests read the token as live; this one lost the claim. It must
	// be rejected generically without reading the owner or minting anything.
	d := newSessionAuthService(t)
	d.expectTransactionBoundRepos()
	raw := "contended-token"
	hash := hashOpaqueToken(raw, sessionTestSecret)
	stored := &models.RefreshToken{
		BaseModel: models.BaseModel{ID: 11},
		UserID:    1,
		TokenHash: hash,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	d.refreshRepo.On("GetByTokenHash", hash).Return(stored, nil)
	d.refreshRepo.On("ClaimForRotation", uint(11)).Return(gorm.ErrRecordNotFound)

	tokens, err := d.svc.RefreshAccessToken(raw)

	assert.Nil(t, tokens)
	assert.ErrorIs(t, err, ErrInvalidRefreshToken)
	d.userRepo.AssertNotCalled(t, "GetByID", mock.Anything)
	d.refreshRepo.AssertNotCalled(t, "Create", mock.Anything)
	d.refreshRepo.AssertNotCalled(t, "RevokeByTokenHash", mock.Anything)
}

func TestAuthService_RefreshAccessToken_InactiveOwnerRejectedAndRevoked(t *testing.T) {
	inactive := sessionActiveUser(2, "OldPass123!")
	inactive.IsActive = false
	for name, owner := range map[string]struct {
		user *models.User
		err  error
	}{
		"inactive": {user: inactive},
		"missing":  {err: gorm.ErrRecordNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			d := newSessionAuthService(t)
			d.expectTransactionBoundRepos()
			raw := "orphan-token"
			hash := hashOpaqueToken(raw, sessionTestSecret)
			stored := &models.RefreshToken{
				BaseModel: models.BaseModel{ID: 7},
				UserID:    2,
				TokenHash: hash,
				ExpiresAt: time.Now().Add(24 * time.Hour),
			}

			d.refreshRepo.On("GetByTokenHash", hash).Return(stored, nil)
			d.refreshRepo.On("ClaimForRotation", uint(7)).Return(nil)
			d.userRepo.On("GetByID", uint(2)).Return(owner.user, owner.err)

			tokens, err := d.svc.RefreshAccessToken(raw)

			assert.Nil(t, tokens)
			assert.ErrorIs(t, err, ErrInvalidRefreshToken)
			// The claim is the containment revocation of the dead account's
			// token; no successor is ever created for it.
			d.refreshRepo.AssertCalled(t, "ClaimForRotation", uint(7))
			d.refreshRepo.AssertNotCalled(t, "Create", mock.Anything)
		})
	}
}

func TestAuthService_RefreshAccessToken_FailuresAfterTheClaimReturnNoCredentials(t *testing.T) {
	raw := "fragile-token"
	hash := hashOpaqueToken(raw, sessionTestSecret)
	stored := func() *models.RefreshToken {
		return &models.RefreshToken{
			BaseModel: models.BaseModel{ID: 13},
			UserID:    1,
			TokenHash: hash,
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}
	}
	injected := errors.New("injected failure")

	t.Run("the successor insert fails", func(t *testing.T) {
		d := newSessionAuthService(t)
		d.expectTransactionBoundRepos()
		d.refreshRepo.On("GetByTokenHash", hash).Return(stored(), nil)
		d.refreshRepo.On("ClaimForRotation", uint(13)).Return(nil)
		d.userRepo.On("GetByID", uint(1)).Return(sessionActiveUser(1, "OldPass123!"), nil)
		d.refreshRepo.On("Create", mock.Anything).Return(injected)

		tokens, err := d.svc.RefreshAccessToken(raw)

		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, injected, "the failure is returned as it is")
		assert.NotErrorIs(t, err, ErrInvalidRefreshToken)
	})

	t.Run("the access token cannot be signed", func(t *testing.T) {
		d := newSessionAuthService(t)
		d.expectTransactionBoundRepos()
		d.svc.(*authService).signAccessToken = func(*models.User) (string, error) { return "", injected }
		d.refreshRepo.On("GetByTokenHash", hash).Return(stored(), nil)
		d.refreshRepo.On("ClaimForRotation", uint(13)).Return(nil)
		d.userRepo.On("GetByID", uint(1)).Return(sessionActiveUser(1, "OldPass123!"), nil)
		d.refreshRepo.On("Create", mock.Anything).Return(nil)

		tokens, err := d.svc.RefreshAccessToken(raw)

		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, injected)
		assert.NotErrorIs(t, err, ErrInvalidRefreshToken)
	})

	t.Run("without a transaction manager the rotation is refused rather than run unsafely", func(t *testing.T) {
		d := newSessionAuthService(t)
		d.svc = NewAuthServiceWithSessions(
			d.userRepo, d.apiKeyRepo, d.refreshRepo, d.resetRepo, nil, d.mailer,
			sessionTestJWTConfig, "http://localhost:5173", "")
		d.refreshRepo.On("GetByTokenHash", hash).Return(stored(), nil)

		tokens, err := d.svc.RefreshAccessToken(raw)

		assert.Nil(t, tokens)
		assert.ErrorIs(t, err, ErrInvalidRefreshToken)
		d.refreshRepo.AssertNotCalled(t, "GetByTokenHash", mock.Anything)
		d.refreshRepo.AssertNotCalled(t, "ClaimForRotation", mock.Anything)
		d.refreshRepo.AssertNotCalled(t, "RevokeByTokenHash", mock.Anything)
		d.refreshRepo.AssertNotCalled(t, "Create", mock.Anything)
	})
}

func TestAuthService_LoginWithTokens_IssuesRefreshToken(t *testing.T) {
	d := newSessionAuthService(t)
	user := sessionActiveUser(1, "Password123!")

	d.userRepo.On("GetByEmail", user.Email).Return(user, nil)
	d.userRepo.On("UpdateLastLogin", uint(1)).Return(nil)

	var stored *models.RefreshToken
	d.refreshRepo.On("Create", mock.MatchedBy(func(rt *models.RefreshToken) bool {
		stored = rt
		return rt.UserID == 1
	})).Return(nil)

	tokens, err := d.svc.LoginWithTokens(user.Email, "Password123!")

	assert.NoError(t, err)
	assert.NotNil(t, tokens)
	assert.NotEmpty(t, tokens.AccessToken)
	assert.NotEmpty(t, tokens.RefreshToken)
	assert.Equal(t, user, tokens.User)
	assert.NotNil(t, stored)
	assert.Equal(t, hashOpaqueToken(tokens.RefreshToken, sessionTestSecret), stored.TokenHash)
}

func TestAuthService_Logout(t *testing.T) {
	t.Run("no token revokes all sessions of the user", func(t *testing.T) {
		d := newSessionAuthService(t)
		d.refreshRepo.On("RevokeAllForUser", uint(42)).Return(nil)

		err := d.svc.Logout(42, "")

		assert.NoError(t, err)
		d.refreshRepo.AssertCalled(t, "RevokeAllForUser", uint(42))
	})

	t.Run("specific token owned by the user is revoked", func(t *testing.T) {
		d := newSessionAuthService(t)
		raw := "owned-token"
		hash := hashOpaqueToken(raw, sessionTestSecret)
		stored := &models.RefreshToken{UserID: 42, TokenHash: hash, ExpiresAt: time.Now().Add(time.Hour)}
		d.refreshRepo.On("GetByTokenHash", hash).Return(stored, nil)
		d.refreshRepo.On("RevokeByTokenHash", hash).Return(nil)

		err := d.svc.Logout(42, raw)

		assert.NoError(t, err)
		d.refreshRepo.AssertCalled(t, "RevokeByTokenHash", hash)
	})

	t.Run("token of another user is not revoked but logout still succeeds", func(t *testing.T) {
		d := newSessionAuthService(t)
		raw := "someone-elses-token"
		hash := hashOpaqueToken(raw, sessionTestSecret)
		stored := &models.RefreshToken{UserID: 7, TokenHash: hash, ExpiresAt: time.Now().Add(time.Hour)}
		d.refreshRepo.On("GetByTokenHash", hash).Return(stored, nil)

		err := d.svc.Logout(42, raw)

		assert.NoError(t, err)
		d.refreshRepo.AssertNotCalled(t, "RevokeByTokenHash", mock.Anything)
	})

	t.Run("unknown token is idempotent success", func(t *testing.T) {
		d := newSessionAuthService(t)
		hash := hashOpaqueToken("gone", sessionTestSecret)
		d.refreshRepo.On("GetByTokenHash", hash).Return(nil, gorm.ErrRecordNotFound)

		err := d.svc.Logout(42, "gone")

		assert.NoError(t, err)
	})
}

func TestAuthService_ChangePassword(t *testing.T) {
	t.Run("wrong current password is rejected without touching the account", func(t *testing.T) {
		d := newSessionAuthService(t)
		user := sessionActiveUser(1, "CurrentPass1!")
		d.userRepo.On("GetByID", uint(1)).Return(user, nil)

		err := d.svc.ChangePassword(1, "WrongPass1!", "NewSecret123!")

		assert.ErrorIs(t, err, ErrInvalidCurrentPassword)
		d.userRepo.AssertNotCalled(t, "Update", mock.Anything)
		d.refreshRepo.AssertNotCalled(t, "RevokeAllForUser", mock.Anything)
	})

	t.Run("weak new password is rejected", func(t *testing.T) {
		d := newSessionAuthService(t)
		user := sessionActiveUser(1, "CurrentPass1!")
		d.userRepo.On("GetByID", uint(1)).Return(user, nil)

		err := d.svc.ChangePassword(1, "CurrentPass1!", "weakpassword")

		assert.Error(t, err)
		d.userRepo.AssertNotCalled(t, "Update", mock.Anything)
	})

	t.Run("success updates the hash and revokes every refresh token", func(t *testing.T) {
		d := newSessionAuthService(t)
		user := sessionActiveUser(1, "CurrentPass1!")
		oldHash := user.Password
		d.userRepo.On("GetByID", uint(1)).Return(user, nil)

		var updated *models.User
		d.userRepo.On("Update", mock.MatchedBy(func(u *models.User) bool {
			updated = u
			return u.ID == 1
		})).Return(nil)
		d.refreshRepo.On("RevokeAllForUser", uint(1)).Return(nil)

		err := d.svc.ChangePassword(1, "CurrentPass1!", "NewSecret123!")

		assert.NoError(t, err)
		assert.NotNil(t, updated)
		assert.NotEqual(t, oldHash, updated.Password)
		assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte("NewSecret123!")))
		d.refreshRepo.AssertCalled(t, "RevokeAllForUser", uint(1))
	})
}

func TestAuthService_RequestPasswordReset(t *testing.T) {
	t.Run("existing active account stores a hashed token and mails the link", func(t *testing.T) {
		d := newSessionAuthService(t)
		user := sessionActiveUser(1, "CurrentPass1!")
		d.userRepo.On("GetByEmail", user.Email).Return(user, nil)
		d.resetRepo.On("InvalidateAllForUser", uint(1)).Return(nil)

		var stored *models.PasswordResetToken
		d.resetRepo.On("Create", mock.MatchedBy(func(prt *models.PasswordResetToken) bool {
			stored = prt
			return prt.UserID == 1 && prt.TokenHash != ""
		})).Return(nil)

		var sentURL string
		d.mailer.On("SendPasswordReset", user.Email, mock.MatchedBy(func(u string) bool {
			sentURL = u
			return strings.HasPrefix(u, "http://localhost:5173/reset-password?token=")
		})).Return(nil)

		err := d.svc.RequestPasswordReset(user.Email)

		assert.NoError(t, err)
		assert.NotNil(t, stored)

		// The link carries the raw token; the store holds only its hash.
		parsed, perr := url.Parse(sentURL)
		assert.NoError(t, perr)
		rawToken := parsed.Query().Get("token")
		assert.NotEmpty(t, rawToken)
		assert.Equal(t, hashOpaqueToken(rawToken, sessionTestSecret), stored.TokenHash)
		assert.NotEqual(t, rawToken, stored.TokenHash)

		// Fixed one-hour expiry.
		assert.WithinDuration(t, time.Now().Add(time.Hour), stored.ExpiresAt, time.Minute)
	})

	t.Run("unknown email succeeds silently without mail", func(t *testing.T) {
		d := newSessionAuthService(t)
		d.userRepo.On("GetByEmail", "ghost@example.com").Return(nil, gorm.ErrRecordNotFound)

		err := d.svc.RequestPasswordReset("ghost@example.com")

		assert.NoError(t, err)
		d.resetRepo.AssertNotCalled(t, "Create", mock.Anything)
		d.mailer.AssertNotCalled(t, "SendPasswordReset", mock.Anything, mock.Anything)
	})

	t.Run("inactive account succeeds silently without mail", func(t *testing.T) {
		d := newSessionAuthService(t)
		user := sessionActiveUser(1, "CurrentPass1!")
		user.IsActive = false
		d.userRepo.On("GetByEmail", user.Email).Return(user, nil)

		err := d.svc.RequestPasswordReset(user.Email)

		assert.NoError(t, err)
		d.resetRepo.AssertNotCalled(t, "Create", mock.Anything)
		d.mailer.AssertNotCalled(t, "SendPasswordReset", mock.Anything, mock.Anything)
	})

	t.Run("mailer failure is swallowed to keep responses uniform", func(t *testing.T) {
		d := newSessionAuthService(t)
		user := sessionActiveUser(1, "CurrentPass1!")
		d.userRepo.On("GetByEmail", user.Email).Return(user, nil)
		d.resetRepo.On("InvalidateAllForUser", uint(1)).Return(nil)
		d.resetRepo.On("Create", mock.Anything).Return(nil)
		d.mailer.On("SendPasswordReset", user.Email, mock.Anything).Return(errors.New("smtp down"))

		err := d.svc.RequestPasswordReset(user.Email)

		assert.NoError(t, err)
	})
}

// The mock-based cases below pin the call sequence of the reset flow; whether
// the writes really share one transaction on SQL is proven by the real
// database roots in auth_password_reset_regression_test.go.
func TestAuthService_ConfirmPasswordReset(t *testing.T) {
	const raw = "reset-token-raw"
	hash := hashOpaqueToken(raw, sessionTestSecret)
	spendable := func() *models.PasswordResetToken {
		return &models.PasswordResetToken{
			BaseModel: models.BaseModel{ID: 9},
			UserID:    1,
			TokenHash: hash,
			ExpiresAt: time.Now().Add(30 * time.Minute),
		}
	}

	t.Run("valid token claims the token, sets the password, clears the lockout and revokes sessions", func(t *testing.T) {
		d := newSessionAuthService(t)
		d.expectTransactionBoundRepos()
		user := sessionActiveUser(1, "CurrentPass1!")
		lockedUntil := time.Now().Add(10 * time.Minute)
		user.FailedLoginAttempts = MaxFailedLoginAttempts
		user.LockedUntil = &lockedUntil
		d.resetRepo.On("GetByTokenHash", hash).Return(spendable(), nil)
		d.resetRepo.On("MarkUsed", uint(9)).Return(nil)
		d.userRepo.On("GetByID", uint(1)).Return(user, nil)

		var updated *models.User
		d.userRepo.On("Update", mock.MatchedBy(func(u *models.User) bool {
			updated = u
			return u.ID == 1
		})).Return(nil)
		d.refreshRepo.On("RevokeAllForUser", uint(1)).Return(nil)

		err := d.svc.ConfirmPasswordReset(raw, "BrandNewPass1!")

		assert.NoError(t, err)
		d.resetRepo.AssertCalled(t, "MarkUsed", uint(9))
		require.NotNil(t, updated)
		assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte("BrandNewPass1!")))
		assert.Zero(t, updated.FailedLoginAttempts)
		assert.Nil(t, updated.LockedUntil)
		d.refreshRepo.AssertCalled(t, "RevokeAllForUser", uint(1))
		// Every write went through the transaction-bound repositories.
		d.resetRepo.AssertCalled(t, "WithTx", mock.Anything)
		d.userRepo.AssertCalled(t, "WithTx", mock.Anything)
		d.refreshRepo.AssertCalled(t, "WithTx", mock.Anything)
	})

	t.Run("unknown, expired or used token is rejected generically", func(t *testing.T) {
		// Repository lookup filters used and expired tokens, so all cases
		// surface as record-not-found here.
		d := newSessionAuthService(t)
		d.expectTransactionBoundRepos()
		spentHash := hashOpaqueToken("spent", sessionTestSecret)
		d.resetRepo.On("GetByTokenHash", spentHash).Return(nil, gorm.ErrRecordNotFound)

		err := d.svc.ConfirmPasswordReset("spent", "BrandNewPass1!")

		assert.ErrorIs(t, err, ErrInvalidResetToken)
		d.resetRepo.AssertNotCalled(t, "MarkUsed", mock.Anything)
		d.userRepo.AssertNotCalled(t, "Update", mock.Anything)
		d.refreshRepo.AssertNotCalled(t, "RevokeAllForUser", mock.Anything)
	})

	t.Run("a claim that finds nothing to stamp is rejected generically", func(t *testing.T) {
		// Both requests read the token as spendable; this one lost the claim.
		d := newSessionAuthService(t)
		d.expectTransactionBoundRepos()
		d.resetRepo.On("GetByTokenHash", hash).Return(spendable(), nil)
		d.resetRepo.On("MarkUsed", uint(9)).Return(gorm.ErrRecordNotFound)

		err := d.svc.ConfirmPasswordReset(raw, "BrandNewPass1!")

		assert.ErrorIs(t, err, ErrInvalidResetToken)
		d.userRepo.AssertNotCalled(t, "GetByID", mock.Anything)
		d.userRepo.AssertNotCalled(t, "Update", mock.Anything)
		d.refreshRepo.AssertNotCalled(t, "RevokeAllForUser", mock.Anything)
	})

	t.Run("weak password is refused before the token is even looked up", func(t *testing.T) {
		d := newSessionAuthService(t)
		d.expectTransactionBoundRepos()
		d.resetRepo.On("GetByTokenHash", hash).Return(spendable(), nil)

		err := d.svc.ConfirmPasswordReset(raw, "weak")

		assert.Error(t, err)
		assert.NotErrorIs(t, err, ErrInvalidResetToken)
		d.resetRepo.AssertNotCalled(t, "GetByTokenHash", mock.Anything)
		d.resetRepo.AssertNotCalled(t, "MarkUsed", mock.Anything)
		d.userRepo.AssertNotCalled(t, "Update", mock.Anything)
	})

	t.Run("a password bcrypt cannot hash is refused without spending the token", func(t *testing.T) {
		d := newSessionAuthService(t)
		d.expectTransactionBoundRepos()
		d.resetRepo.On("GetByTokenHash", hash).Return(spendable(), nil)
		// Complex by the policy, but over bcrypt's 72-byte limit.
		tooLong := "Aa1!" + strings.Repeat("x", 80)
		require.NoError(t, utils.ValidatePasswordComplexity(tooLong))

		err := d.svc.ConfirmPasswordReset(raw, tooLong)

		assert.ErrorIs(t, err, bcrypt.ErrPasswordTooLong)
		assert.NotErrorIs(t, err, ErrInvalidResetToken)
		d.resetRepo.AssertNotCalled(t, "MarkUsed", mock.Anything)
		d.userRepo.AssertNotCalled(t, "Update", mock.Anything)
	})

	t.Run("inactive or missing owner is rejected generically after the claim", func(t *testing.T) {
		inactive := sessionActiveUser(1, "CurrentPass1!")
		inactive.IsActive = false
		for name, owner := range map[string]struct {
			user *models.User
			err  error
		}{
			"inactive": {user: inactive},
			"missing":  {err: gorm.ErrRecordNotFound},
		} {
			t.Run(name, func(t *testing.T) {
				d := newSessionAuthService(t)
				d.expectTransactionBoundRepos()
				d.resetRepo.On("GetByTokenHash", hash).Return(spendable(), nil)
				d.resetRepo.On("MarkUsed", uint(9)).Return(nil)
				d.userRepo.On("GetByID", uint(1)).Return(owner.user, owner.err)

				err := d.svc.ConfirmPasswordReset(raw, "BrandNewPass1!")

				assert.ErrorIs(t, err, ErrInvalidResetToken)
				d.userRepo.AssertNotCalled(t, "Update", mock.Anything)
				d.refreshRepo.AssertNotCalled(t, "RevokeAllForUser", mock.Anything)
			})
		}
	})

	t.Run("a session revocation failure fails the reset", func(t *testing.T) {
		d := newSessionAuthService(t)
		d.expectTransactionBoundRepos()
		injected := errors.New("refresh token table unavailable")
		d.resetRepo.On("GetByTokenHash", hash).Return(spendable(), nil)
		d.resetRepo.On("MarkUsed", uint(9)).Return(nil)
		d.userRepo.On("GetByID", uint(1)).Return(sessionActiveUser(1, "CurrentPass1!"), nil)
		d.userRepo.On("Update", mock.Anything).Return(nil)
		d.refreshRepo.On("RevokeAllForUser", uint(1)).Return(injected)

		err := d.svc.ConfirmPasswordReset(raw, "BrandNewPass1!")

		assert.ErrorIs(t, err, injected, "the failure is returned, not logged away")
		assert.NotErrorIs(t, err, ErrInvalidResetToken)
	})

	t.Run("without a transaction manager the reset is refused rather than run unsafely", func(t *testing.T) {
		d := newSessionAuthService(t)
		d.svc = NewAuthServiceWithSessions(
			d.userRepo, d.apiKeyRepo, d.refreshRepo, d.resetRepo, nil, d.mailer,
			sessionTestJWTConfig, "http://localhost:5173", "")
		d.resetRepo.On("GetByTokenHash", hash).Return(spendable(), nil)

		err := d.svc.ConfirmPasswordReset(raw, "BrandNewPass1!")

		assert.ErrorIs(t, err, ErrInvalidResetToken)
		d.resetRepo.AssertNotCalled(t, "GetByTokenHash", mock.Anything)
		d.resetRepo.AssertNotCalled(t, "MarkUsed", mock.Anything)
		d.userRepo.AssertNotCalled(t, "Update", mock.Anything)
	})
}
