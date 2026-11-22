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
	"github.com/florinel-chis/gophercrm/internal/idkit/sso"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// GopherCRM's implementations of the host interfaces declared in
// internal/idkit/host.go, over the existing repositories, the auth service,
// the sign-in audit service, the single sign-on settings service, the
// transaction manager and the logger. NewOIDCService wires them into the
// sign-in of internal/idkit/sso, together with the sign-in's own account
// store (oidcAccountStore: the governed role write and the provisioning)
// and its transaction (oidcSignInRunner: the governed transaction, which
// takes the admin set first).
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
var _ idkit.AccountStore = (*oidcAccountStore)(nil)
var _ idkit.TxRunner = (*oidcSignInRunner)(nil)
var _ idkit.SessionIssuer = (*oidcSessionIssuer)(nil)
var _ idkit.ConfigSource = (*oidcConfigSource)(nil)
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

// Create inserts the row; a used pair is idkit.ErrIdentityTaken (and
// repository.ErrUserIdentityTaken).
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
			// Both sentinels stay in the chain: the host's callers classify
			// the repository's.
			return fmt.Errorf("user identity: %w: %w", idkit.ErrIdentityTaken, err)
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
	session, _, err := s.issue(ctx, req)
	return session, err
}

// issue is Issue that also returns the account row it read.
func (s *idkitSessionIssuer) issue(ctx context.Context, req idkit.SessionRequest) (idkit.Session, *models.User, error) {
	if req.Method != idkit.SignInMethodOIDC {
		return idkit.Session{}, nil, fmt.Errorf("session sign-in method %q is not supported", req.Method)
	}
	if req.SignedInAt.IsZero() {
		return idkit.Session{}, nil, errors.New("session sign-in time is required")
	}
	userID, err := parseIDKitID(req.AccountID)
	if err != nil {
		return idkit.Session{}, nil, err
	}
	users, refreshTokens := s.users, s.refreshTokens
	if tx := idkitTx(ctx); tx != nil {
		users, refreshTokens = s.users.WithTx(tx), s.refreshTokens.WithTx(tx)
	}
	user, err := users.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return idkit.Session{}, nil, fmt.Errorf("session account: %w", idkit.ErrNotFound)
		}
		return idkit.Session{}, nil, err
	}
	familyID, err := newRefreshFamilyID()
	if err != nil {
		return idkit.Session{}, nil, err
	}
	session := refreshSession{FamilyID: familyID, SignInMethod: models.RefreshTokenSignInOIDC, StartedAt: req.SignedInAt}
	refresh, err := s.auth.issueRefreshToken(refreshTokens, user.ID, session)
	if err != nil {
		return idkit.Session{}, nil, err
	}
	access, err := s.auth.accessTokenFor(user)
	if err != nil {
		return idkit.Session{}, nil, fmt.Errorf("failed to sign access token at single sign-on: %w", err)
	}
	return idkit.Session{AccountID: formatIDKitID(user.ID), AccessToken: access, RefreshToken: refresh}, user, nil
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

// --------------------------------------------- the sign-in's own adapters ---

// oidcConfigSource is the settings source of the OIDC sign-in: the
// configuration source over the single sign-on settings, refused with
// errOIDCNotWired when NewOIDCService did not get the session-issuing auth
// service.
type oidcConfigSource struct {
	wired bool
	inner idkit.ConfigSource
}

func (s *oidcConfigSource) Config(ctx context.Context) (idkit.Config, error) {
	if !s.wired {
		return idkit.Config{}, errOIDCNotWired
	}
	cfg, err := s.inner.Config(ctx)
	if err != nil {
		return idkit.Config{}, fmt.Errorf("read the single sign-on settings: %w", err)
	}
	return cfg, nil
}

// oidcGovernedKey carries the governed transaction of one sign-in attempt
// on the ctx oidcSignInRunner hands to fn.
type oidcGovernedKey struct{}

// oidcGoverned is the governed transaction of one run of fn and the admin
// set it locked first.
type oidcGoverned struct {
	tx     *governedTx
	admins adminSet
}

// errOIDCNotGoverned is a wiring fault: an account store method was called
// with a ctx that oidcSignInRunner did not make.
var errOIDCNotGoverned = errors.New("oidc account store called outside the governed sign-in transaction")

// oidcGovernedFrom is the governed transaction ctx carries.
func oidcGovernedFrom(ctx context.Context) (*oidcGoverned, error) {
	governed, ok := ctx.Value(oidcGovernedKey{}).(*oidcGoverned)
	if !ok || governed == nil {
		return nil, errOIDCNotGoverned
	}
	return governed, nil
}

