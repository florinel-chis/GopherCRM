package service

import (
	"errors"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

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
