package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
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
