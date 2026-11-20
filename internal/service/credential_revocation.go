package service

import (
	"fmt"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// credentialStores are the repositories revokeAllCredentials writes through.
// All three are bound to the caller's transaction; users is the handle the
// caller already writes the owner row through.
type credentialStores struct {
	users         repository.UserRepository
	refreshTokens repository.RefreshTokenRepository
	apiKeys       repository.APIKeyRepository
}

// revokeAllCredentials ends every credential of owner inside the caller's
// transaction: it increments users.token_version (every access token issued
// before is refused from the commit on), revokes every refresh token and
// revokes every API key (is_active false, revoked_at stamped with at; a key
// revoked before keeps its first revoked_at). owner is the users row the
// caller locked in this transaction; its TokenVersion is set to the new
// value, so an access token signed from it afterwards carries the new
// generation.
//
// The writes follow the documented lock order users -> refresh_tokens ->
// api_keys. Every error is returned wrapped with %w and must roll the
// caller's whole transaction back. Callers: the password change, the
// password-reset confirm, the governed deactivation (WithUserCredentialRevocation)
// and the SCIM deactivations (WithSCIMUserWrites). Logout keeps its own
// revocation and leaves API keys alone. The helper logs nothing.
func revokeAllCredentials(stores credentialStores, owner *models.User, at time.Time) error {
	newVersion := owner.TokenVersion + 1
	if err := stores.users.UpdateFields(owner.ID, map[string]interface{}{
		"token_version": newVersion,
	}); err != nil {
		return fmt.Errorf("failed to end access tokens: %w", err)
	}
	owner.TokenVersion = newVersion
	if err := stores.refreshTokens.RevokeAllForUser(owner.ID); err != nil {
		return fmt.Errorf("failed to revoke refresh tokens: %w", err)
	}
	if _, err := stores.apiKeys.RevokeAllForUser(owner.ID, at); err != nil {
		return fmt.Errorf("failed to revoke API keys: %w", err)
	}
	return nil
}
