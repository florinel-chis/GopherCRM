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
	// txManager binds the password reset confirmation, the refresh token
	// rotation, the password change, the logout and the login (#336) into one
	// transaction each. The first four are refused without it rather than run
	// as separate writes; a login without it keeps the legacy unlocked path.
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
	// loginBeforeLock is an instance-local seam that runs after a login has
	// verified the password against the plain lookup and before its issuing
	// transaction begins (#336). Tests commit a competing change there; nil in
	// production.
	loginBeforeLock func(userID uint)
	// resetBeforeLock is an instance-local seam that runs after a password
	// reset request has found an active account by its plain lookup and before
	// its issuing transaction begins (#342). Tests commit a competing change
	// there; nil in production.
	resetBeforeLock func(userID uint)
	// productName is the configuration read behind the product name of the
	// password reset mail (#292), set by WithAuthProductName. Nil means
	// "GopherCRM".
	productName MailProductNameSource
	// identities refuses password reset and password sign-in to an account
	// with a linked OpenID Connect identity (#444, #457) while ssoPassword
	// does not allow them, set by WithAuthIdentities. Nil skips the check.
	identities repository.UserIdentityRepository
	// ssoPassword is security.sso.allow_password (#457), set by
	// WithAuthSSOPasswordPolicy. Nil means off: linked accounts are refused.
	ssoPassword SSOPasswordPolicy
}

// AuthServiceOption configures NewAuthServiceWithSessions.
type AuthServiceOption func(*authService)

// WithAuthProductName makes the password reset mail read its product name from
// source (general.company_name, through ResolveMailProductName) on every
// request that sends one. Without it the mail names "GopherCRM".
func WithAuthProductName(source MailProductNameSource) AuthServiceOption {
	return func(s *authService) {
		s.productName = source
	}
}

// WithAuthIdentities makes the password reset request, the reset confirm and
// the password login refuse an account that has a linked OpenID Connect
// identity (a user_identities row, #444, #457) unless the policy given by
// WithAuthSSOPasswordPolicy allows passwords for such accounts. A nil
// repository (stateless or test callers) skips the check; cmd/main.go must
// pass it.
func WithAuthIdentities(repo repository.UserIdentityRepository) AuthServiceOption {
	return func(s *authService) {
		s.identities = repo
	}
}

// WithAuthSSOPasswordPolicy makes the identity check of WithAuthIdentities
// depend on security.sso.allow_password (#457): when the policy allows it, an
// account linked to single sign-on uses password sign-in and password reset
// like a local one. A nil policy means off, so the check always applies. The
// policy is read once per call, before the transaction begins (the SQLite pool
// holds one connection). Turning the setting off does not end sessions or
// refresh tokens issued before.
func WithAuthSSOPasswordPolicy(policy SSOPasswordPolicy) AuthServiceOption {
	return func(s *authService) {
		s.ssoPassword = policy
	}
}

