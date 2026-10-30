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
	"gorm.io/gorm"
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
	// ErrSessionStale rejects a password change whose presented refresh token
	// is not a live session of the caller (unknown, revoked, expired or
	// foreign). Nothing is written.
	ErrSessionStale = errors.New("the session to renew is no longer current")
	// ErrAccountUnavailable rejects a password change for an account that was
	// erased or deactivated after the request authenticated.
	ErrAccountUnavailable = errors.New("account is not available")
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
	// txManager binds the password reset confirmation and the refresh token
	// rotation into one transaction each: token claim, owner check and the
	// dependent writes. It is required for both flows; without it they are
	// refused rather than run as separate writes.
	txManager    repository.TransactionManager
	mailer       mailer.Mailer
	jwtConfig    config.JWTConfig
	apiKeySecret string
	appBaseURL   string
	// loginTime is an instance-local clock for lockout decisions; nil uses wall time.
	loginTime func() time.Time
	// signAccessToken is an instance-local seam for the access token issued
	// by a rotation; nil means GenerateJWT. Tests inject a failing signer on
	// one instance to prove the rollback, instead of mutating the global
	// signing methods under concurrent callers.
	signAccessToken func(user *models.User) (string, error)
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
// token, writes the password and revokes the sessions, and the rotation claims
// the presented token and inserts its successor, through the transaction-bound
// repositories it yields. NewAuthService (above) remains for callers that only
// need stateless JWT auth — on such an instance the session methods reject
// every token with the generic sentinels instead of panicking.
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
		attempts := user.FailedLoginAttempts
		if user.LockedUntil != nil {
			attempts = 0
		}
		attempts++
		bookkeeping := map[string]interface{}{"failed_login_attempts": attempts, "locked_until": nil}
		if attempts >= MaxFailedLoginAttempts {
			lockUntil := now.Add(AccountLockDuration)
			bookkeeping["locked_until"] = &lockUntil
			logger.WithField("user_id", user.ID).Warn("Account locked due to too many failed login attempts")
		}
		// Only the bookkeeping columns are written, and only while the row is
		// live: a full save of the struct read above would write a stale
		// password hash back, or resurrect an account erased meanwhile.
		if updateErr := s.userRepo.UpdateFields(user.ID, bookkeeping); updateErr != nil {
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
		if updateErr := s.userRepo.UpdateFields(user.ID, map[string]interface{}{
			"failed_login_attempts": 0, "locked_until": nil,
		}); updateErr != nil {
			if apperrors.IsNotFound(updateErr) {
				logger.WithField("user_id", user.ID).Warn("Login failed - account erased during authentication")
				return nil, errors.New("invalid credentials")
			}
			logger.WithError(utils.LogError(updateErr)).Warn("Failed to reset failed login attempts")
		}
		user.FailedLoginAttempts = 0
		user.LockedUntil = nil
	}

	// Update last login timestamp. The write is conditional on a live row, so
	// a not-found here means the account was erased after it was read: the
	// login is refused rather than answered for an account that is gone.
	if err := s.userRepo.UpdateLastLogin(user.ID); err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithField("user_id", user.ID).Warn("Login failed - account erased during authentication")
			return nil, errors.New("invalid credentials")
		}
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
		refreshToken, err := s.issueRefreshToken(s.refreshTokenRepo, user.ID)
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

// issueRefreshToken mints, hashes and persists a refresh token for the user
// through the given repository — the root one at login, the transaction-bound
// one during a rotation — returning the raw value for the client. Lifetime
// comes from REFRESH_TOKEN_EXPIRY_DAYS (config JWT.RefreshTokenDays).
func (s *authService) issueRefreshToken(repo repository.RefreshTokenRepository, userID uint) (string, error) {
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
	if err := repo.Create(token); err != nil {
		return "", fmt.Errorf("failed to store refresh token: %w", err)
	}
	return raw, nil
}

// accessTokenFor signs the access token of a rotation through the
// instance-local seam when one is installed, GenerateJWT otherwise.
func (s *authService) accessTokenFor(user *models.User) (string, error) {
	if s.signAccessToken != nil {
		return s.signAccessToken(user)
	}
	return s.GenerateJWT(user)
}

