package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type authService struct {
	userRepo     repository.UserRepository
	apiKeyRepo   repository.APIKeyRepository
	jwtConfig    config.JWTConfig
	apiKeySecret string
}

func NewAuthService(userRepo repository.UserRepository, apiKeyRepo repository.APIKeyRepository, jwtConfig config.JWTConfig, apiKeySecret ...string) AuthService {
	secret := ""
	if len(apiKeySecret) > 0 {
		secret = apiKeySecret[0]
	}
	return &authService{
		userRepo:     userRepo,
		apiKeyRepo:   apiKeyRepo,
		jwtConfig:    jwtConfig,
		apiKeySecret: secret,
	}
}

// MaxFailedLoginAttempts is the number of failed attempts before an account is locked.
const MaxFailedLoginAttempts = 5

// AccountLockDuration is how long an account stays locked after too many failed attempts.
const AccountLockDuration = 15 * time.Minute

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

	// Check if account is locked
	if user.LockedUntil != nil && user.LockedUntil.After(time.Now()) {
		logger.WithField("user_id", user.ID).Warn("Login failed - account locked")
		return "", errors.New("account is locked due to too many failed login attempts, please try again later")
	}

	// Check password result
	if bcryptErr != nil {
		logger.WithField("user_id", user.ID).Warn("Login failed - invalid password")
		// Increment failed login attempts
		user.FailedLoginAttempts++
		if user.FailedLoginAttempts >= MaxFailedLoginAttempts {
			lockUntil := time.Now().Add(AccountLockDuration)
			user.LockedUntil = &lockUntil
			logger.WithField("user_id", user.ID).Warn("Account locked due to too many failed login attempts")
		}
		if updateErr := s.userRepo.Update(user); updateErr != nil {
			logger.WithError(updateErr).Warn("Failed to update failed login attempts")
		}
		return "", errors.New("invalid credentials")
	}

	// Check if account is active (after password verification to prevent account enumeration)
	if !user.IsActive {
		logger.WithField("user_id", user.ID).Warn("Login failed - account disabled")
		return "", errors.New("invalid credentials") // Use same error message
	}

	// Reset failed login attempts on successful login
	if user.FailedLoginAttempts > 0 || user.LockedUntil != nil {
		user.FailedLoginAttempts = 0
		user.LockedUntil = nil
		if updateErr := s.userRepo.Update(user); updateErr != nil {
			logger.WithError(updateErr).Warn("Failed to reset failed login attempts")
		}
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
	// Try HMAC-SHA256 hash first (new format)
	hmacHash := utils.HashAPIKeyHMAC(key, s.apiKeySecret)
	apiKey, err := s.apiKeyRepo.GetByKeyHash(hmacHash)
	if err != nil {
		// Fall back to legacy plain SHA256 hash for migration
		legacyHash := utils.HashAPIKey(key)
		apiKey, err = s.apiKeyRepo.GetByKeyHash(legacyHash)
		if err != nil {
			return nil, errors.New("invalid API key")
		}
	}

	// Check if API key is active (not revoked)
	if !apiKey.IsActive {
		return nil, errors.New("API key has been revoked")
	}

	// Check expiration
	if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
		return nil, errors.New("API key expired")
	}

	// Defence in depth: a credential must never outlive its owner.
	//
	// Erasing a user destroys their API keys in the same transaction, so in
	// principle no key can reach this point without a live owner. This check is
	// the second line: a key restored from a backup, written by an older
	// release, or one that escaped the purge for any reason at all must not
	// authenticate. The lookup is deliberately the SCOPED one, so an erased
	// (soft-deleted) owner simply does not exist and the key is rejected —
	// relying on the preloaded association would not do, because a preload of a
	// soft-deleted row silently yields a zero-valued user, which would sail
	// through as a user with ID 0.
	user, err := s.userRepo.GetByID(apiKey.UserID)
	if err != nil {
		utils.Logger.WithField("api_key_id", apiKey.ID).WithField("user_id", apiKey.UserID).
			Warn("Rejected API key whose owner no longer exists")
		return nil, errors.New("invalid API key")
	}
	if !user.IsActive {
		utils.Logger.WithField("api_key_id", apiKey.ID).WithField("user_id", apiKey.UserID).
			Warn("Rejected API key of an inactive user")
		return nil, errors.New("API key owner is not active")
	}

	// Update last used timestamp (best effort - don't fail validation if this fails)
	if err := s.apiKeyRepo.UpdateLastUsed(apiKey.ID); err != nil {
		// Log error but don't fail validation
		utils.Logger.WithError(err).WithField("api_key_id", apiKey.ID).Warn("Failed to update API key last used timestamp")
	}

	return user, nil
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

// csrfTokenMaxAge is the maximum age of a CSRF token (24 hours).
const csrfTokenMaxAge = 24 * time.Hour

// generateCSRFHMAC computes HMAC-SHA256 of the given message using the JWT secret.
func (s *authService) generateCSRFHMAC(message string) string {
	mac := hmac.New(sha256.New, []byte(s.jwtConfig.Secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// GenerateCSRFToken creates an HMAC-SHA256 signed CSRF token encoding a nonce and timestamp.
// The token format is base64url(nonce:timestamp.hmac_hex).
func (s *authService) GenerateCSRFToken() (string, error) {
	// Generate a random nonce
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("failed to generate CSRF nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	message := nonce + ":" + timestamp
	sig := s.generateCSRFHMAC(message)

	raw := message + "." + sig
	encoded := base64.RawURLEncoding.EncodeToString([]byte(raw))
	return encoded, nil
}

// ValidateCSRFToken decodes the token, verifies the HMAC signature, and checks
// that the token is not older than 24 hours.
func (s *authService) ValidateCSRFToken(token string) bool {
	if token == "" {
		return false
	}

	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}

	// Split into message and signature: "nonce:timestamp.hmac_hex"
	parts := strings.SplitN(string(decoded), ".", 2)
	if len(parts) != 2 {
		return false
	}
	message := parts[0]
	providedSig := parts[1]

	// Verify HMAC signature using constant-time comparison
	expectedSig := s.generateCSRFHMAC(message)
	if !hmac.Equal([]byte(expectedSig), []byte(providedSig)) {
		return false
	}

	// Extract timestamp from message ("nonce:timestamp")
	msgParts := strings.SplitN(message, ":", 2)
	if len(msgParts) != 2 {
		return false
	}

	tsUnix, err := strconv.ParseInt(msgParts[1], 10, 64)
	if err != nil {
		return false
	}

	// Check token age
	tokenTime := time.Unix(tsUnix, 0)
	if time.Since(tokenTime) > csrfTokenMaxAge {
		return false
	}

	return true
}