// checkSSOIdentity reports whether this call must refuse an account linked to
// single sign-on: identities are wired and the policy does not allow
// passwords for such accounts. Call it outside any transaction.
func (s *authService) checkSSOIdentity() bool {
	if s.identities == nil {
		return false
	}
	return s.ssoPassword == nil || !s.ssoPassword.AllowPasswordForSSOAccounts(context.Background())
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
	opts ...AuthServiceOption,
) AuthService {
	s := &authService{
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
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// recordLoginFailureInTransaction counts one failed login of userID on the
// owner row read through userRepo, which the caller binds to a locking read
// inside a transaction. The decision is taken on that row only:
//   - the row is gone (erased meanwhile): nothing is written;
//   - an active lock (a parallel attempt has just set it): nothing is written,
//     the lock is neither extended nor reset;
//   - an expired lock, including at its exact expiry instant, starts a fresh
//     window with this attempt as its first;
//   - otherwise the stored count grows by one.
//
// Reaching MaxFailedLoginAttempts locks the account for AccountLockDuration
// from now. Exactly failed_login_attempts and locked_until are written. The
// account's active flag plays no part: an inactive account's failures count.
func recordLoginFailureInTransaction(userRepo repository.UserRepository, userID uint, now time.Time) error {
	owner, err := userRepo.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	attempts := owner.FailedLoginAttempts
	if owner.LockedUntil != nil {
		if owner.LockedUntil.After(now) {
			return nil
		}
		attempts = 0
	}
	attempts++
	bookkeeping := map[string]interface{}{"failed_login_attempts": attempts, "locked_until": nil}
	if attempts >= MaxFailedLoginAttempts {
		lockUntil := now.Add(AccountLockDuration)
		bookkeeping["locked_until"] = &lockUntil
		utils.Logger.WithField("user_id", userID).WithField("operation", "Login").
			Warn("Account locked due to too many failed login attempts")
	}
	if err := userRepo.UpdateFields(userID, bookkeeping); err != nil && !apperrors.IsNotFound(err) {
		return err
	}
	return nil
}

// MaxFailedLoginAttempts is the number of failed attempts before an account is locked.
const MaxFailedLoginAttempts = 5

// AccountLockDuration is how long an account stays locked after too many failed attempts.
const AccountLockDuration = 15 * time.Minute

// loginMaxRetries is how often the login issuing transaction is replayed after
// a deadlock, a lock wait timeout or SQLite busy/locked (#183 retry rule).
const loginMaxRetries = 3

// The login errors. Their messages are part of the service contract (the
// handler answers every login error with the same 401).
var (
	errLoginInvalidCredentials = errors.New("invalid credentials")
	errLoginAccountLocked      = errors.New("account is locked due to too many failed login attempts, please try again later")
)

// Login verifies the credentials and returns an access token only; it never
// issues a refresh token.
func (s *authService) Login(email, password string) (string, error) {
	tokens, err := s.authenticate(email, password, false)
	if err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

// authenticate verifies credentials and account state and issues the session,
// maintaining the constant-time and anti-enumeration behaviour: unknown email,
// wrong password, locked account and deactivated account are
// indistinguishable to the caller.
//
// The plain email lookup and bcrypt run first, outside any transaction, so the
// timing and the failure bookkeeping are unchanged. On a service with a
// transaction manager (NewAuthServiceWithSessions) the session is then issued
// by one WithTransactionAndRetry transaction (#336) that locks the owner row by
// primary key (`SELECT ... FOR UPDATE` on MySQL/MariaDB, a plain read on
// SQLite) and decides on that row only: it must still carry the verified hash,
// be active, be live (not erased) and not be locked. The transaction writes
// last_login_at and the lockout clear, inserts the refresh token (withRefresh
// only, under a savepoint) and signs the access JWT from the locked
// token_version, in the order users → (read) user_identities →
// refresh_tokens. The user_identities read runs only with WithAuthIdentities
// while security.sso.allow_password is off (#457, read once before the
// transaction); it refuses an account linked to single sign-on with the
// generic error and writes nothing. A change committed
// between the lookup and the lock is therefore honoured, and a login that
// commits first is ended by the change that follows it. Without a transaction
// manager (NewAuthService) the legacy unlocked path runs unchanged.
func (s *authService) authenticate(email, password string, withRefresh bool) (*AuthTokens, error) {
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
		if !apperrors.IsNotFound(err) {
			return nil, authRefusal(AuthAuditReasonInternalError, 0, errLoginInvalidCredentials)
		}
		return nil, authRefusal(AuthAuditReasonInvalidCredentials, 0, errLoginInvalidCredentials)
	}

	clock := time.Now
	if s.loginTime != nil {
		clock = s.loginTime
	}
	now := clock()
	// Check if account is locked
	if user.LockedUntil != nil && user.LockedUntil.After(now) {
		logger.WithField("user_id", user.ID).Warn("Login failed - account locked")
		return nil, authRefusal(AuthAuditReasonAccountLocked, user.ID, errLoginAccountLocked)
	}

	// Check password result
	if bcryptErr != nil {
		logger.WithField("user_id", user.ID).Warn("Login failed - invalid password")
		if s.txManager != nil {
			// The failure is counted from the owner row read by primary key
			// with a locking read, in its own transaction, so parallel failed
			// attempts each count. bcrypt has already run, outside it.
			err := s.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
				tx, ok := utils.GetTxFromContext(ctx)
				if !ok {
					return utils.ErrNoTransaction
				}
				return recordLoginFailureInTransaction(
					s.userRepo.WithTx(repository.LockingRead(tx)),
					user.ID, now)
			}, loginMaxRetries)
			if err != nil {
				utils.Logger.WithField("user_id", user.ID).WithField("operation", "Login").
					WithField("error_code", "login_failure_record_failed").Warn("Recording a failed login attempt failed")
			}
			return nil, authRefusal(AuthAuditReasonInvalidCredentials, user.ID, errLoginInvalidCredentials)
		}
		// Legacy path of a service without a transaction manager
		// (NewAuthService): one statement from the plain read, unchanged.
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
		return nil, authRefusal(AuthAuditReasonInvalidCredentials, user.ID, errLoginInvalidCredentials)
	}

	// Check if account is active (after password verification to prevent account enumeration)
	if !user.IsActive {
		logger.WithField("user_id", user.ID).Warn("Login failed - account disabled")
		return nil, authRefusal(AuthAuditReasonAccountInactive, user.ID, errLoginInvalidCredentials) // Use same error message
	}

	if s.txManager != nil {
		if s.loginBeforeLock != nil {
			s.loginBeforeLock(user.ID)
		}
		// The issuing transaction. Every attempt is a fresh root transaction:
		// a deadlock, a lock wait timeout or SQLite busy rolls it back whole
		// (the bookkeeping, the refresh token and the signature with it) and
		// replays it, at most loginMaxRetries times.
		verifiedHash := user.Password
		// The single sign-on password policy is read once, here, outside the
		// transaction (#457).
		checkIdentity := s.checkSSOIdentity()
		var tokens *AuthTokens
		err := s.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
			tx, ok := utils.GetTxFromContext(ctx)
			if !ok {
				return utils.ErrNoTransaction
			}
			var identities repository.UserIdentityRepository
			if checkIdentity {
				identities = s.identities.WithTx(tx)
			}
			// A fresh Session on the locking handle, as for the other session
			// transactions: the clause must not leak into later statements.
			issued, err := s.loginInTransaction(ctx,
				s.userRepo.WithTx(repository.LockingRead(tx)),
				identities, user.ID, verifiedHash, now, withRefresh)
			if err != nil {
				return err
			}
			tokens = issued
			return nil
		}, loginMaxRetries)
		if err != nil {
			if !errors.Is(err, errLoginInvalidCredentials) && !errors.Is(err, errLoginAccountLocked) {
				utils.Logger.WithField("user_id", user.ID).WithField("operation", "Login").
					WithField("error_code", "login_transaction_failed").Warn("Login issuing transaction failed")
			}
			return nil, withAuthAuditUser(err, user.ID)
		}
		utils.Logger.WithField("user_id", user.ID).WithField("operation", "Login").
			Info("User logged in successfully")
		return tokens, nil
	}

	// Legacy path of a service without a transaction manager (NewAuthService):
	// separate statements, no owner lock, unchanged.

	// Reset failed login attempts on successful login
	if user.FailedLoginAttempts > 0 || user.LockedUntil != nil {
		if updateErr := s.userRepo.UpdateFields(user.ID, map[string]interface{}{
			"failed_login_attempts": 0, "locked_until": nil,
		}); updateErr != nil {
			if apperrors.IsNotFound(updateErr) {
				logger.WithField("user_id", user.ID).Warn("Login failed - account erased during authentication")
				return nil, authRefusal(AuthAuditReasonAccountErased, user.ID, errLoginInvalidCredentials)
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
			return nil, authRefusal(AuthAuditReasonAccountErased, user.ID, errLoginInvalidCredentials)
		}
		logger.WithError(utils.LogError(err)).Warn("Failed to update last login time")
	}

	logger.WithFields(map[string]interface{}{
		"user_id": user.ID,
		"role":    user.Role,
	}).Info("User logged in successfully")

	accessToken, err := s.GenerateJWT(user)
	if err != nil {
		return nil, err
	}
	tokens := &AuthTokens{AccessToken: accessToken, User: user}
	// Refresh tokens are issued only when the session store is wired. If
	// persisting one fails, the login still succeeds with a JWT-only session.
	if withRefresh && s.refreshTokenRepo != nil {
		session, err := newRefreshSession(models.RefreshTokenSignInPassword)
		var refreshToken string
		if err == nil {
			refreshToken, err = s.issueRefreshToken(s.refreshTokenRepo, user.ID, session)
		}
		if err != nil {
			utils.Logger.WithError(utils.LogError(err)).WithField("user_id", user.ID).
				Warn("Failed to issue refresh token at login; continuing with access token only")
		} else {
			tokens.RefreshToken = refreshToken
		}
	}
	return tokens, nil
}

