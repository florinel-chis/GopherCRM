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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
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
	mailer      *mockMailer
	svc         AuthService
}

const sessionTestSecret = "session-test-secret"

func newSessionAuthService(t *testing.T) *sessionTestDeps {
	t.Helper()
	d := &sessionTestDeps{
		userRepo:    &mocks.UserRepository{},
		apiKeyRepo:  &mocks.APIKeyRepository{},
		refreshRepo: &mocks.RefreshTokenRepository{},
		resetRepo:   &mocks.PasswordResetTokenRepository{},
		mailer:      &mockMailer{},
	}
	jwtConfig := config.JWTConfig{
		Secret:           sessionTestSecret,
		ExpiryHours:      1,
		RefreshTokenDays: 30,
	}
	d.svc = NewAuthServiceWithSessions(
		d.userRepo, d.apiKeyRepo, d.refreshRepo, d.resetRepo, d.mailer,
		jwtConfig, "http://localhost:5173", "")
	return d
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

func TestAuthService_RefreshAccessToken_RotatesToken(t *testing.T) {
	d := newSessionAuthService(t)
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
	d.userRepo.On("GetByID", uint(1)).Return(user, nil)
	d.refreshRepo.On("RevokeByTokenHash", hash).Return(nil)

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

	// The presented token must be revoked as part of the rotation.
	d.refreshRepo.AssertCalled(t, "RevokeByTokenHash", hash)

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
	hash := hashOpaqueToken("dead-token", sessionTestSecret)
	d.refreshRepo.On("GetByTokenHash", hash).Return(nil, gorm.ErrRecordNotFound)

	tokens, err := d.svc.RefreshAccessToken("dead-token")

	assert.Nil(t, tokens)
	assert.ErrorIs(t, err, ErrInvalidRefreshToken)
	d.refreshRepo.AssertNotCalled(t, "Create", mock.Anything)
}

func TestAuthService_RefreshAccessToken_InactiveOwnerRejectedAndRevoked(t *testing.T) {
	d := newSessionAuthService(t)
	raw := "orphan-token"
	hash := hashOpaqueToken(raw, sessionTestSecret)
	stored := &models.RefreshToken{
		BaseModel: models.BaseModel{ID: 7},
		UserID:    2,
		TokenHash: hash,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	inactive := sessionActiveUser(2, "OldPass123!")
	inactive.IsActive = false

	d.refreshRepo.On("GetByTokenHash", hash).Return(stored, nil)
	d.userRepo.On("GetByID", uint(2)).Return(inactive, nil)
	d.refreshRepo.On("RevokeByTokenHash", hash).Return(nil)

	tokens, err := d.svc.RefreshAccessToken(raw)

	assert.Nil(t, tokens)
	assert.ErrorIs(t, err, ErrInvalidRefreshToken)
	d.refreshRepo.AssertCalled(t, "RevokeByTokenHash", hash)
	d.refreshRepo.AssertNotCalled(t, "Create", mock.Anything)
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

func TestAuthService_ConfirmPasswordReset(t *testing.T) {
	t.Run("valid token sets the password, marks the token used and revokes sessions", func(t *testing.T) {
		d := newSessionAuthService(t)
		user := sessionActiveUser(1, "CurrentPass1!")
		raw := "reset-token-raw"
		hash := hashOpaqueToken(raw, sessionTestSecret)
		prt := &models.PasswordResetToken{
			BaseModel: models.BaseModel{ID: 9},
			UserID:    1,
			TokenHash: hash,
			ExpiresAt: time.Now().Add(30 * time.Minute),
		}
		d.resetRepo.On("GetByTokenHash", hash).Return(prt, nil)
		d.userRepo.On("GetByID", uint(1)).Return(user, nil)
		d.resetRepo.On("MarkUsed", uint(9)).Return(nil)

		var updated *models.User
		d.userRepo.On("Update", mock.MatchedBy(func(u *models.User) bool {
			updated = u
			return u.ID == 1
		})).Return(nil)
		d.refreshRepo.On("RevokeAllForUser", uint(1)).Return(nil)

		err := d.svc.ConfirmPasswordReset(raw, "BrandNewPass1!")

		assert.NoError(t, err)
		d.resetRepo.AssertCalled(t, "MarkUsed", uint(9))
		assert.NotNil(t, updated)
		assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte("BrandNewPass1!")))
		d.refreshRepo.AssertCalled(t, "RevokeAllForUser", uint(1))
	})

	t.Run("unknown, expired or used token is rejected generically", func(t *testing.T) {
		// Repository lookup filters used and expired tokens, so all cases
		// surface as record-not-found here.
		d := newSessionAuthService(t)
		hash := hashOpaqueToken("spent", sessionTestSecret)
		d.resetRepo.On("GetByTokenHash", hash).Return(nil, gorm.ErrRecordNotFound)

		err := d.svc.ConfirmPasswordReset("spent", "BrandNewPass1!")

		assert.ErrorIs(t, err, ErrInvalidResetToken)
		d.userRepo.AssertNotCalled(t, "Update", mock.Anything)
	})

	t.Run("weak password leaves the token spendable", func(t *testing.T) {
		d := newSessionAuthService(t)
		raw := "reset-token-raw"
		hash := hashOpaqueToken(raw, sessionTestSecret)
		prt := &models.PasswordResetToken{
			BaseModel: models.BaseModel{ID: 9},
			UserID:    1,
			TokenHash: hash,
			ExpiresAt: time.Now().Add(30 * time.Minute),
		}
		d.resetRepo.On("GetByTokenHash", hash).Return(prt, nil)

		err := d.svc.ConfirmPasswordReset(raw, "weak")

		assert.Error(t, err)
		d.resetRepo.AssertNotCalled(t, "MarkUsed", mock.Anything)
		d.userRepo.AssertNotCalled(t, "Update", mock.Anything)
	})
}