// errRefreshOwnerRejected marks a rotation refused because of its owner —
// inactive, erased or missing — after the presented token was already
// claimed. It is internal to RefreshAccessToken: the claim is committed as
// containment and the caller receives the generic ErrInvalidRefreshToken.
var errRefreshOwnerRejected = fmt.Errorf("refresh token owner rejected: %w", ErrInvalidRefreshToken)

// RefreshAccessToken validates the presented refresh token and rotates it:
// the presented token is revoked and a fresh JWT + refresh token are issued.
// Every rejection maps to ErrInvalidRefreshToken so callers cannot learn
// whether the token was unknown, revoked, expired, or orphaned.
//
// The rotation is one transaction. The presented token is claimed by a
// conditional update that revokes it only while it is still not revoked and
// strictly unexpired and must affect exactly one row, so two requests that
// both read the token as live cannot both rotate it: the second claim finds
// nothing to revoke and is rejected before any successor exists. The owner is
// read with a locking read before the claim (users row before refresh_tokens
// rows, the order every session transaction follows), and the successor is
// inserted and its access token signed, all inside the same transaction. A failure of the successor
// insert or of the signing rolls the claim back with it, so the presented
// token stays usable and no credentials are returned; nothing is left half
// rotated. An inactive, erased or missing owner is the one rejection that
// commits: the claim stands as the containment revocation of a token whose
// account is dead, no successor is inserted, and the public error is the same
// generic one.
func (s *authService) RefreshAccessToken(refreshToken string) (*AuthTokens, error) {
	// Every dependency of the transactional flow is required. Running the old
	// sequence of independent writes when one is missing would reintroduce
	// the double-successor and stranded-session failures this flow prevents.
	if s.refreshTokenRepo == nil || s.txManager == nil || refreshToken == "" {
		return nil, fmt.Errorf("refresh not available: %w", ErrInvalidRefreshToken)
	}

	hash := hashOpaqueToken(refreshToken, s.sessionTokenSecret())

	var (
		tokens        *AuthTokens
		ownerRejected bool
		tokenOwnerID  uint
	)
	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		// The owner repository is bound to a locking handle so its read is
		// `SELECT ... FOR UPDATE` on MySQL (SQLite drops the clause). The
		// handle must be a fresh Session: Clauses alone returns a statement
		// that GORM keeps reusing, and its state would leak into the
		// successor insert that follows on the same transaction.
		rotated, ownerID, err := s.rotateRefreshTokenInTransaction(
			s.refreshTokenRepo.WithTx(tx),
			s.userRepo.WithTx(tx.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})),
			hash)
		tokenOwnerID = ownerID
		if errors.Is(err, errRefreshOwnerRejected) {
			// Commit the containment: the claim has revoked a token whose
			// owner is gone or disabled, and that revocation must survive
			// the rejection rather than be rolled back with it.
			ownerRejected = true
			return nil
		}
		if err != nil {
			return err
		}
		tokens = rotated
		return nil
	})
	if ownerRejected {
		if err != nil {
			// The containment did not commit; the token is still live and the
			// next presentation will try again. The caller still learns only
			// that the token was rejected.
			utils.Logger.WithError(utils.LogError(err)).WithField("user_id", tokenOwnerID).
				Warn("Failed to commit revocation of refresh token with inactive or missing owner")
		}
		utils.Logger.WithField("user_id", tokenOwnerID).
			Warn("Rejected refresh token whose owner is missing or inactive")
		return nil, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
	}
	if err != nil {
		return nil, err
	}

	utils.Logger.WithField("user_id", tokens.User.ID).Info("Refresh token rotated")
	return tokens, nil
}

