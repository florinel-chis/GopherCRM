package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type authService struct {
	userRepo   repository.UserRepository
	apiKeyRepo repository.APIKeyRepository
	jwtConfig  config.JWTConfig
}

func NewAuthService(userRepo repository.UserRepository, apiKeyRepo repository.APIKeyRepository, jwtConfig config.JWTConfig) AuthService {
	return &authService{
		userRepo:   userRepo,
		apiKeyRepo: apiKeyRepo,
		jwtConfig:  jwtConfig,
	}
}

func (s *authService) Login(email, password string) (string, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("email", email), "AuthService", "Login")

	// Pre-computed dummy hash for timing attack prevention
	// This is a bcrypt hash of "dummy-password-for-timing-attack-prevention"
	const dummyHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

	user, err := s.userRepo.GetByEmail(email)

	// Always perform bcrypt comparison to maintain constant timing
	// If user doesn't exist, compare against dummy hash
	var passwordHash string
	if err != nil {
		// User not found - use dummy hash to prevent timing attack
		passwordHash = dummyHash
	} else {
		passwordHash = user.Password
	}

	// Perform password comparison (always happens regardless of whether user exists)
	bcryptErr := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password))

	// Now check if user lookup failed
	if err != nil {
		logger.WithError(err).Warn("Login failed - user not found")
		return "", errors.New("invalid credentials")
	}

	// Check password result
	if bcryptErr != nil {
		logger.WithField("user_id", user.ID).Warn("Login failed - invalid password")
		return "", errors.New("invalid credentials")
	}

	// Check if account is active (after password verification to prevent account enumeration)
	if !user.IsActive {
		logger.WithField("user_id", user.ID).Warn("Login failed - account disabled")
		return "", errors.New("invalid credentials") // Use same error message
	}

	// Update last login timestamp
	if err := s.userRepo.UpdateLastLogin(user.ID); err != nil {
		logger.WithError(err).Warn("Failed to update last login time")
	}

	// Generate JWT token
	token, err := s.GenerateJWT(user)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return "", err
	}

	logger.WithFields(map[string]interface{}{
		"user_id": user.ID,
		"role":    user.Role,
	}).Info("User logged in successfully")

	return token, nil
}

func (s *authService) ValidateToken(tokenString string) (*models.User, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(s.jwtConfig.Secret), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		// Safe type assertion with validation
		userIDFloat, ok := claims["user_id"].(float64)
		if !ok {
			return nil, errors.New("invalid user_id in token claims")
		}

		userID := uint(userIDFloat)
		return s.userRepo.GetByID(userID)
	}

	return nil, errors.New("invalid token")
}

func (s *authService) ValidateAPIKey(key string) (*models.User, error) {
	hashedKey := utils.HashAPIKey(key)
	apiKey, err := s.apiKeyRepo.GetByKeyHash(hashedKey)
	if err != nil {
		return nil, errors.New("invalid API key")
	}

	// Check if API key is active (not revoked)
	if !apiKey.IsActive {
		return nil, errors.New("API key has been revoked")
	}

	// Check expiration
	if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
		return nil, errors.New("API key expired")
	}

	// Update last used timestamp (best effort - don't fail validation if this fails)
	if err := s.apiKeyRepo.UpdateLastUsed(apiKey.ID); err != nil {
		// Log error but don't fail validation
		utils.Logger.WithError(err).WithField("api_key_id", apiKey.ID).Warn("Failed to update API key last used timestamp")
	}

	return &apiKey.User, nil
}

func (s *authService) GenerateJWT(user *models.User) (string, error) {
	claims := jwt.MapClaims{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    user.Role,
		"exp":     time.Now().Add(time.Hour * time.Duration(s.jwtConfig.ExpiryHours)).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.jwtConfig.Secret))
}

func (s *authService) LoginWithTokens(email, password string) (*AuthTokens, error) {
	accessToken, err := s.Login(email, password)
	if err != nil {
		return nil, err
	}
	return &AuthTokens{AccessToken: accessToken}, nil
}

func (s *authService) GenerateTokens(user *models.User) (*AuthTokens, error) {
	accessToken, err := s.GenerateJWT(user)
	if err != nil {
		return nil, err
	}
	return &AuthTokens{AccessToken: accessToken}, nil
}

func (s *authService) RefreshAccessToken(refreshToken string) (*AuthTokens, error) {
	return nil, errors.New("refresh tokens not implemented")
}

func (s *authService) InvalidateRefreshToken(refreshToken string) error {
	return errors.New("refresh tokens not implemented")
}

func (s *authService) GenerateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *authService) ValidateCSRFToken(token string) bool {
	return token != ""
}