// oidcSignInRunner is the transaction of an OIDC sign-in: one governed root
// transaction (roleGovernance.runWithContext, replayed on a deadlock or a
// lock wait and restarted as a governed write is), whose first statement
// locks the admin set. It continues the governed lock order:
//
//	admin set → users → roles → user_identities → refresh_tokens
//
// fn's ctx carries the transaction handle (utils.InTransaction), which the
// identity store and the session issuer join, and the governed transaction
// with the locked admin set, which oidcAccountStore uses. Every run of fn
// gets a fresh one.
type oidcSignInRunner struct {
	governance *roleGovernance
}

func (r *oidcSignInRunner) WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	if r.governance == nil {
		return errRoleGovernanceUnavailable
	}
	return r.governance.runWithContext(ctx, func(ctx context.Context, tx *governedTx) error {
		admins, err := lockAdminSet(tx)
		if err != nil {
			return err
		}
		return fn(context.WithValue(ctx, oidcGovernedKey{}, &oidcGoverned{tx: tx, admins: admins}))
	})
}

// oidcPasswordFirstRunner computes the unusable password of a first sign-in
// (oidcUnusablePasswordHash) once per callback, after the token checks and
// before next starts the transaction, so bcrypt never runs while the admin
// set and the role rows are locked. A replay of the transaction reuses it;
// oidcAccountStore.Provision reads it from ctx.
type oidcPasswordFirstRunner struct {
	next idkit.TxRunner
}

func (r *oidcPasswordFirstRunner) WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	passwordHash, err := oidcUnusablePasswordHash()
	if err != nil {
		return err
	}
	return r.next.WithTransaction(context.WithValue(ctx, oidcPasswordHashKey{}, passwordHash), fn)
}

// oidcPasswordHashKey carries the hash oidcPasswordFirstRunner computed.
type oidcPasswordHashKey struct{}

// errOIDCPasswordNotPrepared is a wiring fault: Provision ran without the
// hash oidcPasswordFirstRunner puts on ctx.
var errOIDCPasswordNotPrepared = errors.New("single sign-on account password not prepared before the transaction")

// oidcAccountStore is the account store of the OIDC sign-in: the governed
// role write of an existing account and the provisioning of a new one, both
// on the governed transaction oidcSignInRunner put on ctx. The sign-in
// decides which account an identity belongs to; this store never reads
// user_identities.
type oidcAccountStore struct {
	// beforeInsert is a test seam that runs on a first sign-in after the
	// email check and before the users insert; nil in production.
	beforeInsert func()
}

// oidcAccountRefusal attaches the account a refusal concerns (the audit
// row's user_id, #454).
func oidcAccountRefusal(err error, userID uint) error {
	return sso.RefuseAccount(err, formatIDKitID(userID))
}

// SignIn signs in as the linked account: it locks the users row, refuses an
// erased or deactivated account, re-applies the mapped role with the rules
// of governedWrite minus the actor-key and escalation checks (the mapping is
// the administrator's grant): ErrCustomerLoginRole and ErrLastAdmin refuse,
// and a changed role commits its user_role_changed audit row with
// auth_method oidc. Every refusal of the account names it.
func (s *oidcAccountStore) SignIn(ctx context.Context, accountID string, roleID uint) error {
	governed, err := oidcGovernedFrom(ctx)
	if err != nil {
		return err
	}
	tx, admins := governed.tx, governed.admins
	userID, err := parseIDKitID(accountID)
	if err != nil {
		return err
	}
	users, err := tx.lockUsers(userID)
	if err != nil {
		return err
	}
	target := users[userID]
	if target == nil {
		return oidcAccountRefusal(oidcRefuse(AuthAuditReasonAccountErased), userID)
	}
	if !target.IsActive {
		return oidcAccountRefusal(oidcRefuse(AuthAuditReasonAccountInactive), target.ID)
	}
	mapped := roleID
	roles, err := tx.lockRoles(target.RoleID, &mapped)
	if err != nil {
		return err
	}
	role, assignedID, custom, err := oidcMappedRole(roles[mapped])
	if err != nil {
		// role_missing concerns the mapping, not the account: no user id.
		return err
	}
	if role == models.RoleCustom && target.Role == models.RoleCustomer {
		return oidcAccountRefusal(oidcRefuseWith(AuthAuditReasonCustomerAccount, apperrors.ErrCustomerLoginRole), target.ID)
	}
	admins.include(target)
	if errors.Is(admins.checkAfter(map[uint]userChange{target.ID: {role: role, active: target.IsActive}}), apperrors.ErrLastAdmin) {
		return oidcAccountRefusal(oidcRefuseWith(AuthAuditReasonLastAdmin, apperrors.ErrLastAdmin), target.ID)
	}
	if role != target.Role || !sameRoleID(assignedID, target.RoleID) {
		fields := map[string]interface{}{"role": role, "role_id": nil}
		if assignedID != nil {
			fields["role_id"] = *assignedID
		}
		if err := tx.users.UpdateFields(target.ID, fields); err != nil {
			if isNotFound(err) {
				return oidcAccountRefusal(oidcRefuse(AuthAuditReasonAccountErased), userID)
			}
			return err
		}
		if err := tx.record(Actor{UserID: target.ID, AuthMethod: AuthMethodOIDC}, models.RoleAuditUserRoleChanged,
			assignedID, assignedRoleName(role, custom), &target.ID,
			userRoleAuditState{Role: target.Role, RoleID: target.RoleID},
			userRoleAuditState{Role: role, RoleID: assignedID}); err != nil {
			return err
		}
	}
	if _, err := tx.users.GetByID(target.ID); err != nil {
		if isNotFound(err) {
			return oidcAccountRefusal(oidcRefuse(AuthAuditReasonAccountErased), userID)
		}
		return err
	}
	return nil
}

