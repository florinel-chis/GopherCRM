package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// Sign-in audit log (#454).
//
// One auth_audit_log row is written per password sign-in that reaches
// LoginWithTokens, per single sign-on callback and per refresh, with attempts
// 1. Failed API-key authentications are counted instead (#466): one row per
// reason, user id, client network and UTC minute, whose attempts is the number
// of refusals in that bucket; the first refusal writes the row and every later
// one adds 1 to it in the same upsert statement, so its request id, user-agent
// family and created_at stay the first refusal's. A row is pseudonymous: the method, the outcome, a
// stable reason code, the user id when the account is known, the sign-in
// attempt id, the request id (only when it matches
// ^[A-Za-z0-9._:-]{1,64}$), an HMAC reference of a refused single sign-on
// identity, the role and group claim values the mapping used, the client's
// /24 or /48 network and a user-agent family from a fixed vocabulary. An
// email, a name, a subject, a token, a code, a state, a nonce, a password, a
// full IP address or a full user agent is never stored.
//
// The row is written on the root handle after the sign-in's transaction has
// ended, never inside it (the SQLite pool holds one connection). A failed
// write is logged once (component auth_audit, error_code audit_write_failed)
// and never changes the sign-in's answer. Rows older than
// AUTH_AUDIT_RETENTION_DAYS are pruned by the stale bulk operation sweep
// (PruneAuthAuditLog).

// Methods of an auth_audit_log row.
const (
	AuthAuditMethodPassword = "password"
	AuthAuditMethodOIDC     = "oidc"
	AuthAuditMethodRefresh  = "refresh"
	AuthAuditMethodAPIKey   = "api_key"
)

// Outcomes of an auth_audit_log row.
const (
	AuthAuditOutcomeSuccess = "success"
	AuthAuditOutcomeRefused = "refused"
)

// Reason codes that are not single sign-on refusal codes. The single sign-on
// refusals carry the error_code of oidcRefuse; its 503s carry
// not_configured, discovery_failed or token_exchange_failed.
const (
	AuthAuditReasonInvalidCredentials = "invalid_credentials"
	AuthAuditReasonAccountLocked      = "account_locked"
	AuthAuditReasonAccountInactive    = "account_inactive"
	AuthAuditReasonAccountErased      = "account_erased"
	AuthAuditReasonPasswordChanged    = "password_changed"
	AuthAuditReasonSSOAccount         = "sso_account"
	AuthAuditReasonInternalError      = "internal_error"
	AuthAuditReasonTokenInvalid       = "token_invalid"
	AuthAuditReasonUnavailable        = "unavailable"
	AuthAuditReasonKeyInvalid         = "key_invalid"
	AuthAuditReasonKeyRevoked         = "key_revoked"
	AuthAuditReasonKeyExpired         = "key_expired"
	AuthAuditReasonSSODisabled        = "sso_disabled"
)

// Bounds of the stored claim values.
const (
	authAuditMaxClaimValues     = 100
	authAuditClaimValueMaxBytes = 256
	// authAuditClaimColumnMaxBytes is what a MySQL TEXT column holds.
	authAuditClaimColumnMaxBytes = 65535
	// authAuditPruneChunk is how many rows one prune statement deletes.
	authAuditPruneChunk = 500
	// authAuditDefaultRetentionDays applies when no positive retention is
	// passed (config.AuthAuditRetentionDaysDefault).
	authAuditDefaultRetentionDays = 90
)

// AuthAuditEntry is one outcome to record. Request id, client network and
// user-agent family come from the context passed to Record.
type AuthAuditEntry struct {
	Method  string
	Outcome string
	// Reason is empty on success.
	Reason string
	// UserID and AttemptID are 0 when unknown (stored as NULL).
	UserID    uint
	AttemptID uint
	// SubjectRef is empty unless a verified single sign-on identity was
	// refused (stored as NULL).
	SubjectRef string
	// ClaimRoles and ClaimGroups are nil when the claims were not reached or
	// malformed (stored as NULL), otherwise stored as JSON arrays.
	ClaimRoles  []string
	ClaimGroups []string
}

