package service

import (
	"context"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

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
