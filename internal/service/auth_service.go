package service

import (
	"errors"
	"time"

	"github.com/florinel-chis/gocrm/internal/config"
	"github.com/florinel-chis/gocrm/internal/models"
	"github.com/florinel-chis/gocrm/internal/repository"
	"github.com/florinel-chis/gocrm/internal/utils"
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
	
	user, err := s.userRepo.GetByEmail(email)
	if err != nil {
		logger.WithError(err).Warn("Login failed - user not found")
		return "", errors.New("invalid credentials")
	}

	if !user.IsActive {
		logger.WithField("user_id", user.ID).Warn("Login failed - account disabled")
		return "", errors.New("account is disabled")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		logger.WithField("user_id", user.ID).Warn("Login failed - invalid password")
		return "", errors.New("invalid credentials")
	}

	if err := s.userRepo.UpdateLastLogin(user.ID); err != nil {
		logger.WithError(err).Warn("Failed to update last login time")
	}

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
		userID := uint(claims["user_id"].(float64))
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

	if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
		return nil, errors.New("API key expired")
	}

	if err := s.apiKeyRepo.UpdateLastUsed(apiKey.ID); err != nil {
		// Log error but don't fail validation
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