// AuthAuditService writes the sign-in audit log. Every method returns the id
// of the row it wrote, or 0 when the write failed; none returns an error or
// panics. A nil service is never called: the callers check for nil.
type AuthAuditService interface {
	Record(ctx context.Context, entry AuthAuditEntry) uint
	// RecordPassword records the outcome of LoginWithTokens.
	RecordPassword(ctx context.Context, tokens *AuthTokens, err error) uint
	// RecordRefresh records the outcome of RefreshAccessToken.
	RecordRefresh(ctx context.Context, tokens *AuthTokens, err error) uint
	// RecordAPIKeyFailure counts a failed ValidateAPIKey in the aggregated
	// row of its reason, user id, client network and UTC minute (#466).
	RecordAPIKeyFailure(ctx context.Context, err error) uint
}

type authAuditService struct {
	repo repository.AuthAuditRepository
	now  func() time.Time
}

// NewAuthAuditService returns the audit writer over repo.
func NewAuthAuditService(repo repository.AuthAuditRepository) AuthAuditService {
	return &authAuditService{repo: repo, now: time.Now}
}

// Record inserts one row and returns its id, or 0 after logging the failure.
func (s *authAuditService) Record(ctx context.Context, entry AuthAuditEntry) (id uint) {
	if s == nil || s.repo == nil {
		return 0
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			id = 0
			authAuditWriteFailed(entry, fmt.Errorf("audit write panicked: %v", recovered))
		}
	}()
	row := s.row(ctx, entry)
	if err := s.repo.Create(row); err != nil {
		authAuditWriteFailed(entry, err)
		return 0
	}
	return row.ID
}

// recordAggregated adds entry to the row of its reason, user id, client
// network and UTC minute and returns that row's id, or 0 after logging the
// failure once.
func (s *authAuditService) recordAggregated(ctx context.Context, entry AuthAuditEntry) (id uint) {
	if s == nil || s.repo == nil {
		return 0
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			id = 0
			authAuditWriteFailed(entry, fmt.Errorf("audit write panicked: %v", recovered))
		}
	}()
	row := s.row(ctx, entry)
	key := authAuditAggregateKey(row)
	row.AggregateKey = &key
	id, err := s.repo.RecordAggregated(row)
	if err != nil {
		authAuditWriteFailed(entry, err)
		return 0
	}
	return id
}

// authAuditAggregateKey is the bucket of an API-key refusal:
// "api_key|<reason>|<user id or 0>|<client network>|<UTC minute>". It holds
// no request fact beyond the stored network.
func authAuditAggregateKey(row *models.AuthAuditLog) string {
	var userID uint
	if row.UserID != nil {
		userID = *row.UserID
	}
	key := row.Method + "|" + row.Reason + "|" + strconv.FormatUint(uint64(userID), 10) + "|" +
		row.ClientNetwork + "|" + row.CreatedAt.UTC().Truncate(time.Minute).Format("2006-01-02T15:04")
	return truncateUTF8(key, models.AuthAuditAggregateKeyMaxLength)
}

// row is the stored form of entry with the request facts of ctx, the time
// of s.now in UTC and attempts 1.
func (s *authAuditService) row(ctx context.Context, entry AuthAuditEntry) *models.AuthAuditLog {
	info := utils.ClientInfoFromContext(ctx)
	row := &models.AuthAuditLog{
		CreatedAt:       s.now().UTC(),
		Method:          entry.Method,
		Outcome:         entry.Outcome,
		Reason:          truncateUTF8(entry.Reason, models.AuthAuditReasonMaxLength),
		UserID:          optionalID(entry.UserID),
		AttemptID:       optionalID(entry.AttemptID),
		RequestID:       utils.SafeRequestID(utils.RequestIDFromContext(ctx)),
		ClientNetwork:   utils.ClientNetwork(info.IP),
		UserAgentFamily: utils.UserAgentFamily(info.UserAgent),
		ClaimRoles:      authAuditClaimJSON(entry.ClaimRoles),
		ClaimGroups:     authAuditClaimJSON(entry.ClaimGroups),
		Attempts:        1,
	}
	if entry.SubjectRef != "" {
		ref := entry.SubjectRef
		row.SubjectRef = &ref
	}
	return row
}

