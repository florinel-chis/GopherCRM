package service

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"golang.org/x/crypto/bcrypt"
)

func TestAuthService_LoginWithTokens(t *testing.T) {
	jwtConfig := config.JWTConfig{
		Secret:             "test-secret",
		AccessTokenMinutes: 15,
		RefreshTokenDays:   7,
		ExpiryHours:        24,
	}

	// Test data
	hashedPassword, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	activeUser := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "test@example.com",
		Password:  string(hashedPassword),
		IsActive:  true,
		Role:      models.RoleCustomer,
	}

	t.Run("successful login with tokens", func(t *testing.T) {
		mockUserRepo := &mocks.UserRepository{}
		mockAPIKeyRepo := &mocks.APIKeyRepository{}
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

		mockUserRepo.On("GetByEmail", "test@example.com").Return(activeUser, nil)
		mockUserRepo.On("UpdateLastLogin", uint(1)).Return(nil)

		tokens, err := authService.LoginWithTokens("test@example.com", "password123")

		assert.NoError(t, err)
		assert.NotNil(t, tokens)
		assert.NotEmpty(t, tokens.AccessToken)
		// LoginWithTokens does not generate refresh tokens in this implementation
		mockUserRepo.AssertExpectations(t)
	})

	t.Run("login fails with invalid password", func(t *testing.T) {
		mockUserRepo := &mocks.UserRepository{}
		mockAPIKeyRepo := &mocks.APIKeyRepository{}
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

		mockUserRepo.On("GetByEmail", "test@example.com").Return(activeUser, nil)
		mockUserRepo.On("Update", mock.AnythingOfType("*models.User")).Return(nil)

		tokens, err := authService.LoginWithTokens("test@example.com", "wrongpassword")

		assert.Error(t, err)
		assert.Nil(t, tokens)
		assert.Equal(t, "invalid credentials", err.Error())
	})

	t.Run("login fails with inactive user", func(t *testing.T) {
		mockUserRepo := &mocks.UserRepository{}
		mockAPIKeyRepo := &mocks.APIKeyRepository{}
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

		inactiveUser := &models.User{
			BaseModel: models.BaseModel{ID: 2},
			Email:     "inactive@example.com",
			Password:  string(hashedPassword),
			IsActive:  false,
			Role:      models.RoleCustomer,
		}

		mockUserRepo.On("GetByEmail", "inactive@example.com").Return(inactiveUser, nil)

		tokens, err := authService.LoginWithTokens("inactive@example.com", "password123")

		assert.Error(t, err)
		assert.Nil(t, tokens)
		// Login returns "invalid credentials" for inactive users to prevent account enumeration
		assert.Equal(t, "invalid credentials", err.Error())
	})
}

func TestAuthService_RefreshAccessToken(t *testing.T) {
	jwtConfig := config.JWTConfig{
		Secret:             "test-secret",
		AccessTokenMinutes: 15,
		RefreshTokenDays:   7,
		ExpiryHours:        24,
	}

	t.Run("refresh returns not implemented", func(t *testing.T) {
		mockUserRepo := &mocks.UserRepository{}
		mockAPIKeyRepo := &mocks.APIKeyRepository{}
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

		tokens, err := authService.RefreshAccessToken("some-refresh-token")

		assert.Error(t, err)
		assert.Nil(t, tokens)
		assert.Equal(t, "refresh tokens not implemented", err.Error())
	})
}

func TestAuthService_GenerateCSRFToken(t *testing.T) {
	jwtConfig := config.JWTConfig{Secret: "test-secret", ExpiryHours: 24}

	t.Run("generates valid CSRF token", func(t *testing.T) {
		mockUserRepo := &mocks.UserRepository{}
		mockAPIKeyRepo := &mocks.APIKeyRepository{}
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

		token, err := authService.GenerateCSRFToken()

		assert.NoError(t, err)
		assert.NotEmpty(t, token)
		assert.True(t, len(token) > 0)
	})
}

func TestAuthService_ValidateCSRFToken(t *testing.T) {
	jwtConfig := config.JWTConfig{Secret: "test-secret", ExpiryHours: 24}

	t.Run("validates non-empty token", func(t *testing.T) {
		mockUserRepo := &mocks.UserRepository{}
		mockAPIKeyRepo := &mocks.APIKeyRepository{}
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

		token, err := authService.GenerateCSRFToken()
		assert.NoError(t, err)

		valid := authService.ValidateCSRFToken(token)
		assert.True(t, valid)
	})

	t.Run("rejects empty token", func(t *testing.T) {
		mockUserRepo := &mocks.UserRepository{}
		mockAPIKeyRepo := &mocks.APIKeyRepository{}
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

		valid := authService.ValidateCSRFToken("")
		assert.False(t, valid)
	})
}

func TestAuthService_InvalidateRefreshToken(t *testing.T) {
	jwtConfig := config.JWTConfig{Secret: "test-secret", ExpiryHours: 24}

	t.Run("returns not implemented", func(t *testing.T) {
		mockUserRepo := &mocks.UserRepository{}
		mockAPIKeyRepo := &mocks.APIKeyRepository{}
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

		err := authService.InvalidateRefreshToken("test-refresh-token")

		assert.Error(t, err)
		assert.Equal(t, "refresh tokens not implemented", err.Error())
	})
}
