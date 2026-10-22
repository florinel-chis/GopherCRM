package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm/clause"
)

// Sentinel errors for the session lifecycle, classified with errors.Is().
// The refresh and reset sentinels are deliberately generic: the endpoints that
// surface them must not disclose why a token was rejected (revoked vs expired
// vs never issued) or whether an account exists.
var (
	// ErrInvalidRefreshToken covers every rejection of a presented refresh
	// token: unknown, revoked, expired, or owned by an inactive/erased user.
	ErrInvalidRefreshToken = errors.New("invalid refresh token")
	// ErrInvalidCurrentPassword means the re-authentication step of a password
	// change failed. It is a 400-class validation failure of the request body,
	// not a 401 authentication failure of the session presenting it.
	ErrInvalidCurrentPassword = errors.New("current password is incorrect")
	// ErrInvalidResetToken covers every rejection of a password reset token:
	// unknown, expired, already used, or owned by an inactive/erased user.
	ErrInvalidResetToken = errors.New("invalid or expired reset token")
)

// resetTokenTTL is the fixed lifetime of a password reset token.
const resetTokenTTL = time.Hour

// sessionTokenBytes is the entropy of opaque refresh/reset tokens (256 bits).
const sessionTokenBytes = 32

type authService struct {
	userRepo         repository.UserRepository
	apiKeyRepo       repository.APIKeyRepository
	refreshTokenRepo repository.RefreshTokenRepository
	resetTokenRepo   repository.PasswordResetTokenRepository
	// txManager binds the password reset confirmation into one transaction:
	// token claim, password and lockout update, and session revocation. It is
	// required for that flow; without it the confirmation is refused rather
	// than run as separate writes.
	txManager    repository.TransactionManager
	mailer       mailer.Mailer
	jwtConfig    config.JWTConfig
	apiKeySecret string
	appBaseURL   string
	// loginTime is an instance-local clock for lockout decisions; nil uses wall time.
	loginTime func() time.Time
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

// NewAuthServiceWithSessions constructs the fully wired auth service with the
// session lifecycle enabled: refresh token rotation, logout revocation and the
// password reset flow. The transaction manager must be built on the same
// database handle as the repositories: the reset confirmation claims the
// token, writes the password and revokes the sessions through the
// transaction-bound repositories it yields. NewAuthService (above) remains for
// callers that only need stateless JWT auth — on such an instance the session
// methods reject every token with the generic sentinels instead of panicking.
func NewAuthServiceWithSessions(
	userRepo repository.UserRepository,
	apiKeyRepo repository.APIKeyRepository,
	refreshTokenRepo repository.RefreshTokenRepository,
	resetTokenRepo repository.PasswordResetTokenRepository,
	txManager repository.TransactionManager,
	m mailer.Mailer,
	jwtConfig config.JWTConfig,
	appBaseURL string,
	apiKeySecret string,
) AuthService {
	return &authService{
		userRepo:         userRepo,
		apiKeyRepo:       apiKeyRepo,
		refreshTokenRepo: refreshTokenRepo,
		resetTokenRepo:   resetTokenRepo,
		txManager:        txManager,
		mailer:           m,
		jwtConfig:        jwtConfig,
		apiKeySecret:     apiKeySecret,
		appBaseURL:       strings.TrimRight(appBaseURL, "/"),
	}
}

// MaxFailedLoginAttempts is the number of failed attempts before an account is locked.
const MaxFailedLoginAttempts = 5

// AccountLockDuration is how long an account stays locked after too many failed attempts.
const AccountLockDuration = 15 * time.Minute

func (s *authService) Login(email, password string) (string, error) {
	user, err := s.authenticate(email, password)
	if err != nil {
		return "", err
	}
	return s.GenerateJWT(user)
}

// authenticate verifies credentials and account state, maintaining the
// constant-time and anti-enumeration behaviour: unknown email, wrong password,
// locked account and deactivated account are indistinguishable to the caller.
func (s *authService) authenticate(email, password string) (*models.User, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "auth"), "AuthService", "Login")

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
		logger.WithError(utils.LogError(err)).Warn("Login failed - user not found")
		return nil, errors.New("invalid credentials")
	}

	clock := time.Now
	if s.loginTime != nil {
		clock = s.loginTime
	}
	now := clock()
	// Check if account is locked
	if user.LockedUntil != nil && user.LockedUntil.After(now) {
		logger.WithField("user_id", user.ID).Warn("Login failed - account locked")
		return nil, errors.New("account is locked due to too many failed login attempts, please try again later")
	}

	// Check password result
	if bcryptErr != nil {
		logger.WithField("user_id", user.ID).Warn("Login failed - invalid password")
		// An expired lock starts a fresh failure window, including at its
		// exact expiry. Active locks have already returned above.
		if user.LockedUntil != nil {
			user.FailedLoginAttempts = 0
			user.LockedUntil = nil
		}
		// Increment failed login attempts
		user.FailedLoginAttempts++
		if user.FailedLoginAttempts >= MaxFailedLoginAttempts {
			lockUntil := now.Add(AccountLockDuration)
			user.LockedUntil = &lockUntil
			logger.WithField("user_id", user.ID).Warn("Account locked due to too many failed login attempts")
		}
		if updateErr := s.userRepo.Update(user); updateErr != nil {
			logger.WithError(utils.LogError(updateErr)).Warn("Failed to update failed login attempts")
		}
		return nil, errors.New("invalid credentials")
	}

	// Check if account is active (after password verification to prevent account enumeration)
	if !user.IsActive {
		logger.WithField("user_id", user.ID).Warn("Login failed - account disabled")
		return nil, errors.New("invalid credentials") // Use same error message
	}

	// Reset failed login attempts on successful login
	if user.FailedLoginAttempts > 0 || user.LockedUntil != nil {
		user.FailedLoginAttempts = 0
		user.LockedUntil = nil
		if updateErr := s.userRepo.Update(user); updateErr != nil {
			logger.WithError(utils.LogError(updateErr)).Warn("Failed to reset failed login attempts")
		}
	}

	// Update last login timestamp
	if err := s.userRepo.UpdateLastLogin(user.ID); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Failed to update last login time")
	}

	logger.WithFields(map[string]interface{}{
		"user_id": user.ID,
		"role":    user.Role,
	}).Info("User logged in successfully")

	return user, nil
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
		user, err := s.userRepo.GetByID(userID)
		if err != nil {
			return nil, err
		}
		if !user.IsActive {
			utils.Logger.WithField("user_id", user.ID).
				Warn("Rejected bearer token of a deactivated user")
			return nil, errors.New("user account is deactivated")
		}
		return user, nil
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
		utils.Logger.WithError(utils.LogError(err)).WithField("api_key_id", apiKey.ID).Warn("Failed to update API key last used timestamp")
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
	user, err := s.authenticate(email, password)
	if err != nil {
		return nil, err
	}

	accessToken, err := s.GenerateJWT(user)
	if err != nil {
		return nil, err
	}

	tokens := &AuthTokens{AccessToken: accessToken, User: user}

	// Refresh tokens are issued only when the session store is wired. If
	// persisting one fails, the login still succeeds with a JWT-only session:
	// the response contract marks refresh_token as optional, and locking a
	// user out because the token table hiccuped would be the worse trade.
	if s.refreshTokenRepo != nil {
		refreshToken, err := s.issueRefreshToken(user.ID)
		if err != nil {
			utils.Logger.WithError(utils.LogError(err)).WithField("user_id", user.ID).
				Warn("Failed to issue refresh token at login; continuing with access token only")
		} else {
			tokens.RefreshToken = refreshToken
		}
	}

	return tokens, nil
}