// loginInTransaction issues a login session on transaction-bound
// repositories; userRepo is bound to a locking handle. Every decision is taken
// on the owner row the locking read returns, never on the plain lookup that
// preceded the transaction. A refusal returns the generic error and writes
// nothing (the transaction rolls back). A non-retryable failure of the refresh
// token insert rolls back to its savepoint and the login continues with an
// access token only; a retryable one is returned so the root attempt replays.
// identities is non-nil, bound to the transaction, when an account linked to
// single sign-on must be refused (#457); nil skips that read.
func (s *authService) loginInTransaction(
	ctx context.Context,
	userRepo repository.UserRepository,
	identities repository.UserIdentityRepository,
	userID uint, verifiedHash string, now time.Time, withRefresh bool,
) (*AuthTokens, error) {
	log := utils.Logger.WithField("user_id", userID).WithField("operation", "Login")
	refuse := func(code string, err error) (*AuthTokens, error) {
		log.WithField("error_code", code).Warn("Login refused under the owner lock")
		return nil, authRefusal(code, userID, err)
	}

	// Owner first: users before refresh_tokens, the order every session
	// transaction follows. By primary key, never through the email index.
	owner, err := userRepo.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return refuse(AuthAuditReasonAccountErased, errLoginInvalidCredentials)
		}
		return nil, fmt.Errorf("login owner lookup failed: %w", err)
	}
	// bcrypt verified the password against verifiedHash; a password change or
	// reset committed since makes that verification obsolete.
	if owner.Password != verifiedHash {
		return refuse(AuthAuditReasonPasswordChanged, errLoginInvalidCredentials)
	}
	if !owner.IsActive {
		return refuse(AuthAuditReasonAccountInactive, errLoginInvalidCredentials)
	}
	if owner.LockedUntil != nil && owner.LockedUntil.After(now) {
		return refuse(AuthAuditReasonAccountLocked, errLoginAccountLocked)
	}
	// An account linked to single sign-on, while security.sso.allow_password
	// is off (#457): a plain read of user_identities after the owner lock and
	// before any write, refused with the generic error. Nothing is written:
	// no last_login_at, no lockout change, no refresh token.
	if identities != nil {
		linked, err := identities.ExistsForUser(owner.ID)
		if err != nil {
			return nil, fmt.Errorf("login identity lookup failed: %w", err)
		}
		if linked {
			return refuse(AuthAuditReasonSSOAccount, errLoginInvalidCredentials)
		}
	}

	lastLogin := now
	fields := map[string]interface{}{"last_login_at": &lastLogin}
	if owner.FailedLoginAttempts > 0 || owner.LockedUntil != nil {
		fields["failed_login_attempts"] = 0
		fields["locked_until"] = nil
	}
	if err := userRepo.UpdateFields(owner.ID, fields); err != nil {
		if apperrors.IsNotFound(err) {
			return refuse(AuthAuditReasonAccountErased, errLoginInvalidCredentials)
		}
		return nil, fmt.Errorf("failed to record login: %w", err)
	}
	owner.LastLoginAt = &lastLogin
	owner.FailedLoginAttempts = 0
	owner.LockedUntil = nil

	tokens := &AuthTokens{User: owner}
	if withRefresh && s.refreshTokenRepo != nil {
		// The insert runs under a savepoint of the issuing transaction, so a
		// failure leaves no partial row. A retryable failure comes back tagged
		// utils.ErrNestedRetryDeferred and is returned: the enclosing root
		// attempt is replayed whole.
		var raw string
		insertErr := s.txManager.WithTransactionAndRetry(ctx, func(spCtx context.Context) error {
			spTx, ok := utils.GetTxFromContext(spCtx)
			if !ok {
				return utils.ErrNoTransaction
			}
			session, err := newRefreshSession(models.RefreshTokenSignInPassword)
			if err != nil {
				return err
			}
			issued, err := s.issueRefreshToken(s.refreshTokenRepo.WithTx(spTx), owner.ID, session)
			if err != nil {
				return err
			}
			raw = issued
			return nil
		}, 0)
		switch {
		case insertErr == nil:
			tokens.RefreshToken = raw
		case errors.Is(insertErr, utils.ErrNestedRetryDeferred),
			errors.Is(insertErr, utils.ErrTransactionPoisoned),
			ctx.Err() != nil:
			return nil, insertErr
		default:
			// The documented fallback: the refresh_token of the response is
			// optional, and refusing the login because the token table
			// hiccuped would be the worse trade.
			log.WithField("error_code", "refresh_token_insert_failed").
				Warn("Failed to issue refresh token at login; continuing with access token only")
		}
	}

	// Signed from the locked row, so the claim carries the current
	// token_version. A signing failure rolls everything back.
	accessToken, err := s.accessTokenFor(owner)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token at login: %w", err)
	}
	tokens.AccessToken = accessToken
	return tokens, nil
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

		// The session generation is checked for shape before any conversion
		// or database read; an absent claim reads as 0.
		claimedVersion, err := accessTokenVersion(claims)
		if err != nil {
			return nil, err
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
		// A logout, password change or password reset since the token was
		// signed has incremented the stored counter (#236).
		if err := checkAccessTokenVersion(claimedVersion, user.TokenVersion); err != nil {
			utils.Logger.WithField("user_id", user.ID).WithField("operation", "ValidateToken").
				Warn("Rejected access token of an ended session")
			return nil, err
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
			return nil, authRefusal(AuthAuditReasonKeyInvalid, 0, errors.New("invalid API key"))
		}
	}

	// Check if API key is active (not revoked). A stamped revoked_at is
	// final whatever is_active says.
	if !apiKey.IsActive || apiKey.RevokedAt != nil {
		return nil, authRefusal(AuthAuditReasonKeyRevoked, apiKey.UserID, errors.New("API key has been revoked"))
	}

	// Check expiration. A key stored without an expiry (older rows, or rows
	// written by an older binary) expires one year after it was created
	// (#532).
	expiresAt := models.APIKeyExpiryCap(apiKey.CreatedAt)
	if apiKey.ExpiresAt != nil {
		expiresAt = *apiKey.ExpiresAt
	}
	if expiresAt.Before(time.Now()) {
		return nil, authRefusal(AuthAuditReasonKeyExpired, apiKey.UserID, errors.New("API key expired"))
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
		return nil, authRefusal(AuthAuditReasonAccountErased, apiKey.UserID, errors.New("invalid API key"))
	}
	if !user.IsActive {
		utils.Logger.WithField("api_key_id", apiKey.ID).WithField("user_id", apiKey.UserID).
			Warn("Rejected API key of an inactive user")
		return nil, authRefusal(AuthAuditReasonAccountInactive, apiKey.UserID, errors.New("API key owner is not active"))
	}
	// Customer accounts hold no API keys (#532); a key left from before is
	// refused like any other unusable credential.
	if user.Role == models.RoleCustomer {
		utils.Logger.WithField("api_key_id", apiKey.ID).WithField("user_id", apiKey.UserID).
			Warn("Rejected API key of a customer account")
		return nil, authRefusal(AuthAuditReasonCustomerAccount, apiKey.UserID, errors.New("API key owner is a customer account"))
	}

	// Update last used timestamp (best effort - don't fail validation if this fails)
	if err := s.apiKeyRepo.UpdateLastUsed(apiKey.ID); err != nil {
		// Log error but don't fail validation
		utils.Logger.WithError(utils.LogError(err)).WithField("api_key_id", apiKey.ID).Warn("Failed to update API key last used timestamp")
	}

	return user, nil
}

// GenerateJWT signs the access token of a sign-in: a password login, a
// registration, a single sign-on session or the renewed token of a password
// change. It carries auth_time, the sign-in time (#532).
func (s *authService) GenerateJWT(user *models.User) (string, error) {
	return s.signJWT(user, true)
}

// signJWT signs an access token for user. With signIn the token carries
// auth_time = now (Unix seconds); without it (a refresh rotation) it carries
// none.
func (s *authService) signJWT(user *models.User, signIn bool) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    user.Role,
		"exp":     now.Add(time.Hour * time.Duration(s.jwtConfig.ExpiryHours)).Unix(),
		// The owner's session generation; ValidateToken refuses the token once
		// the stored counter has moved on (#236).
		tokenVersionClaim: user.TokenVersion,
	}
	if signIn {
		claims[signInTimeClaim] = now.Unix()
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.jwtConfig.Secret))
}

// signInTimeClaim is the access-token claim holding the sign-in time in Unix
// seconds (the OpenID Connect name, #532).
const signInTimeClaim = "auth_time"

// SignedInAt returns the auth_time claim of an access token. The signature is
// verified exactly as ValidateToken verifies it; the account is not read. It
// reports false when the token does not verify, or the claim is absent or not
// a number.
func (s *authService) SignedInAt(tokenString string) (time.Time, bool) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(s.jwtConfig.Secret), nil
	})
	if err != nil || !token.Valid {
		return time.Time{}, false
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return time.Time{}, false
	}
	seconds, ok := claims[signInTimeClaim].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), 0), true
}

