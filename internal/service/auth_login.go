package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"golang.org/x/crypto/bcrypt"
)

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

// LoginWithTokens verifies the credentials and issues an access token plus,
// when the session store is wired, a refresh token. On a service built by
// NewAuthServiceWithSessions both come from the owner row locked inside one
// issuing transaction (see authenticate). If persisting the refresh token
// fails for a reason a replay cannot fix, the login still succeeds with a
// JWT-only session: the response contract marks refresh_token as optional.
func (s *authService) LoginWithTokens(email, password string) (*AuthTokens, error) {
	return s.authenticate(email, password, true)
}