func (s *authService) GenerateTokens(user *models.User) (*AuthTokens, error) {
	accessToken, err := s.GenerateJWT(user)
	if err != nil {
		return nil, err
	}
	return &AuthTokens{AccessToken: accessToken}, nil
}

// sessionTokenSecret is the HMAC key for hashing opaque session tokens:
// the dedicated API key secret when configured, the JWT secret otherwise.
func (s *authService) sessionTokenSecret() string {
	if s.apiKeySecret != "" {
		return s.apiKeySecret
	}
	return s.jwtConfig.Secret
}

// hashOpaqueToken derives the storage form of an opaque token:
// hex(HMAC-SHA256(token)). Only this hash is ever persisted or compared; the
// raw value exists client-side and, for reset tokens, inside the mailed link.
func hashOpaqueToken(token, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// generateOpaqueToken mints a 256-bit random token, base64url-encoded.
func generateOpaqueToken() (string, error) {
	buf := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// issueRefreshToken mints, hashes and persists a refresh token for the user,
// returning the raw value for the client. Lifetime comes from
// REFRESH_TOKEN_EXPIRY_DAYS (config JWT.RefreshTokenDays).
func (s *authService) issueRefreshToken(userID uint) (string, error) {
	raw, err := generateOpaqueToken()
	if err != nil {
		return "", err
	}

	days := s.jwtConfig.RefreshTokenDays
	if days <= 0 {
		days = 30
	}

	token := &models.RefreshToken{
		UserID:    userID,
		TokenHash: hashOpaqueToken(raw, s.sessionTokenSecret()),
		ExpiresAt: time.Now().Add(time.Duration(days) * 24 * time.Hour),
	}
	if err := s.refreshTokenRepo.Create(token); err != nil {
		return "", fmt.Errorf("failed to store refresh token: %w", err)
	}
	return raw, nil
}

// RefreshAccessToken validates the presented refresh token and rotates it:
// the presented token is revoked and a fresh JWT + refresh token are issued.
// Every rejection maps to ErrInvalidRefreshToken so callers cannot learn
// whether the token was unknown, revoked, expired, or orphaned.
func (s *authService) RefreshAccessToken(refreshToken string) (*AuthTokens, error) {
	if s.refreshTokenRepo == nil || refreshToken == "" {
		return nil, fmt.Errorf("refresh not available: %w", ErrInvalidRefreshToken)
	}

	hash := hashOpaqueToken(refreshToken, s.sessionTokenSecret())

	// The repository lookup already excludes revoked and expired rows.
	stored, err := s.refreshTokenRepo.GetByTokenHash(hash)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
		}
		return nil, fmt.Errorf("refresh token lookup failed: %w", err)
	}

	// Defence in depth in case a repository implementation stops filtering.
	if stored.Revoked || stored.ExpiresAt.Before(time.Now()) {
		return nil, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
	}

	// A token must never outlive or outrank its owner. The scoped lookup makes
	// an erased (soft-deleted) owner a not-found. Revoke the presented token on
	// this path so a dead account cannot keep probing with it.
	user, err := s.userRepo.GetByID(stored.UserID)
	if err != nil || !user.IsActive {
		if revokeErr := s.refreshTokenRepo.RevokeByTokenHash(hash); revokeErr != nil {
			utils.Logger.WithError(utils.LogError(revokeErr)).WithField("user_id", stored.UserID).
				Warn("Failed to revoke refresh token of inactive or missing user")
		}
		utils.Logger.WithField("user_id", stored.UserID).
			Warn("Rejected refresh token whose owner is missing or inactive")
		return nil, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
	}

	// Rotate: kill the presented token before minting its successor. If the
	// revocation fails the rotation is aborted — failing open here would let a
	// stolen token be replayed indefinitely.
	if err := s.refreshTokenRepo.RevokeByTokenHash(hash); err != nil {
		return nil, fmt.Errorf("failed to revoke refresh token during rotation: %w", err)
	}

	newRefreshToken, err := s.issueRefreshToken(user.ID)
	if err != nil {
		return nil, err
	}

	accessToken, err := s.GenerateJWT(user)
	if err != nil {
		return nil, err
	}

	utils.Logger.WithField("user_id", user.ID).Info("Refresh token rotated")
	return &AuthTokens{AccessToken: accessToken, RefreshToken: newRefreshToken, User: user}, nil
}

