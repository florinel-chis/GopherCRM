package service

import (
	"errors"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"golang.org/x/crypto/bcrypt"
)

// Mock repositories
type MockUserRepository struct {
	mock.Mock
}

func (m *MockUserRepository) Create(user *models.User) error {
	args := m.Called(user)
	return args.Error(0)
}

func (m *MockUserRepository) GetByID(id uint) (*models.User, error) {
	args := m.Called(id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.User), args.Error(1)
}

func (m *MockUserRepository) GetByEmail(email string) (*models.User, error) {
	args := m.Called(email)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.User), args.Error(1)
}

func (m *MockUserRepository) Update(user *models.User) error {
	args := m.Called(user)
	return args.Error(0)
}

func (m *MockUserRepository) Delete(id uint) error {
	args := m.Called(id)
	return args.Error(0)
}

func (m *MockUserRepository) List(offset, limit int) ([]models.User, error) {
	args := m.Called(offset, limit)
	return args.Get(0).([]models.User), args.Error(1)
}

func (m *MockUserRepository) Count() (int64, error) {
	args := m.Called()
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockUserRepository) UpdateLastLogin(id uint) error {
	args := m.Called(id)
	return args.Error(0)
}

type MockAPIKeyRepository struct {
	mock.Mock
}

func (m *MockAPIKeyRepository) Create(apiKey *models.APIKey) error {
	args := m.Called(apiKey)
	return args.Error(0)
}

func (m *MockAPIKeyRepository) GetByID(id uint) (*models.APIKey, error) {
	args := m.Called(id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.APIKey), args.Error(1)
}

func (m *MockAPIKeyRepository) GetByKeyHash(keyHash string) (*models.APIKey, error) {
	args := m.Called(keyHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.APIKey), args.Error(1)
}

func (m *MockAPIKeyRepository) GetByUserID(userID uint) ([]models.APIKey, error) {
	args := m.Called(userID)
	return args.Get(0).([]models.APIKey), args.Error(1)
}

func (m *MockAPIKeyRepository) Update(apiKey *models.APIKey) error {
	args := m.Called(apiKey)
	return args.Error(0)
}

func (m *MockAPIKeyRepository) Delete(id uint) error {
	args := m.Called(id)
	return args.Error(0)
}

func (m *MockAPIKeyRepository) UpdateLastUsed(id uint) error {
	args := m.Called(id)
	return args.Error(0)
}

func init() {
	// Initialize logger for tests
	utils.Logger = logrus.New()
	utils.Logger.SetLevel(logrus.WarnLevel) // Reduce noise in tests
}

func TestAuthService_Login(t *testing.T) {
	t.Run("successful login", func(t *testing.T) {
		mockUserRepo := new(MockUserRepository)
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		jwtConfig := config.JWTConfig{
			Secret:      "test-secret",
			ExpiryHours: 24,
		}
		
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)
		password := "password123"
		hashedPassword, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		
		user := &models.User{
			BaseModel: models.BaseModel{ID: 1},
			Email:     "test@example.com",
			Password:  string(hashedPassword),
			IsActive:  true,
			Role:      models.RoleCustomer,
		}

		mockUserRepo.On("GetByEmail", "test@example.com").Return(user, nil)
		mockUserRepo.On("UpdateLastLogin", uint(1)).Return(nil)

		token, err := authService.Login("test@example.com", password)
		
		assert.NoError(t, err)
		assert.NotEmpty(t, token)
		mockUserRepo.AssertExpectations(t)
	})

	t.Run("invalid email", func(t *testing.T) {
		mockUserRepo := new(MockUserRepository)
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		jwtConfig := config.JWTConfig{
			Secret:      "test-secret",
			ExpiryHours: 24,
		}
		
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)
		mockUserRepo.On("GetByEmail", "invalid@example.com").Return(nil, errors.New("user not found"))

		token, err := authService.Login("invalid@example.com", "password")
		
		assert.Error(t, err)
		assert.Equal(t, "invalid credentials", err.Error())
		assert.Empty(t, token)
		mockUserRepo.AssertExpectations(t)
	})

	t.Run("wrong password", func(t *testing.T) {
		mockUserRepo := new(MockUserRepository)
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		jwtConfig := config.JWTConfig{
			Secret:      "test-secret",
			ExpiryHours: 24,
		}
		
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)
		hashedPassword, _ := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
		
		user := &models.User{
			BaseModel: models.BaseModel{ID: 1},
			Email:     "test@example.com",
			Password:  string(hashedPassword),
			IsActive:  true,
		}

		mockUserRepo.On("GetByEmail", "test@example.com").Return(user, nil)

		token, err := authService.Login("test@example.com", "wrong-password")
		
		assert.Error(t, err)
		assert.Equal(t, "invalid credentials", err.Error())
		assert.Empty(t, token)
		mockUserRepo.AssertExpectations(t)
	})

	t.Run("inactive user", func(t *testing.T) {
		mockUserRepo := new(MockUserRepository)
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		jwtConfig := config.JWTConfig{
			Secret:      "test-secret",
			ExpiryHours: 24,
		}
		
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)
		password := "password123"
		hashedPassword, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		
		user := &models.User{
			BaseModel: models.BaseModel{ID: 1},
			Email:     "test@example.com",
			Password:  string(hashedPassword),
			IsActive:  false,
		}

		mockUserRepo.On("GetByEmail", "test@example.com").Return(user, nil)

		token, err := authService.Login("test@example.com", password)
		
		assert.Error(t, err)
		assert.Equal(t, "account is disabled", err.Error())
		assert.Empty(t, token)
		mockUserRepo.AssertExpectations(t)
	})
}