// rotateRefreshTokenInTransaction performs the rotation on transaction-bound
// repositories: lookup, locking owner read, conditional claim, owner verdict,
// successor insert and access token signing. It returns the owner id it learned from
// the token, if any, so the caller can log a rejection. Any error other than
// errRefreshOwnerRejected discards every write, the claim included.
func (s *authService) rotateRefreshTokenInTransaction(
	refreshRepo repository.RefreshTokenRepository,
	userRepo repository.UserRepository,
	hash string,
) (*AuthTokens, uint, error) {
	// The lookup already excludes revoked and expired tokens; the claim below
	// re-applies both predicates as part of its own UPDATE.
	stored, err := refreshRepo.GetByTokenHash(hash)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, 0, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
		}
		return nil, 0, fmt.Errorf("refresh token lookup failed: %w", err)
	}

	// Defence in depth in case a repository implementation stops filtering.
	if stored.Revoked || !stored.ExpiresAt.After(time.Now()) {
		return nil, stored.UserID, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
	}

	// Lock the owner before claiming the token. Every transaction that
	// writes both an account and its refresh tokens takes the users row
	// first and the refresh_tokens rows second (the password change locks the
	// owner and then revokes every token; the reset does the same); claiming
	// first here would let a rotation of one token and a password change of
	// its owner each hold the lock the other waits for. The locking read also
	// returns the current committed row: on MySQL a plain SELECT inside this
	// REPEATABLE READ transaction would answer from the snapshot taken at the
	// token lookup, so an account deactivated or erased a moment after that
	// lookup would still look active and receive a fresh session. The scoped
	// lookup makes an erased (soft-deleted) owner a not-found; a deactivated
	// one is refused the same way. A lookup failure aborts before anything is
	// written, leaving the token usable once the database recovers.
	user, err := userRepo.GetByID(stored.UserID)
	ownerRejected := false
	if err != nil {
		if !apperrors.IsNotFound(err) {
			return nil, stored.UserID, fmt.Errorf("refresh token owner lookup failed: %w", err)
		}
		ownerRejected = true
	} else if !user.IsActive {
		ownerRejected = true
	}

	// Claim the token. The claim is the one write competing requests contend
	// on: whoever revokes the row first owns the rotation, and every other
	// request finds no row to revoke — even one that passed the lookup a
	// moment ago, or one whose token expired or was revoked between the
	// lookup and this statement.
	if err := refreshRepo.ClaimForRotation(stored.ID); err != nil {
		if apperrors.IsNotFound(err) {
			return nil, stored.UserID, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
		}
		return nil, stored.UserID, fmt.Errorf("failed to claim refresh token for rotation: %w", err)
	}

	// A token must never outlive or outrank its owner. A missing, erased or
	// inactive owner is reported as errRefreshOwnerRejected so the caller
	// commits the claim as containment and issues nothing.
	if ownerRejected {
		return nil, stored.UserID, errRefreshOwnerRejected
	}

	// The successor is inserted and its access token signed inside the
	// transaction, so a failure of either rolls the claim back: the presented
	// token stays usable and the caller receives no credentials at all.
	newRefreshToken, err := s.issueRefreshToken(refreshRepo, user.ID)
	if err != nil {
		return nil, stored.UserID, err
	}

	accessToken, err := s.accessTokenFor(user)
	if err != nil {
		return nil, stored.UserID, fmt.Errorf("failed to sign access token during rotation: %w", err)
	}

	return &AuthTokens{AccessToken: accessToken, RefreshToken: newRefreshToken, User: user}, stored.UserID, nil
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
// complexity policy to the new one and replaces it, ending every refresh
// session of the user except the one being renewed.
//
// refreshToken names the caller's current browser session. When it is given,
// that session is renewed: the token is claimed and a replacement refresh
// token and access token are returned. It must be a live token of this user;
// anything else (unknown, revoked, expired, foreign) is ErrSessionStale and
// nothing is written. When it is empty — API-key callers, scripts — the
// password is changed, every refresh token is revoked and no credentials are
// returned (nil tokens, nil error).
//
// The slow bcrypt work runs first, outside any transaction. The writes are one
// transaction that locks the owner row before touching refresh tokens (the
// order every session transaction follows): the locked row must still carry
// the hash the current password was verified against, so of two concurrent
// changes only one can succeed; then the presented token is claimed, the
// password written, every other token revoked and the successor issued and
// signed. Any failure rolls all of it back: the password is unchanged and
// every session is as it was. Access tokens already issued elsewhere stay
// valid until they expire — there is no blocklist — but can no longer renew.
func (s *authService) ChangePassword(userID uint, currentPassword, newPassword, refreshToken string) (*AuthTokens, error) {
	if s.refreshTokenRepo == nil || s.txManager == nil {
		return nil, errors.New("password change requires the session store")
	}

	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, ErrAccountUnavailable
		}
		return nil, fmt.Errorf("failed to load user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(currentPassword)); err != nil {
		utils.Logger.WithField("user_id", userID).Warn("Password change rejected - wrong current password")
		return nil, ErrInvalidCurrentPassword
	}
	if err := utils.ValidatePasswordComplexity(newPassword); err != nil {
		return nil, err
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}
	verifiedHash := user.Password

	var presentedHash string
	if refreshToken != "" {
		presentedHash = hashOpaqueToken(refreshToken, s.sessionTokenSecret())
	}

	var tokens *AuthTokens
	err = s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		tokens, err = s.changePasswordInTransaction(
			s.userRepo.WithTx(tx.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})),
			s.refreshTokenRepo.WithTx(tx),
			userID, verifiedHash, string(hashed), presentedHash)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrInvalidCurrentPassword) {
			utils.Logger.WithField("user_id", userID).Warn("Password change rejected - password changed concurrently")
		}
		return nil, err
	}

	if tokens != nil {
		utils.Logger.WithField("user_id", userID).Info("Password changed; current session renewed, other sessions revoked")
	} else {
		utils.Logger.WithField("user_id", userID).Info("Password changed; all sessions revoked")
	}
	return tokens, nil
}