// InvalidateRefreshToken revokes the presented token unconditionally.
// Prefer Logout for request-scoped revocation, which is owner-checked.
func (s *authService) InvalidateRefreshToken(refreshToken string) error {
	if s.refreshTokenRepo == nil || refreshToken == "" {
		return fmt.Errorf("refresh not available: %w", ErrInvalidRefreshToken)
	}
	hash := hashOpaqueToken(refreshToken, s.sessionTokenSecret())
	return s.refreshTokenRepo.RevokeByTokenHash(hash)
}

// Logout revokes refresh tokens for the authenticated user. With an empty
// refreshToken every session of the user is revoked; with a specific one only
// that token — and only if it actually belongs to the user, so an
// authenticated caller cannot use logout to revoke someone else's session.
// Unknown or foreign tokens are ignored: logout is idempotent and discloses
// nothing about token validity.
func (s *authService) Logout(userID uint, refreshToken string) error {
	if s.refreshTokenRepo == nil {
		return nil
	}

	if refreshToken == "" {
		if err := s.refreshTokenRepo.RevokeAllForUser(userID); err != nil {
			return fmt.Errorf("failed to revoke sessions: %w", err)
		}
		utils.Logger.WithField("user_id", userID).Info("All sessions revoked at logout")
		return nil
	}

	hash := hashOpaqueToken(refreshToken, s.sessionTokenSecret())
	stored, err := s.refreshTokenRepo.GetByTokenHash(hash)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("logout lookup failed: %w", err)
	}
	if stored.UserID != userID {
		utils.Logger.WithField("user_id", userID).WithField("token_owner", stored.UserID).
			Warn("Logout presented a refresh token belonging to another user; ignoring")
		return nil
	}
	if err := s.refreshTokenRepo.RevokeByTokenHash(hash); err != nil {
		return fmt.Errorf("failed to revoke session: %w", err)
	}
	return nil
}