// LoginWithTokens verifies the credentials and issues an access token plus,
// when the session store is wired, a refresh token. On a service built by
// NewAuthServiceWithSessions both come from the owner row locked inside one
// issuing transaction (see authenticate). If persisting the refresh token
// fails for a reason a replay cannot fix, the login still succeeds with a
// JWT-only session: the response contract marks refresh_token as optional.
func (s *authService) LoginWithTokens(email, password string) (*AuthTokens, error) {
	return s.authenticate(email, password, true)
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

// refreshSession is what a refresh token records about the session it
// belongs to: the family shared by every token of one rotation chain, how
// that session signed in and when.
type refreshSession struct {
	FamilyID     string
	SignInMethod string
	StartedAt    time.Time
}

// refreshFamilyIDBytes is the random length of a new family id, hex-encoded
// to 32 lowercase characters.
const refreshFamilyIDBytes = 16

// newRefreshSession starts a session for a sign-in with the given method
// (models.RefreshTokenSignInPassword or models.RefreshTokenSignInOIDC): a new
// random family id and the current time as its start.
func newRefreshSession(method string) (refreshSession, error) {
	buf := make([]byte, refreshFamilyIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return refreshSession{}, fmt.Errorf("failed to generate refresh token family: %w", err)
	}
	return refreshSession{FamilyID: hex.EncodeToString(buf), SignInMethod: method, StartedAt: time.Now()}, nil
}

// ssoSessionMaxAge is how long a session that started with single sign-on
// (sign_in_method oidc) can be refreshed, counted from session_started_at,
// the sign-in at the identity provider. It is fixed, not configurable:
// REFRESH_TOKEN_EXPIRY_DAYS never extends it. Past it the person signs in at
// the provider again, which re-applies the provider's role mapping.
const ssoSessionMaxAge = 24 * time.Hour

// ssoSessionExpired reports whether a session can no longer be refreshed
// because of the single sign-on limit: the method is oidc and the session has
// no start, or now is at or past start + ssoSessionMaxAge. Every other method
// ('password', an empty one) is never expired by this rule.
func ssoSessionExpired(signInMethod string, startedAt *time.Time, now time.Time) bool {
	if signInMethod != models.RefreshTokenSignInOIDC {
		return false
	}
	return startedAt == nil || !now.Before(startedAt.Add(ssoSessionMaxAge))
}

// issueRefreshToken mints, hashes and persists a refresh token for the user
// through the given repository — the root one at a login without a transaction
// manager, the transaction-bound one at a wired login, a rotation or a
// password change — returning the raw value for the client. The row records
// the given session: a new one at a sign-in or a password change, the
// presented token's at a rotation. Lifetime comes from
// REFRESH_TOKEN_EXPIRY_DAYS (config JWT.RefreshTokenDays); a single sign-on
// session (oidc) is capped at its start + ssoSessionMaxAge, whichever comes
// first.
func (s *authService) issueRefreshToken(repo repository.RefreshTokenRepository, userID uint, session refreshSession) (string, error) {
	raw, err := generateOpaqueToken()
	if err != nil {
		return "", err
	}

	days := s.jwtConfig.RefreshTokenDays
	if days <= 0 {
		days = 30
	}

	now := time.Now()
	expiresAt := now.Add(time.Duration(days) * 24 * time.Hour)
	if session.SignInMethod == models.RefreshTokenSignInOIDC {
		if limit := session.StartedAt.Add(ssoSessionMaxAge); limit.Before(expiresAt) {
			expiresAt = limit
		}
	}

	token := &models.RefreshToken{
		UserID:       userID,
		TokenHash:    hashOpaqueToken(raw, s.sessionTokenSecret()),
		ExpiresAt:    expiresAt,
		FamilyID:     session.FamilyID,
		SignInMethod: session.SignInMethod,
	}
	// A zero start (a presented row without one) stays NULL; the row gets
	// its own copy of the value either way.
	if !session.StartedAt.IsZero() {
		startedAt := session.StartedAt
		token.SessionStartedAt = &startedAt
	}
	if err := repo.Create(token); err != nil {
		return "", fmt.Errorf("failed to store refresh token: %w", err)
	}
	return raw, nil
}

// accessTokenFor signs the access token of a wired login, a single sign-on
// session or a password change through the instance-local seam when one is
// installed, GenerateJWT otherwise. The token carries auth_time.
func (s *authService) accessTokenFor(user *models.User) (string, error) {
	if s.signAccessToken != nil {
		return s.signAccessToken(user)
	}
	return s.GenerateJWT(user)
}

// refreshedAccessTokenFor signs the access token of a refresh rotation
// through the same seam. It carries no auth_time: a refresh is not a sign-in
// (#532).
func (s *authService) refreshedAccessTokenFor(user *models.User) (string, error) {
	if s.signAccessToken != nil {
		return s.signAccessToken(user)
	}
	return s.signJWT(user, false)
}

// errRefreshOwnerRejected marks a rotation refused because of its owner —
// inactive, erased or missing — after the presented token was already
// claimed. It is internal to RefreshAccessToken: the claim is committed as
// containment and the caller receives the generic ErrInvalidRefreshToken.
var errRefreshOwnerRejected = fmt.Errorf("refresh token owner rejected: %w", ErrInvalidRefreshToken)

// errRefreshTokenReuse marks a refresh token presented again after a
// committed rotation had already exchanged it for a successor. It is internal
// to RefreshAccessToken: the revocation of the token's whole family is
// committed and the caller receives the generic ErrInvalidRefreshToken.
var errRefreshTokenReuse = fmt.Errorf("refresh token reused: %w", ErrInvalidRefreshToken)

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
//
// A token that a committed rotation already exchanged for a successor is the
// other rejection that commits: when the live lookup misses, the revoked row
// is read in the same transaction, and when a later row of its family exists
// the owner is locked and every live token of the family is revoked. The
// public error is again the generic one; the audit reason is token_reuse with
// the owner's id. A request that loses the claim to a concurrent rotation is
// not a reuse: it passed the live lookup, so it is refused as before and ends
// nothing.
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
		tokenReused   bool
		ownerReason   string
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
			s.userRepo.WithTx(repository.LockingRead(tx)),
			hash)
		tokenOwnerID = ownerID
		if errors.Is(err, errRefreshTokenReuse) {
			// Commit the family revocation: the refusal stands, and so must
			// the end of every token of the reused session.
			tokenReused = true
			return nil
		}
		if errors.Is(err, errRefreshOwnerRejected) {
			ownerReason, _ = AuthAuditReason(err)
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
	if tokenReused {
		if err != nil {
			// The family revocation did not commit; the next presentation of
			// the token will try again. The caller still learns only that the
			// token was rejected.
			utils.Logger.WithError(utils.LogError(err)).WithField("user_id", tokenOwnerID).
				Warn("Failed to commit revocation of a reused refresh token's session")
		} else {
			utils.Logger.WithField("user_id", tokenOwnerID).
				Warn("Refresh token presented again after rotation; its session was revoked")
		}
		return nil, authRefusal(AuthAuditReasonTokenReuse, tokenOwnerID, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken))
	}
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
		return nil, authRefusal(ownerReason, tokenOwnerID, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken))
	}
	if err != nil {
		return nil, withAuthAuditUser(err, tokenOwnerID)
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
			return s.refuseRevokedRefreshToken(refreshRepo, userRepo, hash)
		}
		return nil, 0, fmt.Errorf("refresh token lookup failed: %w", err)
	}

	// Defence in depth in case a repository implementation stops filtering.
	if stored.Revoked || !stored.ExpiresAt.After(time.Now()) {
		return nil, stored.UserID, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
	}

	// A single sign-on session can be refreshed only until ssoSessionMaxAge
	// after its sign-in, whatever expiry the row carries (rows issued before
	// the cap existed carry a later one). The refusal is the plain error, so
	// the transaction rolls back and nothing is written.
	if ssoSessionExpired(stored.SignInMethod, stored.SessionStartedAt, time.Now()) {
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
	ownerRejected := ""
	if err != nil {
		if !apperrors.IsNotFound(err) {
			return nil, stored.UserID, fmt.Errorf("refresh token owner lookup failed: %w", err)
		}
		ownerRejected = AuthAuditReasonAccountErased
	} else if !user.IsActive {
		ownerRejected = AuthAuditReasonAccountInactive
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
	if ownerRejected != "" {
		return nil, stored.UserID, authRefusal(ownerRejected, stored.UserID, errRefreshOwnerRejected)
	}

	// The successor is inserted and its access token signed inside the
	// transaction, so a failure of either rolls the claim back: the presented
	// token stays usable and the caller receives no credentials at all.
	// The successor continues the presented token's session unchanged.
	session := refreshSession{FamilyID: stored.FamilyID, SignInMethod: stored.SignInMethod}
	if stored.SessionStartedAt != nil {
		session.StartedAt = *stored.SessionStartedAt
	}
	newRefreshToken, err := s.issueRefreshToken(refreshRepo, user.ID, session)
	if err != nil {
		return nil, stored.UserID, err
	}

	accessToken, err := s.refreshedAccessTokenFor(user)
	if err != nil {
		return nil, stored.UserID, fmt.Errorf("failed to sign access token during rotation: %w", err)
	}

	return &AuthTokens{AccessToken: accessToken, RefreshToken: newRefreshToken, User: user}, stored.UserID, nil
}

// refuseRevokedRefreshToken handles a presented token that is not live. It
// reads the revoked row in the rotation's transaction (on MySQL the same
// snapshot as the live lookup). An unknown token, a token without a family,
// or the newest token of its family (revoked by logout, a password change,
// deactivation or containment) is the generic refusal and writes nothing. A
// revoked token with a later row in its family was already rotated: the owner
// is locked first (users before refresh_tokens, the order of every session
// transaction), every live token of the family is revoked, and
// errRefreshTokenReuse tells the caller to commit. A missing owner does not
// stop the revocation; any other failure aborts with nothing written.
func (s *authService) refuseRevokedRefreshToken(
	refreshRepo repository.RefreshTokenRepository,
	userRepo repository.UserRepository,
	hash string,
) (*AuthTokens, uint, error) {
	rejected := fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
	stored, err := refreshRepo.GetRevokedByTokenHash(hash)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, 0, rejected
		}
		return nil, 0, fmt.Errorf("refresh token lookup failed: %w", err)
	}
	if stored.FamilyID == "" {
		return nil, 0, rejected
	}
	rotated, err := refreshRepo.HasLaterInFamily(stored)
	if err != nil {
		return nil, 0, fmt.Errorf("refresh token family lookup failed: %w", err)
	}
	if !rotated {
		return nil, 0, rejected
	}

	if _, err := userRepo.GetByID(stored.UserID); err != nil && !apperrors.IsNotFound(err) {
		return nil, stored.UserID, fmt.Errorf("refresh token owner lookup failed: %w", err)
	}
	if err := refreshRepo.RevokeFamily(stored.UserID, stored.FamilyID); err != nil {
		return nil, stored.UserID, fmt.Errorf("failed to revoke reused refresh token session: %w", err)
	}
	return nil, stored.UserID, authRefusal(AuthAuditReasonTokenReuse, stored.UserID, errRefreshTokenReuse)
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
//
// Every logout also ends the access JWTs issued to the user before it (#236):
// users.token_version is incremented by one, with or without a refresh token
// and for Bearer and API-key callers alike. The increment and the refresh
// revocation are one transaction that locks the users row (`SELECT ... FOR
// UPDATE` on MySQL) before it reads or writes any refresh_tokens row, the
// order every session transaction follows; a failure of either rolls both
// back. Logout is refused with an error when the transaction manager or the
// refresh-token repository is not wired, instead of revoking half a session.
// A user the scoped locking read cannot find (erased meanwhile) has nothing
// left to end: Logout returns nil and writes nothing.
func (s *authService) Logout(userID uint, refreshToken string) error {
	if s.refreshTokenRepo == nil || s.txManager == nil {
		return errors.New("logout requires the session store")
	}

	var presentedHash string
	if refreshToken != "" {
		presentedHash = hashOpaqueToken(refreshToken, s.sessionTokenSecret())
	}

	var ended bool
	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		ended, err = s.logoutInTransaction(
			s.userRepo.WithTx(repository.LockingRead(tx)),
			s.refreshTokenRepo.WithTx(tx),
			userID, presentedHash)
		return err
	})
	if errors.Is(err, errLogoutOwnerGone) {
		return nil
	}
	if err != nil {
		return err
	}
	if ended {
		utils.Logger.WithField("user_id", userID).WithField("operation", "Logout").
			Info("Logout ended the access tokens issued before it")
	}
	return nil
}