// changePasswordInTransaction performs the password change on
// transaction-bound repositories; userRepo is bound to a locking handle. Any
// error discards every write.
func (s *authService) changePasswordInTransaction(
	userRepo repository.UserRepository,
	refreshRepo repository.RefreshTokenRepository,
	userID uint, verifiedHash, newHash, presentedHash string,
) (*AuthTokens, error) {
	// Owner first: SELECT ... FOR UPDATE on MySQL/MariaDB (SQLite drops the
	// clause and serialises writers instead).
	owner, err := userRepo.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, ErrAccountUnavailable
		}
		return nil, fmt.Errorf("password change owner lookup failed: %w", err)
	}
	if !owner.IsActive {
		return nil, ErrAccountUnavailable
	}
	// The current password was verified against verifiedHash outside the
	// transaction. If the locked row carries another hash, a concurrent
	// change committed first and the caller's "current" password is no
	// longer current.
	if owner.Password != verifiedHash {
		return nil, ErrInvalidCurrentPassword
	}

	if presentedHash != "" {
		stored, err := refreshRepo.GetByTokenHash(presentedHash)
		if err != nil {
			if apperrors.IsNotFound(err) {
				return nil, ErrSessionStale
			}
			return nil, fmt.Errorf("password change session lookup failed: %w", err)
		}
		if stored.UserID != userID {
			return nil, ErrSessionStale
		}
		if err := refreshRepo.ClaimForRotation(stored.ID); err != nil {
			if apperrors.IsNotFound(err) {
				return nil, ErrSessionStale
			}
			return nil, fmt.Errorf("failed to claim the current session: %w", err)
		}
	}

	if err := userRepo.UpdateFields(userID, map[string]interface{}{"password": newHash}); err != nil {
		if apperrors.IsNotFound(err) {
			return nil, ErrAccountUnavailable
		}
		return nil, fmt.Errorf("failed to update password: %w", err)
	}

	// Stolen sessions must die with the old password. A revocation that fails
	// is a failure of the change itself and rolls the password back with it.
	if err := refreshRepo.RevokeAllForUser(userID); err != nil {
		return nil, fmt.Errorf("failed to revoke sessions after password change: %w", err)
	}

	if presentedHash == "" {
		return nil, nil
	}

	owner.Password = newHash
	newRefreshToken, err := s.issueRefreshToken(refreshRepo, userID)
	if err != nil {
		return nil, err
	}
	accessToken, err := s.accessTokenFor(owner)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token during password change: %w", err)
	}
	return &AuthTokens{AccessToken: accessToken, RefreshToken: newRefreshToken, User: owner}, nil
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
		// The owner repository is bound to a locking handle so its read is
		// `SELECT ... FOR UPDATE` on MySQL (SQLite drops the clause). The
		// handle must be a fresh Session: Clauses alone returns a statement
		// that GORM keeps reusing, so the owner lookup would leave its table
		// and conditions on it and the following Save would then build an
		// invalid query (SQLite reports an ambiguous `users.id`).
		userID, err = s.confirmPasswordResetInTransaction(
			s.resetTokenRepo.WithTx(tx), s.userRepo.WithTx(tx.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})), s.refreshTokenRepo.WithTx(tx),
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

	// Ownership of the mailbox has just been proven; clear any lockout so the
	// user can sign in with the new password immediately. Only these columns
	// are written, and only while the row is live.
	if err := userRepo.UpdateFields(user.ID, map[string]interface{}{
		"password": passwordHash, "failed_login_attempts": 0, "locked_until": nil,
	}); err != nil {
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
