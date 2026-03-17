package service

import (
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"golang.org/x/crypto/bcrypt"
)



func TestAuthService_LoginWithTokens(t *testing.T) {
	// Setup
	mockUserRepo := &mocks.UserRepository{}
	mockAPIKeyRepo := &mocks.APIKeyRepository{}
	mockRefreshTokenRepo := &mocks.RefreshTokenRepository{}
	
	jwtConfig := config.JWTConfig{
		Secret:             "test-secret",
		AccessTokenMinutes: 15,
		RefreshTokenDays:   7,
	}
	
	csrfConfig := config.CSRFConfig{
		Secret:  "csrf-secret",
		Enabled: true,
	}

	_ = mockRefreshTokenRepo // reserved for future use
	_ = csrfConfig           // reserved for future use
	authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

	// Test data
	hashedPassword, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	user := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "test@example.com",
		Password:  string(hashedPassword),
		IsActive:  true,
		Role:      models.RoleCustomer,
	}

	t.Run("successful login with tokens", func(t *testing.T) {
		// Setup mocks
		mockUserRepo.On("GetByEmail", "test@example.com").Return(user, nil)
		mockUserRepo.On("UpdateLastLogin", uint(1)).Return(nil)
		mockRefreshTokenRepo.On("Create", mock.AnythingOfType("*models.RefreshToken")).Return(nil)

		// Execute
		tokens, err := authService.LoginWithTokens("test@example.com", "password123")

		// Verify
		assert.NoError(t, err)
		assert.NotNil(t, tokens)
		assert.NotEmpty(t, tokens.AccessToken)
		assert.NotEmpty(t, tokens.RefreshToken)

		mockUserRepo.AssertExpectations(t)
		mockRefreshTokenRepo.AssertExpectations(t)
	})

	t.Run("login fails with invalid password", func(t *testing.T) {
		// Setup mocks
		mockUserRepo.On("GetByEmail", "test@example.com").Return(user, nil)

		// Execute
		tokens, err := authService.LoginWithTokens("test@example.com", "wrongpassword")

		// Verify
		assert.Error(t, err)
		assert.Nil(t, tokens)
		assert.Equal(t, "invalid credentials", err.Error())
	})

	t.Run("login fails with inactive user", func(t *testing.T) {
		inactiveUser := *user
		inactiveUser.IsActive = false

		// Setup mocks
		mockUserRepo.On("GetByEmail", "test@example.com").Return(&inactiveUser, nil)

		// Execute
		tokens, err := authService.LoginWithTokens("test@example.com", "password123")

		// Verify
		assert.Error(t, err)
		assert.Nil(t, tokens)
		assert.Equal(t, "account is disabled", err.Error())
	})
}

func TestAuthService_RefreshAccessToken(t *testing.T) {
	// Setup
	mockUserRepo := &mocks.UserRepository{}
	mockAPIKeyRepo := &mocks.APIKeyRepository{}
	mockRefreshTokenRepo := &mocks.RefreshTokenRepository{}
	
	jwtConfig := config.JWTConfig{
		Secret:             "test-secret",
		AccessTokenMinutes: 15,
		RefreshTokenDays:   7,
	}
	
	csrfConfig := config.CSRFConfig{
		Secret:  "csrf-secret",
		Enabled: true,
	}

	_ = mockRefreshTokenRepo // reserved for future use
	_ = csrfConfig           // reserved for future use
	authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

	// Test data
	user := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "test@example.com",
		IsActive:  true,
		Role:      models.RoleCustomer,
	}

	refreshToken := "test-refresh-token"
	tokenHash := utils.HashToken(refreshToken)
	storedToken := &models.RefreshToken{
		BaseModel: models.BaseModel{ID: 1},
		UserID:    1,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Revoked: false,
	}

	t.Run("successful token refresh", func(t *testing.T) {
		// Setup mocks
		mockRefreshTokenRepo.On("GetByTokenHash", tokenHash).Return(storedToken, nil)
		mockUserRepo.On("GetByID", uint(1)).Return(user, nil)
		mockRefreshTokenRepo.On("Create", mock.AnythingOfType("*models.RefreshToken")).Return(nil)
		mockRefreshTokenRepo.On("RevokeByTokenHash", tokenHash).Return(nil)

		// Execute
		tokens, err := authService.RefreshAccessToken(refreshToken)

		// Verify
		assert.NoError(t, err)
		assert.NotNil(t, tokens)
		assert.NotEmpty(t, tokens.AccessToken)
		assert.NotEmpty(t, tokens.RefreshToken)

		mockRefreshTokenRepo.AssertExpectations(t)
		mockUserRepo.AssertExpectations(t)
	})

	t.Run("refresh fails with invalid token", func(t *testing.T) {
		invalidTokenHash := utils.HashToken("invalid-token")

		// Setup mocks
		mockRefreshTokenRepo.On("GetByTokenHash", invalidTokenHash).Return((*models.RefreshToken)(nil), assert.AnError)

		// Execute
		tokens, err := authService.RefreshAccessToken("invalid-token")

		// Verify
		assert.Error(t, err)
		assert.Nil(t, tokens)
		assert.Equal(t, "invalid refresh token", err.Error())
	})

	t.Run("refresh fails with inactive user", func(t *testing.T) {
		inactiveUser := *user
		inactiveUser.IsActive = false

		// Setup mocks
		mockRefreshTokenRepo.On("GetByTokenHash", tokenHash).Return(storedToken, nil)
		mockUserRepo.On("GetByID", uint(1)).Return(&inactiveUser, nil)

		// Execute
		tokens, err := authService.RefreshAccessToken(refreshToken)

		// Verify
		assert.Error(t, err)
		assert.Nil(t, tokens)
		assert.Equal(t, "account is disabled", err.Error())
	})
}