// logoutInTransaction performs the logout on transaction-bound repositories;
// userRepo is bound to a locking handle. It reports whether the user was found
// and the counter incremented. Any error discards every write.
func (s *authService) logoutInTransaction(
	userRepo repository.UserRepository,
	refreshRepo repository.RefreshTokenRepository,
	userID uint, presentedHash string,
) (bool, error) {
	// Owner first, before any refresh_tokens row: a rotation of the same user
	// locks the owner before its claim, so taking the rows in the other order
	// here could deadlock against it on MySQL.
	owner, err := userRepo.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("logout owner lookup failed: %w", err)
	}

	if presentedHash == "" {
		if err := refreshRepo.RevokeAllForUser(userID); err != nil {
			return false, fmt.Errorf("failed to revoke sessions: %w", err)
		}
	} else {
		stored, err := refreshRepo.GetByTokenHash(presentedHash)
		switch {
		case err != nil && !apperrors.IsNotFound(err):
			return false, fmt.Errorf("logout lookup failed: %w", err)
		case err != nil:
			// Unknown, revoked or expired: nothing to revoke.
		case stored.UserID != userID:
			utils.Logger.WithField("user_id", userID).WithField("token_owner", stored.UserID).
				Warn("Logout presented a refresh token belonging to another user; ignoring")
		default:
			if err := refreshRepo.RevokeByTokenHash(presentedHash); err != nil {
				return false, fmt.Errorf("failed to revoke session: %w", err)
			}
		}
	}

	if err := userRepo.UpdateFields(userID, map[string]interface{}{
		"token_version": owner.TokenVersion + 1,
	}); err != nil {
		if apperrors.IsNotFound(err) {
			// Erased after the locking read (only possible where the read
			// takes no lock): roll the revocation back and report nothing.
			return false, errLogoutOwnerGone
		}
		return false, fmt.Errorf("failed to end access tokens at logout: %w", err)
	}
	return true, nil
}

// errLogoutOwnerGone rolls back a logout whose user vanished between the
// owner read and the increment; Logout turns it into a nil result.
var errLogoutOwnerGone = errors.New("logout owner no longer exists")

// ChangePassword re-authenticates with the current password, applies the
// complexity policy to the new one and replaces it, ending every credential
// of the user (revokeAllCredentials) except the session being renewed.
//
// refreshToken names the caller's current browser session. When it is given,
// that session is renewed: the token is claimed and a replacement refresh
// token and access token are returned. It must be a live token of this user;
// anything else (unknown, revoked, expired, foreign, or a single sign-on
// session at or past ssoSessionMaxAge after its sign-in) is ErrSessionStale
// and nothing is written. The replacement starts a new family; a single
// sign-on session keeps its sign-in method and start, so the renewal keeps
// its 24-hour limit, while any other session becomes a password session
// starting now. When it is empty — API-key callers, scripts — the
// password is changed, every refresh token and every API key (the caller's
// own key included) is revoked and no credentials are returned (nil tokens,
// nil error).
//
// The slow bcrypt work runs first, outside any transaction. The writes are one
// transaction that locks the owner row before touching refresh tokens (the
// order every session transaction follows): the locked row must still carry
// the hash the current password was verified against, so of two concurrent
// changes only one can succeed; then the presented token is claimed, the
// password written, every credential ended by revokeAllCredentials
// (token_version incremented, every refresh token and every API key revoked)
// and the successor issued and signed. Any failure before the commit rolls
// all of it back: the password is unchanged and every session and key is as
// it was. (A failed COMMIT is the exception every transaction has: its
// outcome is ambiguous, utils.ErrCommitFailed, and the caller reports it as
// an error.) Every access token issued before the change, the caller's own
// included, is refused once it commits; the renewed access token is signed
// with the new value and keeps working (#236).
func (s *authService) ChangePassword(userID uint, currentPassword, newPassword, refreshToken string) (*AuthTokens, error) {
	if s.refreshTokenRepo == nil || s.txManager == nil || s.apiKeyRepo == nil {
		return nil, errors.New("password change requires the session store")
	}

	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, authRefusal(AuthAuditReasonAccountErased, userID, ErrAccountUnavailable)
		}
		return nil, fmt.Errorf("failed to load user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(currentPassword)); err != nil {
		utils.Logger.WithField("user_id", userID).Warn("Password change rejected - wrong current password")
		return nil, authRefusal(AuthAuditReasonInvalidCredentials, userID, ErrInvalidCurrentPassword)
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
			s.userRepo.WithTx(repository.LockingRead(tx)),
			s.refreshTokenRepo.WithTx(tx), s.apiKeyRepo.WithTx(tx),
			userID, verifiedHash, string(hashed), presentedHash)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrInvalidCurrentPassword) {
			utils.Logger.WithField("user_id", userID).Warn("Password change rejected - password changed concurrently")
		}
		return nil, err
	}

	logger := utils.Logger.WithField("user_id", userID).WithField("operation", "ChangePassword")
	if tokens != nil {
		logger.Info("Password changed; current session renewed, other sessions, earlier access tokens and API keys ended")
	} else {
		logger.Info("Password changed; all sessions, earlier access tokens and API keys ended")
	}
	return tokens, nil
}