// RecordPassword records one password sign-in: success with the signed-in
// user, or the refusal reason AuthAuditReason finds.
func (s *authAuditService) RecordPassword(ctx context.Context, tokens *AuthTokens, err error) uint {
	return s.Record(ctx, outcomeEntry(AuthAuditMethodPassword, tokens, err, AuthAuditReasonInternalError))
}

// RecordRefresh records one refresh: success with the token owner, or the
// refusal; an operational failure (the 503 path) is unavailable.
func (s *authAuditService) RecordRefresh(ctx context.Context, tokens *AuthTokens, err error) uint {
	return s.Record(ctx, outcomeEntry(AuthAuditMethodRefresh, tokens, err, AuthAuditReasonUnavailable))
}

// RecordAPIKeyFailure counts a refused API key in the row of its reason, user
// id, client network and UTC minute and returns that row's id; err nil
// records nothing.
func (s *authAuditService) RecordAPIKeyFailure(ctx context.Context, err error) uint {
	if err == nil {
		return 0
	}
	return s.recordAggregated(ctx, outcomeEntry(AuthAuditMethodAPIKey, nil, err, AuthAuditReasonKeyInvalid))
}

// outcomeEntry is the row of a sign-in result; fallback replaces
// internal_error for a method that names its operational failure otherwise.
func outcomeEntry(method string, tokens *AuthTokens, err error, fallback string) AuthAuditEntry {
	if err == nil {
		entry := AuthAuditEntry{Method: method, Outcome: AuthAuditOutcomeSuccess}
		if tokens != nil && tokens.User != nil {
			entry.UserID = tokens.User.ID
		}
		return entry
	}
	reason, userID := AuthAuditReason(err)
	if reason == AuthAuditReasonInternalError {
		reason = fallback
	}
	return AuthAuditEntry{Method: method, Outcome: AuthAuditOutcomeRefused, Reason: reason, UserID: userID}
}

// authAuditWriteFailed logs one failed write: the method, outcome and reason
// of the row and the redacted error, nothing of the sign-in itself.
func authAuditWriteFailed(entry AuthAuditEntry, err error) {
	base := utils.Logger
	if base == nil {
		base = logrus.StandardLogger()
	}
	base.WithFields(logrus.Fields{
		"component":  "auth_audit",
		"error_code": "audit_write_failed",
		"method":     entry.Method,
		"outcome":    entry.Outcome,
		"reason":     entry.Reason,
	}).WithError(utils.LogError(err)).Warn("Sign-in audit row could not be written")
}

func optionalID(id uint) *uint {
	if id == 0 {
		return nil
	}
	return &id
}

// authAuditClaimJSON is the stored form of claim values: nil stays NULL;
// otherwise a JSON array of at most authAuditMaxClaimValues values, each cut
// to authAuditClaimValueMaxBytes on a rune boundary, and trailing values
// dropped until the array fits a TEXT column.
func authAuditClaimJSON(values []string) *string {
	if values == nil {
		return nil
	}
	if len(values) > authAuditMaxClaimValues {
		values = values[:authAuditMaxClaimValues]
	}
	cut := make([]string, len(values))
	for i, value := range values {
		cut[i] = truncateUTF8(value, authAuditClaimValueMaxBytes)
	}
	for {
		raw, err := json.Marshal(cut)
		if err != nil {
			return nil
		}
		if len(raw) <= authAuditClaimColumnMaxBytes {
			out := string(raw)
			return &out
		}
		cut = cut[:len(cut)-1]
	}
}

