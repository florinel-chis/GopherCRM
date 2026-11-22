package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/idkit"
	idkitoidc "github.com/florinel-chis/gophercrm/internal/idkit/oidc"
	"github.com/florinel-chis/gophercrm/internal/idkit/rolemap"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// GopherCRM's implementations of the host interfaces declared in
// internal/idkit/host.go, over the existing repositories, the auth service,
// the sign-in audit service, the single sign-on settings service, the
// transaction manager and the logger. Nothing wires them in yet.
//
// Every adapter decides whether to join, refuse or run on the root handle
// with the same test, utils.InTransaction(ctx), and only then takes the
// transaction handle from ctx. The identity store and the session issuer
// join the transaction; the attempt store, the audit sink and the
// configuration source write or read on the root handle and refuse a ctx
// that carries one.

var _ idkit.IdentityStore = (*idkitIdentityStore)(nil)
var _ idkit.AttemptStore = (*idkitAttemptStore)(nil)
var _ idkit.SessionIssuer = (*idkitSessionIssuer)(nil)
var _ idkit.AuditSink = (*idkitAuditSink)(nil)
var _ idkit.ConfigSource = (*idkitConfigSource)(nil)
var _ idkit.Logger = (*idkitLogger)(nil)
var _ idkit.TxRunner = (*utils.TransactionManager)(nil)
var _ idkit.Clock = idkit.ClockFunc(time.Now)

// idkitTx returns the transaction ctx carries, or nil when it carries none.
func idkitTx(ctx context.Context) *gorm.DB {
	if !utils.InTransaction(ctx) {
		return nil
	}
	tx, _ := utils.GetTxFromContext(ctx)
	return tx
}

// parseIDKitID converts a boundary id to the database key: a canonical
// decimal (no sign, padding or leading zero), non-zero and within uint.
// Anything else is idkit.ErrInvalidAccountID.
func parseIDKitID(s string) (uint, error) {
	parsed, err := strconv.ParseUint(s, 10, strconv.IntSize)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != s {
		return 0, idkit.ErrInvalidAccountID
	}
	return uint(parsed), nil
}

// parseOptionalIDKitID is parseIDKitID with "" read as unknown (0).
func parseOptionalIDKitID(s string) (uint, error) {
	if s == "" {
		return 0, nil
	}
	return parseIDKitID(s)
}

// formatIDKitID is the boundary form of a database key.
func formatIDKitID(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}

// ------------------------------------------------------------ identities ---

type idkitIdentityStore struct {
	repo repository.UserIdentityRepository
}

// NewIDKitIdentityStore returns the identity store over user_identities. It
// joins a transaction carried by ctx.
func NewIDKitIdentityStore(repo repository.UserIdentityRepository) idkit.IdentityStore {
	return &idkitIdentityStore{repo: repo}
}

func (s *idkitIdentityStore) bound(ctx context.Context) repository.UserIdentityRepository {
	if tx := idkitTx(ctx); tx != nil {
		return s.repo.WithTx(tx)
	}
	return s.repo
}

// Get returns the stored row unfiltered; a miss is idkit.ErrNotFound.
func (s *idkitIdentityStore) Get(ctx context.Context, issuer, subject string) (*idkit.Identity, error) {
	row, err := s.bound(ctx).GetByIssuerSubject(issuer, subject)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, fmt.Errorf("user identity: %w", idkit.ErrNotFound)
		}
		return nil, err
	}
	return &idkit.Identity{
		AccountID: formatIDKitID(row.UserID),
		Issuer:    row.Issuer,
		Subject:   row.Subject,
		Source:    row.Source,
	}, nil
}

// Create inserts the row; a used pair is idkit.ErrIdentityTaken.
func (s *idkitIdentityStore) Create(ctx context.Context, identity idkit.Identity) error {
	userID, err := parseIDKitID(identity.AccountID)
	if err != nil {
		return err
	}
	row := &models.UserIdentity{
		UserID:  userID,
		Issuer:  identity.Issuer,
		Subject: identity.Subject,
		Source:  identity.Source,
	}
	if err := s.bound(ctx).Create(row); err != nil {
		if errors.Is(err, repository.ErrUserIdentityTaken) {
			return fmt.Errorf("user identity: %w", idkit.ErrIdentityTaken)
		}
		return err
	}
	return nil
}

// -------------------------------------------------------------- attempts ---

type idkitAttemptStore struct {
	repo repository.OIDCLoginAttemptRepository
}