// Provision creates an active user with the mapped role and its audit row,
// refused when any user, erased ones included, holds the address in any
// letter case: an email never links to an existing account. The password is
// the bcrypt hash of discarded random bytes, computed before the
// transaction by oidcPasswordFirstRunner, so the account has no usable
// password. A replayed attempt inserts a fresh row.
func (s *oidcAccountStore) Provision(ctx context.Context, profile idkit.Profile, roleID uint) (string, error) {
	governed, err := oidcGovernedFrom(ctx)
	if err != nil {
		return "", err
	}
	tx := governed.tx
	if profile.Email == "" || len(profile.Email) > oidcEmailMaxBytes {
		return "", oidcRefuse(AuthAuditReasonEmailMissing)
	}
	mapped := roleID
	roles, err := tx.lockRoles(&mapped)
	if err != nil {
		return "", err
	}
	role, assignedID, custom, err := oidcMappedRole(roles[mapped])
	if err != nil {
		return "", err
	}
	taken, err := tx.users.EmailExistsFoldedUnscoped(profile.Email)
	if err != nil {
		return "", err
	}
	if taken {
		// Never linked to the existing account, live or erased.
		return "", oidcRefuse(AuthAuditReasonEmailTaken)
	}
	passwordHash, ok := ctx.Value(oidcPasswordHashKey{}).(string)
	if !ok || passwordHash == "" {
		return "", errOIDCPasswordNotPrepared
	}
	if s.beforeInsert != nil {
		s.beforeInsert()
	}
	created := &models.User{
		Email:     profile.Email,
		Password:  passwordHash,
		FirstName: profile.GivenName,
		LastName:  profile.FamilyName,
		Role:      role,
		RoleID:    assignedID,
		IsActive:  true,
	}
	if err := tx.users.Create(created); err != nil {
		if errors.Is(err, apperrors.ErrDuplicateEmail) {
			return "", oidcRefuseWith(AuthAuditReasonEmailTaken, err)
		}
		return "", err
	}
	if err := tx.record(Actor{UserID: created.ID, AuthMethod: AuthMethodOIDC}, models.RoleAuditUserRoleChanged,
		assignedID, assignedRoleName(role, custom), &created.ID,
		nil, userRoleAuditState{Role: role, RoleID: assignedID}); err != nil {
		return "", err
	}
	return formatIDKitID(created.ID), nil
}

// oidcSignInResultKey carries the oidcSignInResult of one Callback.
type oidcSignInResultKey struct{}

// oidcSignInResult is the account row the session issuing read inside the
// sign-in transaction, for the AuthTokens Callback returns. Each Issue
// overwrites it.
type oidcSignInResult struct {
	user *models.User
}

// oidcSessionIssuer is the session issuer of the OIDC sign-in: the idkit
// session issuer, which also hands the account row it read to the
// Callback that ctx belongs to.
type oidcSessionIssuer struct {
	inner *idkitSessionIssuer
}

func (s *oidcSessionIssuer) Issue(ctx context.Context, req idkit.SessionRequest) (idkit.Session, error) {
	session, user, err := s.inner.issue(ctx, req)
	if err != nil {
		return idkit.Session{}, err
	}
	if result, ok := ctx.Value(oidcSignInResultKey{}).(*oidcSignInResult); ok && result != nil {
		result.user = user
	}
	return session, nil
}