// changePasswordInTransaction performs the password change on
// transaction-bound repositories; userRepo is bound to a locking handle. Any
// error discards every write.
func (s *authService) changePasswordInTransaction(
	userRepo repository.UserRepository,
	refreshRepo repository.RefreshTokenRepository,
	apiKeyRepo repository.APIKeyRepository,
	userID uint, verifiedHash, newHash, presentedHash string,
) (*AuthTokens, error) {
	// Owner first: SELECT ... FOR UPDATE on MySQL/MariaDB (SQLite drops the
	// clause and serialises writers instead).
	owner, err := userRepo.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, authRefusal(AuthAuditReasonAccountErased, userID, ErrAccountUnavailable)
		}
		return nil, fmt.Errorf("password change owner lookup failed: %w", err)
	}
	if !owner.IsActive {
		return nil, authRefusal(AuthAuditReasonAccountInactive, userID, ErrAccountUnavailable)
	}
	// The current password was verified against verifiedHash outside the
	// transaction. If the locked row carries another hash, a concurrent
	// change committed first and the caller's "current" password is no
	// longer current.
	if owner.Password != verifiedHash {
		return nil, authRefusal(AuthAuditReasonInvalidCredentials, userID, ErrInvalidCurrentPassword)
	}

	// renewOIDCStart is the start of the presented single sign-on session,
	// nil when the presented session is not one.
	var renewOIDCStart *time.Time
	if presentedHash != "" {
		stored, err := refreshRepo.GetByTokenHash(presentedHash)
		if err != nil {
			if apperrors.IsNotFound(err) {
				return nil, authRefusal(AuthAuditReasonSessionStale, userID, ErrSessionStale)
			}
			return nil, fmt.Errorf("password change session lookup failed: %w", err)
		}
		if stored.UserID != userID {
			return nil, authRefusal(AuthAuditReasonSessionStale, userID, ErrSessionStale)
		}
		// A single sign-on session past its limit is not live either; it is
		// refused before the claim, so nothing is written.
		if ssoSessionExpired(stored.SignInMethod, stored.SessionStartedAt, time.Now()) {
			return nil, authRefusal(AuthAuditReasonSessionStale, userID, ErrSessionStale)
		}
		if stored.SignInMethod == models.RefreshTokenSignInOIDC {
			renewOIDCStart = stored.SessionStartedAt
		}
		if err := refreshRepo.ClaimForRotation(stored.ID); err != nil {
			if apperrors.IsNotFound(err) {
				return nil, authRefusal(AuthAuditReasonSessionStale, userID, ErrSessionStale)
			}
			return nil, fmt.Errorf("failed to claim the current session: %w", err)
		}
	}

	if err := userRepo.UpdateFields(userID, map[string]interface{}{
		"password": newHash,
	}); err != nil {
		if apperrors.IsNotFound(err) {
			return nil, authRefusal(AuthAuditReasonAccountErased, userID, ErrAccountUnavailable)
		}
		return nil, fmt.Errorf("failed to update password: %w", err)
	}

	// Every credential ends with the old password: the session generation is
	// incremented, so every access JWT issued before the change — the
	// caller's own included — is refused from the commit on (#236), and every
	// refresh token and API key is revoked. The owner row is locked, so its
	// token_version is current. A revocation that fails is a failure of the
	// change itself and rolls the password back with it. The successor below
	// is issued afterwards and signed from the new generation.
	if err := revokeAllCredentials(credentialStores{
		users: userRepo, refreshTokens: refreshRepo, apiKeys: apiKeyRepo,
	}, owner, time.Now()); err != nil {
		return nil, fmt.Errorf("failed to end credentials after password change: %w", err)
	}

	if presentedHash == "" {
		return nil, nil
	}

	owner.Password = newHash
	// The successor starts a new family, as its access token does. A renewed
	// single sign-on session keeps its sign-in method and start, so its
	// expiry stays capped at that start + ssoSessionMaxAge; any other
	// session becomes a password session starting now.
	session, err := newRefreshSession(models.RefreshTokenSignInPassword)
	if err != nil {
		return nil, err
	}
	if renewOIDCStart != nil {
		session.SignInMethod = models.RefreshTokenSignInOIDC
		session.StartedAt = *renewOIDCStart
	}
	newRefreshToken, err := s.issueRefreshToken(refreshRepo, userID, session)
	if err != nil {
		return nil, err
	}
	accessToken, err := s.accessTokenFor(owner)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token during password change: %w", err)
	}
	return &AuthTokens{AccessToken: accessToken, RefreshToken: newRefreshToken, User: owner}, nil
}

// resetRequestMaxRetries is how often the password-reset issuing transaction
// is replayed after a deadlock, a lock wait timeout or SQLite busy/locked
// (#183 retry rule).
const resetRequestMaxRetries = 3

// credentialOwnerRefusal is the refusal of a credential issuance (#342) whose
// owner, read under the owner lock, is missing, erased (soft-deleted) or
// inactive. It matches ErrAccountUnavailable through errors.Is; code is the
// error_code the log line carries.
type credentialOwnerRefusal struct {
	code string
}

func (r *credentialOwnerRefusal) Error() string {
	return "credential owner unavailable: " + r.code
}

func (r *credentialOwnerRefusal) Unwrap() error { return ErrAccountUnavailable }

// lockLiveCredentialOwner reads the owner of a credential being issued by
// primary key through userRepo, which the caller binds to a locking handle of
// the issuing transaction (`SELECT ... FOR UPDATE` on MySQL/MariaDB, a plain
// read on SQLite). The scoped read makes an erased (soft-deleted) owner a
// not-found. A missing, erased or inactive owner is a *credentialOwnerRefusal;
// any other failure is returned wrapped (and may be replayed by the retry
// helper when it is retryable).
func lockLiveCredentialOwner(userRepo repository.UserRepository, userID uint) (*models.User, error) {
	owner, err := userRepo.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, &credentialOwnerRefusal{code: AuthAuditReasonAccountErased}
		}
		return nil, fmt.Errorf("credential owner lookup failed: %w", err)
	}
	if !owner.IsActive {
		return nil, &credentialOwnerRefusal{code: AuthAuditReasonAccountInactive}
	}
	return owner, nil
}