// AuthAuditSubjectRef is the subject_ref of a refused single sign-on
// identity: hex(HMAC-SHA256) under secret (API_KEY_SECRET, else JWT_SECRET)
// of "auth-audit-subject:" + len(issuer) + ":" + issuer + subject. The length
// prefix keeps (issuer, subject) pairs that concatenate alike apart.
func AuthAuditSubjectRef(secret, issuer, subject string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("auth-audit-subject:" + strconv.Itoa(len(issuer)) + ":" + issuer + subject))
	return hex.EncodeToString(mac.Sum(nil))
}

// authRefusalError is a refusal of a password sign-in, a refresh or an API
// key with its audit reason and the account it concerns (0 when unknown). Its
// message is the wrapped error's, and errors.Is/As see the wrapped error, so
// every answer and message stays what it was.
type authRefusalError struct {
	reason string
	userID uint
	err    error
}

func (e *authRefusalError) Error() string { return e.err.Error() }

func (e *authRefusalError) Unwrap() error { return e.err }

func authRefusal(reason string, userID uint, err error) error {
	return &authRefusalError{reason: reason, userID: userID, err: err}
}

// ssoUnavailableError is a 503 of the single sign-on with its reason code:
// not_configured, discovery_failed or token_exchange_failed. It reads and
// compares as ErrSSOUnavailable.
type ssoUnavailableError struct {
	code string
}

func (e *ssoUnavailableError) Error() string { return ErrSSOUnavailable.Error() }

func (e *ssoUnavailableError) Is(target error) bool { return target == ErrSSOUnavailable }

func ssoUnavailable(code string) error { return &ssoUnavailableError{code: code} }

// AuthAuditReason is the audit reason of a sign-in error and the user id it
// carries (0 when unknown): the typed refusals' own codes, sso_disabled for
// ErrSSODisabled, the bare login and refresh sentinels' codes, and
// internal_error for anything else. A nil error is "", 0.
func AuthAuditReason(err error) (reason string, userID uint) {
	if err == nil {
		return "", 0
	}
	var refusal *authRefusalError
	if errors.As(err, &refusal) {
		return refusal.reason, refusal.userID
	}
	var oidcRefusal *oidcRefusalError
	if errors.As(err, &oidcRefusal) {
		return oidcRefusal.code, oidcRefusal.userID
	}
	var unavailable *ssoUnavailableError
	if errors.As(err, &unavailable) {
		return unavailable.code, 0
	}
	switch {
	case errors.Is(err, ErrSSODisabled):
		return AuthAuditReasonSSODisabled, 0
	case errors.Is(err, errLoginAccountLocked):
		return AuthAuditReasonAccountLocked, 0
	case errors.Is(err, errLoginInvalidCredentials):
		return AuthAuditReasonInvalidCredentials, 0
	case errors.Is(err, ErrInvalidRefreshToken):
		return AuthAuditReasonTokenInvalid, 0
	}
	return AuthAuditReasonInternalError, 0
}

// PruneAuthAuditLog deletes every auth_audit_log row created before
// now - retentionDays days (90 when retentionDays is not positive), in chunks
// of authAuditPruneChunk, until a chunk deletes fewer rows or ctx ends, and
// returns how many it deleted.
func PruneAuthAuditLog(ctx context.Context, repo repository.AuthAuditRepository, now time.Time, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		retentionDays = authAuditDefaultRetentionDays
	}
	cutoff := now.UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		deleted, err := repo.PruneCreatedBefore(cutoff, authAuditPruneChunk)
		total += deleted
		if err != nil {
			return total, err
		}
		if deleted < authAuditPruneChunk {
			return total, nil
		}
	}
}

// withAuthAuditUser keeps a typed refusal as it is and gives any other error
// the reason AuthAuditReason finds for it and the user id it concerns, with
// its message and errors.Is identity unchanged.
func withAuthAuditUser(err error, userID uint) error {
	if err == nil {
		return nil
	}
	var refusal *authRefusalError
	if errors.As(err, &refusal) {
		return err
	}
	reason, _ := AuthAuditReason(err)
	return authRefusal(reason, userID, err)
}
