package service

import (
	"context"
	"sort"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// The read side of the sign-in audit log (#455). The filter values are
// matched exactly and case-sensitively against closed sets before any SQL
// runs, so MySQL and MariaDB (case-insensitive collation) and SQLite answer
// the same: "PASSWORD" or "password " is refused on every engine.

var authAuditMethods = map[string]bool{
	AuthAuditMethodPassword: true,
	AuthAuditMethodOIDC:     true,
	AuthAuditMethodRefresh:  true,
	AuthAuditMethodAPIKey:   true,
}

var authAuditOutcomes = map[string]bool{
	AuthAuditOutcomeSuccess: true,
	AuthAuditOutcomeRefused: true,
}

// authAuditReasons is every reason code the service writes: the constants
// above, the single sign-on refusal codes of oidcRefuse/oidcRefuseWith, the
// 503 codes of ssoUnavailable and the owner-lock codes of the login.
var authAuditReasons = map[string]bool{
	"account_erased":            true,
	"account_inactive":          true,
	"account_locked":            true,
	"claims_invalid":            true,
	"customer_login_role":       true,
	"discovery_failed":          true,
	"email_missing":             true,
	"email_taken":               true,
	"group_overage":             true,
	"groups_malformed":          true,
	"groups_unavailable":        true,
	"id_token_invalid":          true,
	"id_token_missing":          true,
	"identity_mismatch":         true,
	"identity_taken":            true,
	"internal_error":            true,
	"invalid_credentials":       true,
	"issuer_changed":            true,
	"issuer_invalid":            true,
	"key_expired":               true,
	"key_invalid":               true,
	"key_revoked":               true,
	"last_admin":                true,
	"no_mapping_match":          true,
	"nonce_mismatch":            true,
	"not_configured":            true,
	"password_changed":          true,
	"role_missing":              true,
	"roles_malformed":           true,
	"sso_account":               true,
	"sso_disabled":              true,
	"state_invalid":             true,
	"subject_invalid":           true,
	"token_exchange_failed":     true,
	"token_invalid":             true,
	"unavailable":               true,
	"userinfo_subject_mismatch": true,
}

// IsAuthAuditMethod reports whether value is one of the four methods.
func IsAuthAuditMethod(value string) bool { return authAuditMethods[value] }

// IsAuthAuditOutcome reports whether value is success or refused.
func IsAuthAuditOutcome(value string) bool { return authAuditOutcomes[value] }

// IsAuthAuditReason reports whether value is a reason code the service writes.
func IsAuthAuditReason(value string) bool { return authAuditReasons[value] }

// AuthAuditMethods returns the methods, sorted.
func AuthAuditMethods() []string { return authAuditSortedKeys(authAuditMethods) }

// AuthAuditOutcomes returns the outcomes, sorted.
func AuthAuditOutcomes() []string { return authAuditSortedKeys(authAuditOutcomes) }

// AuthAuditReasons returns the reason codes, sorted.
func AuthAuditReasons() []string { return authAuditSortedKeys(authAuditReasons) }

func authAuditSortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// AuthAuditLogService reads the sign-in audit log. It is separate from the
// writer AuthAuditService. The handler validates the filter first.
type AuthAuditLogService interface {
	// ListSignIns returns one page of rows, newest first, and the number of
	// rows matching filter.
	ListSignIns(ctx context.Context, filter repository.AuthAuditFilter, offset, limit int) ([]models.AuthAuditLog, int64, error)
}

type authAuditLogService struct {
	repo repository.AuthAuditRepository
}

// NewAuthAuditLogService returns the audit reader over repo.
func NewAuthAuditLogService(repo repository.AuthAuditRepository) AuthAuditLogService {
	return &authAuditLogService{repo: repo}
}

func (s *authAuditLogService) ListSignIns(_ context.Context, filter repository.AuthAuditFilter, offset, limit int) ([]models.AuthAuditLog, int64, error) {
	return s.repo.List(filter, offset, limit)
}