// NewIDKitAttemptStore returns the attempt store over oidc_login_attempts.
// It runs only on the root handle: inside a transaction carried by ctx every
// method returns idkit.ErrInTransaction without a statement, so a claim can
// never be undone by an enclosing rollback.
func NewIDKitAttemptStore(repo repository.OIDCLoginAttemptRepository) idkit.AttemptStore {
	return &idkitAttemptStore{repo: repo}
}

func (s *idkitAttemptStore) Create(ctx context.Context, attempt idkit.Attempt) (string, error) {
	if utils.InTransaction(ctx) {
		return "", idkit.ErrInTransaction
	}
	row := &models.OIDCLoginAttempt{
		CreatedAt:    attempt.CreatedAt,
		StateHash:    attempt.StateHash,
		Nonce:        attempt.Nonce,
		CodeVerifier: attempt.Verifier,
		Issuer:       attempt.Issuer,
		ExpiresAt:    attempt.ExpiresAt,
	}
	if err := s.repo.Create(row); err != nil {
		return "", err
	}
	return formatIDKitID(row.ID), nil
}

func (s *idkitAttemptStore) Claim(ctx context.Context, stateHash string, now time.Time) (*idkit.Attempt, error) {
	if utils.InTransaction(ctx) {
		return nil, idkit.ErrInTransaction
	}
	row, err := s.repo.Claim(stateHash, now)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, fmt.Errorf("sign-in attempt: %w", idkit.ErrNotFound)
		}
		return nil, err
	}
	return &idkit.Attempt{
		ID:        formatIDKitID(row.ID),
		StateHash: row.StateHash,
		Nonce:     row.Nonce,
		Verifier:  row.CodeVerifier,
		Issuer:    row.Issuer,
		ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt,
	}, nil
}

func (s *idkitAttemptStore) Prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	if utils.InTransaction(ctx) {
		return 0, idkit.ErrInTransaction
	}
	return s.repo.PruneCreatedBefore(ctx, before, limit)
}

// -------------------------------------------------------------- sessions ---

type idkitSessionIssuer struct {
	auth          *authService
	users         repository.UserRepository
	refreshTokens repository.RefreshTokenRepository
}

// NewIDKitSessionIssuer returns the session issuer over the session-issuing
// auth service; any other AuthService is errOIDCNotWired at construction.
func NewIDKitSessionIssuer(auth AuthService, users repository.UserRepository, refreshTokens repository.RefreshTokenRepository) (idkit.SessionIssuer, error) {
	impl, ok := auth.(*authService)
	if !ok || impl == nil {
		return nil, errOIDCNotWired
	}
	return &idkitSessionIssuer{auth: impl, users: users, refreshTokens: refreshTokens}, nil
}

// Issue mints a single sign-on session for the account: a refresh token in a
// new family whose start, and so whose lifetime cap, is req.SignedInAt, and
// an access token signed now. It joins a transaction carried by ctx for the
// account read and the refresh-token write. It performs no account checks:
// the caller checked the account under its lock.
func (s *idkitSessionIssuer) Issue(ctx context.Context, req idkit.SessionRequest) (idkit.Session, error) {
	if req.Method != idkit.SignInMethodOIDC {
		return idkit.Session{}, fmt.Errorf("session sign-in method %q is not supported", req.Method)
	}
	if req.SignedInAt.IsZero() {
		return idkit.Session{}, errors.New("session sign-in time is required")
	}
	userID, err := parseIDKitID(req.AccountID)
	if err != nil {
		return idkit.Session{}, err
	}
	users, refreshTokens := s.users, s.refreshTokens
	if tx := idkitTx(ctx); tx != nil {
		users, refreshTokens = s.users.WithTx(tx), s.refreshTokens.WithTx(tx)
	}
	user, err := users.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return idkit.Session{}, fmt.Errorf("session account: %w", idkit.ErrNotFound)
		}
		return idkit.Session{}, err
	}
	familyID, err := newRefreshFamilyID()
	if err != nil {
		return idkit.Session{}, err
	}
	session := refreshSession{FamilyID: familyID, SignInMethod: models.RefreshTokenSignInOIDC, StartedAt: req.SignedInAt}
	refresh, err := s.auth.issueRefreshToken(refreshTokens, user.ID, session)
	if err != nil {
		return idkit.Session{}, err
	}
	access, err := s.auth.accessTokenFor(user)
	if err != nil {
		return idkit.Session{}, fmt.Errorf("failed to sign access token at single sign-on: %w", err)
	}
	return idkit.Session{AccountID: formatIDKitID(user.ID), AccessToken: access, RefreshToken: refresh}, nil
}