// RequestPasswordReset starts the reset flow. It NEVER discloses whether the
// account exists: unknown email, inactive account, an owner erased or
// deactivated under the lock, a rollback, an unwired service and a mail
// failure all return nil, so the endpoint's response stays uniform. The raw
// token goes only into the mailed link; the store holds its hash.
//
// The plain email lookup runs first, outside any transaction. The token is then
// issued by one WithTransactionAndRetry transaction (#342,
// resetRequestMaxRetries) that locks the owner row by primary key (`SELECT ...
// FOR UPDATE` on MySQL/MariaDB, a plain read on SQLite) and refuses, writing
// nothing, when that row is missing, erased or inactive. With
// WithAuthIdentities, and while security.sso.allow_password is off (#457, read
// once before the transaction), it then refuses the same way an account that
// has a linked OpenID Connect identity (#444, a plain read of
// user_identities). With the setting on that read is skipped and a linked
// account is treated like a local one. Otherwise the user's
// outstanding tokens are spent and the new one inserted through the
// transaction-bound PasswordResetTokenRepository, in the order users →
// (read) user_identities → password_reset_tokens. The mail goes out once, after the commit, to the
// address of the locked row. A deactivation or erasure that commits before the
// lock therefore issues nothing, and one that commits after it ends the token
// it issued (the erasure deletes it, the confirmation refuses an inactive
// owner). Without a transaction manager nothing is written or sent.
//
// RequestPasswordReset is IssuePasswordReset with its result dropped.
func (s *authService) RequestPasswordReset(email string) error {
	_, _ = s.IssuePasswordReset(email)
	return nil
}

// errPasswordResetNotIssued is the refusal IssuePasswordReset reports when no
// reset link was stored; its authRefusal carries the audit reason.
var errPasswordResetNotIssued = errors.New("password reset not issued")

// IssuePasswordReset is the reset flow RequestPasswordReset describes, with
// its result (#550): the owner's id and nil once the token transaction has
// committed, whether or not the mail was delivered; otherwise an authRefusal
// around errPasswordResetNotIssued with the audit reason (unavailable,
// account_unknown, account_inactive, account_erased, sso_account or
// internal_error) and the account's id when it is known, 0 otherwise. Every
// outcome is logged here as before; the caller answers the same way whatever
// it returns.
func (s *authService) IssuePasswordReset(email string) (uint, error) {
	logger := utils.Logger.WithField("operation", "RequestPasswordReset")

	if s.resetTokenRepo == nil || s.mailer == nil || s.txManager == nil {
		logger.WithField("error_code", "reset_unwired").Warn("Password reset requested but the reset flow is not wired")
		return 0, authRefusal(AuthAuditReasonUnavailable, 0, errPasswordResetNotIssued)
	}

	user, err := s.userRepo.GetByEmail(email)
	if err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithField("error_code", "unknown_account").Info("Password reset requested for unknown email")
			return 0, authRefusal(AuthAuditReasonAccountUnknown, 0, errPasswordResetNotIssued)
		}
		logger.WithError(utils.LogError(err)).WithField("error_code", "lookup_failed").
			Error("Password reset user lookup failed")
		return 0, authRefusal(AuthAuditReasonInternalError, 0, errPasswordResetNotIssued)
	}
	logger = logger.WithField("user_id", user.ID)
	if !user.IsActive {
		logger.WithField("error_code", "account_inactive").Info("Password reset requested for inactive account; ignored")
		return user.ID, authRefusal(AuthAuditReasonAccountInactive, user.ID, errPasswordResetNotIssued)
	}

	raw, err := generateOpaqueToken()
	if err != nil {
		logger.WithError(utils.LogError(err)).WithField("error_code", "token_generation_failed").
			Error("Failed to generate reset token")
		return user.ID, authRefusal(AuthAuditReasonInternalError, user.ID, errPasswordResetNotIssued)
	}
	tokenHash := hashOpaqueToken(raw, s.sessionTokenSecret())

	if s.resetBeforeLock != nil {
		s.resetBeforeLock(user.ID)
	}

	// The single sign-on password policy is read once, outside the
	// transaction (#457).
	checkIdentity := s.checkSSOIdentity()

	var recipient string
	err = s.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		// Owner first, by primary key, never through the email index; a fresh
		// Session so the locking clause does not leak into the token writes.
		owner, err := lockLiveCredentialOwner(
			s.userRepo.WithTx(repository.LockingRead(tx)),
			user.ID)
		if err != nil {
			return err
		}
		// An account linked to single sign-on, while
		// security.sso.allow_password is off (#444, #457): a plain read of
		// user_identities after the owner lock, refused like an unavailable
		// owner, nothing written, no mail.
		if checkIdentity {
			linked, err := s.identities.WithTx(tx).ExistsForUser(owner.ID)
			if err != nil {
				return fmt.Errorf("identity lookup failed: %w", err)
			}
			if linked {
				return &credentialOwnerRefusal{code: AuthAuditReasonSSOAccount}
			}
		}
		resetRepo := s.resetTokenRepo.WithTx(tx)
		// Only the newest link should work; stale outstanding tokens are spent
		// in the same transaction, so a failure keeps them as they were.
		if err := resetRepo.InvalidateAllForUser(owner.ID); err != nil {
			return fmt.Errorf("failed to invalidate previous reset tokens: %w", err)
		}
		token := &models.PasswordResetToken{
			UserID:    owner.ID,
			TokenHash: tokenHash,
			ExpiresAt: time.Now().Add(resetTokenTTL),
		}
		if err := resetRepo.Create(token); err != nil {
			return fmt.Errorf("failed to store reset token: %w", err)
		}
		recipient = owner.Email
		return nil
	}, resetRequestMaxRetries)
	if err != nil {
		var refusal *credentialOwnerRefusal
		if errors.As(err, &refusal) {
			logger.WithField("error_code", refusal.code).Info("Password reset refused under the owner lock")
			return user.ID, authRefusal(refusal.code, user.ID, errPasswordResetNotIssued)
		}
		logger.WithError(utils.LogError(err)).WithField("error_code", "reset_token_rolled_back").
			Error("Password reset issuing transaction rolled back")
		return user.ID, authRefusal(AuthAuditReasonInternalError, user.ID, errPasswordResetNotIssued)
	}

	resetURL := s.appBaseURL + "/reset-password?token=" + url.QueryEscape(raw)
	// The product name is read only now that a mail will go out; a failed read
	// falls back to "GopherCRM" and never blocks the send (#292).
	productName := ResolveMailProductName(s.productName)
	if err := s.mailer.SendPasswordReset(recipient, resetURL, productName); err != nil {
		// Already logged (redacted) by the mailer; swallow to stay uniform.
		logger.WithField("error_code", "mail_failed").Warn("Password reset mail delivery failed")
	}
	return user.ID, nil
}

// resetConfirmMaxRetries is how often the password-reset confirming
// transaction is replayed after a deadlock, a lock wait timeout or SQLite
// busy/locked (#183 retry rule, #407).
const resetConfirmMaxRetries = 3