func TestAuthService_GenerateJWT(t *testing.T) {
	mockUserRepo := new(MockUserRepository)
	mockAPIKeyRepo := new(MockAPIKeyRepository)
	jwtConfig := config.JWTConfig{
		Secret:      "test-secret",
		ExpiryHours: 24,
	}
	
	authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

	user := &models.User{
		BaseModel: models.BaseModel{ID: 1},
		Email:     "test@example.com",
		Role:      models.RoleAdmin,
	}

	token, err := authService.GenerateJWT(user)
	
	assert.NoError(t, err)
	assert.NotEmpty(t, token)

	// Parse and validate the token
	parsedToken, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(jwtConfig.Secret), nil
	})
	
	assert.NoError(t, err)
	assert.True(t, parsedToken.Valid)
	
	claims, ok := parsedToken.Claims.(jwt.MapClaims)
	assert.True(t, ok)
	assert.Equal(t, float64(1), claims["user_id"])
	assert.Equal(t, "test@example.com", claims["email"])
	assert.Equal(t, "admin", claims["role"])
}

func TestAuthService_ValidateToken(t *testing.T) {
	mockUserRepo := new(MockUserRepository)
	mockAPIKeyRepo := new(MockAPIKeyRepository)
	jwtConfig := config.JWTConfig{
		Secret:      "test-secret",
		ExpiryHours: 24,
	}
	
	authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)

	t.Run("valid token", func(t *testing.T) {
		user := &models.User{
			BaseModel: models.BaseModel{ID: 1},
			Email:     "test@example.com",
			Role:      models.RoleCustomer,
		}

		token, _ := authService.GenerateJWT(user)
		mockUserRepo.On("GetByID", uint(1)).Return(user, nil)

		validatedUser, err := authService.ValidateToken(token)
		
		assert.NoError(t, err)
		assert.NotNil(t, validatedUser)
		assert.Equal(t, user.ID, validatedUser.ID)
		mockUserRepo.AssertExpectations(t)
	})

	t.Run("invalid token", func(t *testing.T) {
		validatedUser, err := authService.ValidateToken("invalid-token")
		
		assert.Error(t, err)
		assert.Nil(t, validatedUser)
	})

	t.Run("expired token", func(t *testing.T) {
		// Create token with negative expiry
		claims := jwt.MapClaims{
			"user_id": 1,
			"email":   "test@example.com",
			"role":    "customer",
			"exp":     time.Now().Add(-time.Hour).Unix(),
		}
		
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tokenString, _ := token.SignedString([]byte(jwtConfig.Secret))

		validatedUser, err := authService.ValidateToken(tokenString)
		
		assert.Error(t, err)
		assert.Nil(t, validatedUser)
	})
}

func TestAuthService_ValidateAPIKey(t *testing.T) {
	t.Run("valid API key", func(t *testing.T) {
		mockUserRepo := new(MockUserRepository)
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		jwtConfig := config.JWTConfig{
			Secret:      "test-secret",
			ExpiryHours: 24,
		}
		
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)
		user := &models.User{
			BaseModel: models.BaseModel{ID: 1},
			Email:     "test@example.com",
			Role:      models.RoleCustomer,
		}

		apiKey := &models.APIKey{
			BaseModel: models.BaseModel{ID: 1},
			UserID:    1,
			User:      *user,
			IsActive:  true,
		}

		mockAPIKeyRepo.On("GetByKeyHash", mock.Anything).Return(apiKey, nil)
		mockAPIKeyRepo.On("UpdateLastUsed", uint(1)).Return(nil)

		validatedUser, err := authService.ValidateAPIKey("test-api-key")
		
		assert.NoError(t, err)
		assert.NotNil(t, validatedUser)
		assert.Equal(t, user.ID, validatedUser.ID)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("invalid API key", func(t *testing.T) {
		mockUserRepo := new(MockUserRepository)
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		jwtConfig := config.JWTConfig{
			Secret:      "test-secret",
			ExpiryHours: 24,
		}
		
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)
		mockAPIKeyRepo.On("GetByKeyHash", mock.Anything).Return(nil, errors.New("not found"))

		validatedUser, err := authService.ValidateAPIKey("invalid-key")
		
		assert.Error(t, err)
		assert.Equal(t, "invalid API key", err.Error())
		assert.Nil(t, validatedUser)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("expired API key", func(t *testing.T) {
		mockUserRepo := new(MockUserRepository)
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		jwtConfig := config.JWTConfig{
			Secret:      "test-secret",
			ExpiryHours: 24,
		}
		
		authService := NewAuthService(mockUserRepo, mockAPIKeyRepo, jwtConfig)
		expiredTime := time.Now().Add(-time.Hour)
		apiKey := &models.APIKey{
			BaseModel: models.BaseModel{ID: 1},
			UserID:    1,
			IsActive:  true,
			ExpiresAt: &expiredTime,
		}

		mockAPIKeyRepo.On("GetByKeyHash", mock.Anything).Return(apiKey, nil)

		validatedUser, err := authService.ValidateAPIKey("expired-key")
		
		assert.Error(t, err)
		assert.Equal(t, "API key expired", err.Error())
		assert.Nil(t, validatedUser)
		mockAPIKeyRepo.AssertExpectations(t)
	})
}