func TestAuthService_GenerateCSRFToken(t *testing.T) {
	// Setup
	mockUserRepo := &mocks.UserRepository{}
	mockAPIKeyRepo := &mocks.APIKeyRepository{}
	mockRefreshTokenRepo := &mocks.RefreshTokenRepository{}
	
	jwtConfig := config.JWTConfig{Secret: "test-secret"}
	csrfConfig := config.CSRFConfig{Secret: "csrf-secret", Enabled: true}

	_ = mockRefreshTokenRepo // reserved for future use
	_ = csrfConfig           // reserved for future use
	authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

	t.Run("generates valid CSRF token", func(t *testing.T) {
		// Execute
		token, err := authService.GenerateCSRFToken()

		// Verify
		assert.NoError(t, err)
		assert.NotEmpty(t, token)
		assert.True(t, len(token) > 0)
	})
}

func TestAuthService_ValidateCSRFToken(t *testing.T) {
	// Setup
	mockUserRepo := &mocks.UserRepository{}
	mockAPIKeyRepo := &mocks.APIKeyRepository{}
	mockRefreshTokenRepo := &mocks.RefreshTokenRepository{}
	
	jwtConfig := config.JWTConfig{Secret: "test-secret"}
	csrfConfig := config.CSRFConfig{Secret: "csrf-secret", Enabled: true}

	_ = mockRefreshTokenRepo // reserved for future use
	_ = csrfConfig           // reserved for future use
	authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

	t.Run("validates non-empty token", func(t *testing.T) {
		// Execute
		valid := authService.ValidateCSRFToken("valid-token")

		// Verify
		assert.True(t, valid)
	})

	t.Run("rejects empty token", func(t *testing.T) {
		// Execute
		valid := authService.ValidateCSRFToken("")

		// Verify
		assert.False(t, valid)
	})
}

func TestAuthService_InvalidateRefreshToken(t *testing.T) {
	// Setup
	mockUserRepo := &mocks.UserRepository{}
	mockAPIKeyRepo := &mocks.APIKeyRepository{}
	mockRefreshTokenRepo := &mocks.RefreshTokenRepository{}
	
	jwtConfig := config.JWTConfig{Secret: "test-secret"}
	csrfConfig := config.CSRFConfig{Secret: "csrf-secret", Enabled: true}

	_ = mockRefreshTokenRepo // reserved for future use
	_ = csrfConfig           // reserved for future use
	authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

	t.Run("successfully invalidates refresh token", func(t *testing.T) {
		refreshToken := "test-refresh-token"
		tokenHash := utils.HashToken(refreshToken)

		// Setup mocks
		mockRefreshTokenRepo.On("RevokeByTokenHash", tokenHash).Return(nil)

		// Execute
		err := authService.InvalidateRefreshToken(refreshToken)

		// Verify
		assert.NoError(t, err)
		mockRefreshTokenRepo.AssertExpectations(t)
	})
}