// ConfirmPasswordReset spends a reset token and sets the new password. All
// token rejections map to ErrInvalidResetToken; a password that fails the
// complexity policy or cannot be hashed is reported as such.
//
// The new password is validated and hashed before anything is written, so a
// weak or unhashable password never spends the token: the user retries with
// the same link. The token lookup, its conditional claim, the owner check, the
// password and lockout update and the end of every credential
// (revokeAllCredentials: token_version incremented, every refresh token and
// every API key revoked) are then one transaction. The claim stamps the token only while it is still
// unused and strictly unexpired and must affect exactly one row, so two
// requests that both read the token as spendable cannot both succeed: the
// second claim finds nothing to stamp and is rejected. A failure after the
// claim — the password update, the credential revocation — rolls the whole unit
// back, token included, so the link stays usable for a retry instead of being
// spent on a reset that never happened. An inactive, erased or missing owner
// is rejected inside the transaction and leaves the token unspent as well; the
// owner is read with a locking read so that decision rests on the current
// committed account, not on the transaction's snapshot. With
// WithAuthIdentities, while security.sso.allow_password is off (#457, read
// once before the transaction), an owner that has a linked OpenID Connect
// identity (#444) is refused the same way, after the owner check and before
// any write: the token stays unspent and the answer is ErrInvalidResetToken.
// With the setting on the identity read is skipped.
//
// The transaction claims the token before it locks the owner, the reverse of
// the order an erasure and a reset request take, so it can lose a deadlock or
// a lock wait against either. It is therefore replayed up to
// resetConfirmMaxRetries times (#407). Every attempt is a fresh root
// transaction that redoes the lookup, the claim, the owner lock and the
// writes on current committed rows: a replay that finds the token gone or
// spent, or its owner erased, ends in ErrInvalidResetToken like any other
// refusal. When the bound is exhausted the last error is returned as it is
// and nothing is written; the link is still valid then.
//
// ConfirmPasswordReset is CompletePasswordReset without the owner's id.
func (s *authService) ConfirmPasswordReset(token, newPassword string) error {
	_, err := s.CompletePasswordReset(token, newPassword)
	return err
}

// CompletePasswordReset is the confirmation ConfirmPasswordReset describes
// and returns the owner's id on success (#550). Each refusal still matches
// ErrInvalidResetToken with the same message, and carries the audit reason
// (unavailable, token_invalid, account_erased, account_inactive or
// sso_account) and the owner's id once a stored token was found: an unknown,
// spent or expired token is a lookup miss and names no account.
func (s *authService) CompletePasswordReset(token, newPassword string) (uint, error) {
	// Every dependency of the transactional flow is required. Running the old
	// sequence of separate writes when one is missing would reintroduce the
	// burnt-link and double-spend failures this flow exists to prevent.
	if s.resetTokenRepo == nil || s.refreshTokenRepo == nil || s.apiKeyRepo == nil || s.txManager == nil {
		return 0, authRefusal(AuthAuditReasonUnavailable, 0, fmt.Errorf("reset not available: %w", ErrInvalidResetToken))
	}
	if token == "" {
		return 0, authRefusal(AuthAuditReasonTokenInvalid, 0, fmt.Errorf("reset not available: %w", ErrInvalidResetToken))
	}

	// A weak password must NOT spend the token: the user retries with the
	// same link instead of having to request a new one.
	if err := utils.ValidatePasswordComplexity(newPassword); err != nil {
		return 0, err
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt refuses inputs over 72 bytes; nothing has been written.
		return 0, fmt.Errorf("failed to hash password: %w", err)
	}

	hash := hashOpaqueToken(token, s.sessionTokenSecret())

	// The single sign-on password policy is read once, outside the
	// transaction (#457).
	checkIdentity := s.checkSSOIdentity()

	var userID uint
	err = s.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		var identities repository.UserIdentityRepository
		if checkIdentity {
			identities = s.identities.WithTx(tx)
		}
		// The owner repository is bound to a locking handle so its read is
		// `SELECT ... FOR UPDATE` on MySQL (SQLite drops the clause). The
		// handle must be a fresh Session: Clauses alone returns a statement
		// that GORM keeps reusing, so the owner lookup would leave its table
		// and conditions on it and the following Save would then build an
		// invalid query (SQLite reports an ambiguous `users.id`).
		userID, err = s.confirmPasswordResetInTransaction(
			s.resetTokenRepo.WithTx(tx), s.userRepo.WithTx(repository.LockingRead(tx)), s.refreshTokenRepo.WithTx(tx),
			s.apiKeyRepo.WithTx(tx), identities, hash, string(hashed))
		return err
	}, resetConfirmMaxRetries)
	if err != nil {
		return 0, err
	}

	utils.Logger.WithField("user_id", userID).WithField("operation", "ConfirmPasswordReset").
		Info("Password reset completed; all sessions, earlier access tokens and API keys ended")
	return userID, nil
}

// confirmPasswordResetInTransaction performs the reset on transaction-bound
// repositories and returns the owner's id. Any error discards every write,
// the token claim included. identities is nil when the service was built
// without WithAuthIdentities or security.sso.allow_password is on; otherwise
// an owner with a linked identity is refused (#444, #457).
func (s *authService) confirmPasswordResetInTransaction(
	resetRepo repository.PasswordResetTokenRepository,
	userRepo repository.UserRepository,
	refreshRepo repository.RefreshTokenRepository,
	apiKeyRepo repository.APIKeyRepository,
	identities repository.UserIdentityRepository,
	hash, passwordHash string,
) (uint, error) {
	// The lookup already excludes used and expired tokens; the claim below
	// re-applies both predicates as part of its own UPDATE.
	stored, err := resetRepo.GetByTokenHash(hash)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return 0, authRefusal(AuthAuditReasonTokenInvalid, 0, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken))
		}
		return 0, fmt.Errorf("reset token lookup failed: %w", err)
	}

	// Defence in depth in case a repository implementation stops filtering.
	if stored.UsedAt != nil || !stored.ExpiresAt.After(time.Now()) {
		return 0, authRefusal(AuthAuditReasonTokenInvalid, stored.UserID, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken))
	}

	// Claim the token before reading anything else. The claim is the one
	// write competing requests contend on: whoever stamps the row first owns
	// the reset, and every other request finds no row to stamp — even one
	// that passed the lookup a moment ago, or one whose token expired between
	// the lookup and this statement.
	if err := resetRepo.MarkUsed(stored.ID); err != nil {
		if apperrors.IsNotFound(err) {
			return 0, authRefusal(AuthAuditReasonTokenInvalid, stored.UserID, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken))
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
			return 0, authRefusal(AuthAuditReasonAccountErased, stored.UserID, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken))
		}
		return 0, fmt.Errorf("reset token owner lookup failed: %w", err)
	}
	if !user.IsActive {
		return 0, authRefusal(AuthAuditReasonAccountInactive, stored.UserID, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken))
	}
	// An account linked to single sign-on, while security.sso.allow_password
	// is off (#444, #457); a plain read after the owner lock. The rollback
	// hands the claim back, as for the other owner refusals.
	if identities != nil {
		linked, err := identities.ExistsForUser(user.ID)
		if err != nil {
			return 0, fmt.Errorf("reset token owner identity lookup failed: %w", err)
		}
		if linked {
			return 0, authRefusal(AuthAuditReasonSSOAccount, stored.UserID, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken))
		}
	}

	// Ownership of the mailbox has just been proven; clear any lockout so the
	// user can sign in with the new password immediately. Only these columns
	// are written, and only while the row is live.
	if err := userRepo.UpdateFields(user.ID, map[string]interface{}{
		"password":              passwordHash,
		"failed_login_attempts": 0,
		"locked_until":          nil,
	}); err != nil {
		return 0, fmt.Errorf("failed to update password: %w", err)
	}

	// Every credential ends with the old password: the session generation is
	// incremented, which ends every access JWT issued before the reset
	// (#236), and every refresh token and API key is revoked; the row is
	// locked, so the value read above is current. A revocation that fails is
	// a failure of the reset itself: the password and the claim are rolled
	// back with it.
	if err := revokeAllCredentials(credentialStores{
		users: userRepo, refreshTokens: refreshRepo, apiKeys: apiKeyRepo,
	}, user, time.Now()); err != nil {
		return 0, fmt.Errorf("failed to end credentials after password reset: %w", err)
	}
	return user.ID, nil
}