// ----------------------------------------------------------------- audit ---

type idkitAuditSink struct {
	audit AuthAuditService
}

// NewIDKitAuditSink returns the audit sink over the sign-in audit service.
// The service writes on the root handle, so inside a transaction carried by
// ctx the sink writes nothing and returns "".
func NewIDKitAuditSink(audit AuthAuditService) idkit.AuditSink {
	return &idkitAuditSink{audit: audit}
}

// Record writes one row for a registered method, outcome and reason and
// returns its id; anything else, a malformed id or a ctx carrying a
// transaction writes nothing and returns "".
func (s *idkitAuditSink) Record(ctx context.Context, event idkit.AuditEvent) string {
	if s.audit == nil || utils.InTransaction(ctx) {
		return ""
	}
	if !authAuditMethods[event.Method] || !authAuditOutcomes[event.Outcome] ||
		(event.Reason != "" && !authAuditReasons[event.Reason]) {
		return ""
	}
	userID, err := parseOptionalIDKitID(event.AccountID)
	if err != nil {
		return ""
	}
	attemptID, err := parseOptionalIDKitID(event.AttemptID)
	if err != nil {
		return ""
	}
	id := s.audit.Record(ctx, AuthAuditEntry{
		Method:      event.Method,
		Outcome:     event.Outcome,
		Reason:      event.Reason,
		UserID:      userID,
		AttemptID:   attemptID,
		SubjectRef:  event.SubjectRef,
		ClaimRoles:  event.ClaimRoles,
		ClaimGroups: event.ClaimGroups,
	})
	if id == 0 {
		return ""
	}
	return formatIDKitID(id)
}

// --------------------------------------------------------- configuration ---

type idkitConfigSource struct {
	sso SSOSettingsService
}

// NewIDKitConfigSource returns the configuration source over the single
// sign-on settings service. The settings are read on the root handle, so
// inside a transaction carried by ctx it returns idkit.ErrInTransaction
// without reading them.
func NewIDKitConfigSource(sso SSOSettingsService) idkit.ConfigSource {
	return &idkitConfigSource{sso: sso}
}

// Config copies the effective configuration; AllowPassword and Sources stay
// with the CRM.
func (s *idkitConfigSource) Config(ctx context.Context) (idkit.Config, error) {
	if utils.InTransaction(ctx) {
		return idkit.Config{}, idkit.ErrInTransaction
	}
	cfg, err := s.sso.SSOConfig(ctx)
	if err != nil {
		return idkit.Config{}, err
	}
	out := idkit.Config{
		Enabled:             cfg.Enabled,
		IssuerURL:           cfg.IssuerURL,
		ClientID:            cfg.ClientID,
		ClientSecret:        cfg.ClientSecret,
		GraphMaxPages:       cfg.GraphMaxPages,
		EntraAppSigningKeys: cfg.EntraAppSigningKeys,
	}
	if cfg.Scopes != nil {
		out.Scopes = append([]string(nil), cfg.Scopes...)
	}
	if cfg.RoleMapping != nil {
		out.RoleMapping = append([]rolemap.Rule(nil), cfg.RoleMapping...)
	}
	if cfg.ClientCertificate != nil {
		out.ClientCertificate = &idkitoidc.ClientCertificate{
			Certificate: cfg.ClientCertificate.Certificate,
			PrivateKey:  cfg.ClientCertificate.PrivateKey,
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- logger ---

type idkitLogger struct {
	component string
}

// NewIDKitLogger returns a logger over utils.Logger whose lines carry the
// component, the request id of ctx and the given fields, nothing else.
func NewIDKitLogger(component string) idkit.Logger {
	return &idkitLogger{component: component}
}

func (l *idkitLogger) entry(ctx context.Context, fields []idkit.Field) *logrus.Entry {
	base := utils.Logger
	if base == nil {
		base = logrus.StandardLogger()
	}
	data := logrus.Fields{
		"component":  l.component,
		"request_id": utils.RequestIDFromContext(ctx),
	}
	for _, field := range fields {
		data[field.Key] = field.Value
	}
	return base.WithFields(data)
}

func (l *idkitLogger) Info(ctx context.Context, msg string, fields ...idkit.Field) {
	l.entry(ctx, fields).Info(msg)
}

func (l *idkitLogger) Warn(ctx context.Context, msg string, fields ...idkit.Field) {
	l.entry(ctx, fields).Warn(msg)
}