// ChangePassword re-authenticates with the current password, applies the
// complexity policy to the new one, updates the hash and revokes every
// refresh token so stolen sessions die with the old password.
func (s *authService) ChangePassword(userID uint, currentPassword, newPassword string) error {
	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		return fmt.Errorf("failed to load user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(currentPassword)); err != nil {
		utils.Logger.WithField("user_id", userID).Warn("Password change rejected - wrong current password")
		return ErrInvalidCurrentPassword
	}

	if err := utils.ValidatePasswordComplexity(newPassword); err != nil {
		return err
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}
	user.Password = string(hashed)

	if err := s.userRepo.Update(user); err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	s.revokeAllSessionsBestEffort(userID, "password change")
	utils.Logger.WithField("user_id", userID).Info("Password changed")
	return nil
}

// revokeAllSessionsBestEffort kills every refresh token of the user after a
// password change. The credential update has already been committed, so a
// revocation failure is logged loudly rather than surfaced as a failure of an
// operation that in fact succeeded. The password reset flow does not use it:
// there the revocation is part of the transaction and a failure rolls the
// reset back.
func (s *authService) revokeAllSessionsBestEffort(userID uint, reason string) {
	if s.refreshTokenRepo == nil {
		return
	}
	if err := s.refreshTokenRepo.RevokeAllForUser(userID); err != nil {
		utils.Logger.WithError(utils.LogError(err)).WithField("user_id", userID).WithField("reason", reason).
			Error("Failed to revoke refresh tokens after credential change")
	}
}

// RequestPasswordReset starts the reset flow. It NEVER discloses whether the
// account exists: unknown email, inactive account, storage failure and mail
// failure all return nil, so the endpoint's response and timing stay uniform.
// The raw token goes only into the mailed link; the store holds its hash.
func (s *authService) RequestPasswordReset(email string) error {
	logger := utils.Logger.WithField("handler", "AuthService.RequestPasswordReset")

	if s.resetTokenRepo == nil || s.mailer == nil {
		logger.Warn("Password reset requested but the reset flow is not wired")
		return nil
	}

	user, err := s.userRepo.GetByEmail(email)
	if err != nil {
		if apperrors.IsNotFound(err) {
			logger.Info("Password reset requested for unknown email")
		} else {
			logger.WithError(utils.LogError(err)).Error("Password reset user lookup failed")
		}
		return nil
	}
	if !user.IsActive {
		logger.WithField("user_id", user.ID).Info("Password reset requested for inactive account; ignored")
		return nil
	}

	// Only the newest link should work; stale outstanding tokens are spent.
	if err := s.resetTokenRepo.InvalidateAllForUser(user.ID); err != nil {
		logger.WithError(utils.LogError(err)).WithField("user_id", user.ID).
			Warn("Failed to invalidate previous reset tokens")
	}

	raw, err := generateOpaqueToken()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to generate reset token")
		return nil
	}

	token := &models.PasswordResetToken{
		UserID:    user.ID,
		TokenHash: hashOpaqueToken(raw, s.sessionTokenSecret()),
		ExpiresAt: time.Now().Add(resetTokenTTL),
	}
	if err := s.resetTokenRepo.Create(token); err != nil {
		logger.WithError(utils.LogError(err)).WithField("user_id", user.ID).Error("Failed to store reset token")
		return nil
	}

	resetURL := s.appBaseURL + "/reset-password?token=" + url.QueryEscape(raw)
	if err := s.mailer.SendPasswordReset(user.Email, resetURL); err != nil {
		// Already logged (redacted) by the mailer; swallow to stay uniform.
		logger.WithField("user_id", user.ID).Warn("Password reset mail delivery failed")
	}
	return nil
}

// ConfirmPasswordReset spends a reset token and sets the new password. All
// token rejections map to ErrInvalidResetToken; a password that fails the
// complexity policy or cannot be hashed is reported as such.
//
// The new password is validated and hashed before anything is written, so a
// weak or unhashable password never spends the token: the user retries with
// the same link. The token lookup, its conditional claim, the owner check, the
// password and lockout update and the revocation of every refresh token are
// then one transaction. The claim stamps the token only while it is still
// unused and strictly unexpired and must affect exactly one row, so two
// requests that both read the token as spendable cannot both succeed: the
// second claim finds nothing to stamp and is rejected. A failure after the
// claim — the password update, the session revocation — rolls the whole unit
// back, token included, so the link stays usable for a retry instead of being
// spent on a reset that never happened. An inactive, erased or missing owner
// is rejected inside the transaction and leaves the token unspent as well; the
// owner is read with a locking read so that decision rests on the current
// committed account, not on the transaction's snapshot.
func (s *authService) ConfirmPasswordReset(token, newPassword string) error {
	// Every dependency of the transactional flow is required. Running the old
	// sequence of separate writes when one is missing would reintroduce the
	// burnt-link and double-spend failures this flow exists to prevent.
	if s.resetTokenRepo == nil || s.refreshTokenRepo == nil || s.txManager == nil || token == "" {
		return fmt.Errorf("reset not available: %w", ErrInvalidResetToken)
	}

	// A weak password must NOT spend the token: the user retries with the
	// same link instead of having to request a new one.
	if err := utils.ValidatePasswordComplexity(newPassword); err != nil {
		return err
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt refuses inputs over 72 bytes; nothing has been written.
		return fmt.Errorf("failed to hash password: %w", err)
	}

	hash := hashOpaqueToken(token, s.sessionTokenSecret())

	var userID uint
	err = s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		userID, err = s.confirmPasswordResetInTransaction(
			s.resetTokenRepo.WithTx(tx), s.userRepo.WithTx(tx.Clauses(clause.Locking{Strength: "UPDATE"})), s.refreshTokenRepo.WithTx(tx),
			hash, string(hashed))
		return err
	})
	if err != nil {
		return err
	}

	utils.Logger.WithField("user_id", userID).Info("Password reset completed; all sessions revoked")
	return nil
}

// confirmPasswordResetInTransaction performs the reset on transaction-bound
// repositories and returns the owner's id. Any error discards every write,
// the token claim included.
func (s *authService) confirmPasswordResetInTransaction(
	resetRepo repository.PasswordResetTokenRepository,
	userRepo repository.UserRepository,
	refreshRepo repository.RefreshTokenRepository,
	hash, passwordHash string,
) (uint, error) {
	// The lookup already excludes used and expired tokens; the claim below
	// re-applies both predicates as part of its own UPDATE.
	stored, err := resetRepo.GetByTokenHash(hash)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return 0, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken)
		}
		return 0, fmt.Errorf("reset token lookup failed: %w", err)
	}

	// Defence in depth in case a repository implementation stops filtering.
	if stored.UsedAt != nil || !stored.ExpiresAt.After(time.Now()) {
		return 0, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken)
	}

	// Claim the token before reading anything else. The claim is the one
	// write competing requests contend on: whoever stamps the row first owns
	// the reset, and every other request finds no row to stamp — even one
	// that passed the lookup a moment ago, or one whose token expired between
	// the lookup and this statement.
	if err := resetRepo.MarkUsed(stored.ID); err != nil {
		if apperrors.IsNotFound(err) {
			return 0, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken)
		}
		return 0, fmt.Errorf("failed to mark reset token used: %w", err)
	}

	// A token must never outlive its owner. The owner is read with a locking
	// read: on MySQL a plain SELECT inside this REPEATABLE READ transaction
	// would answer from the snapshot taken at the token lookup, so an account
	// deactivated or erased a moment after that lookup would still look active
	// — and the full-row update below would write that stale state back,
	// reactivating it. The locking read returns the current committed row and
	// holds its lock until commit, so nothing changes it underneath the update.
	// The scoped lookup makes an erased (soft-deleted) owner a not-found; a
	// deactivated one is refused the same way. The rollback that follows hands
	// the claim back, so the committed state is exactly what it was.
	user, err := userRepo.GetByID(stored.UserID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return 0, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken)
		}
		return 0, fmt.Errorf("reset token owner lookup failed: %w", err)
	}
	if !user.IsActive {
		return 0, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken)
	}

	user.Password = passwordHash
	// Ownership of the mailbox has just been proven; clear any lockout so the
	// user can sign in with the new password immediately.
	user.FailedLoginAttempts = 0
	user.LockedUntil = nil
	if err := userRepo.Update(user); err != nil {
		return 0, fmt.Errorf("failed to update password: %w", err)
	}

	// Stolen sessions must die with the old password. A revocation that fails
	// is a failure of the reset itself: the password and the claim are rolled
	// back with it rather than committed next to sessions that still work.
	if err := refreshRepo.RevokeAllForUser(user.ID); err != nil {
		return 0, fmt.Errorf("failed to revoke sessions after password reset: %w", err)
	}
	return user.ID, nil
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
