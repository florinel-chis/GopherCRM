package models

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// capAPIKeyLifetimes is the startup half of migration
// 20261129120003_api_key_lifetime (#532), on every driver. It gives every key
// without an expiry, or with one later than APIKeyExpiryCap(now), the expiry
// APIKeyExpiryCap(now), and revokes (is_active false, revoked_at now) every
// key of a customer account that is not revoked yet. Soft-deleted rows are
// included. Both statements are raw UPDATEs, so updated_at is not touched.
//
// A second run changes nothing: a capped expiry is earlier than the next
// run's cap, and a revoked key already has revoked_at. On an up-to-date
// database the step is two UPDATEs that match no row.
func capAPIKeyLifetimes(tx *gorm.DB) error {
	now := time.Now()
	limit := APIKeyExpiryCap(now)
	if err := tx.Exec("UPDATE api_keys SET expires_at = ? WHERE expires_at IS NULL OR expires_at > ?",
		limit, limit).Error; err != nil {
		return fmt.Errorf("api key lifetime: failed to cap expiries: %w", err)
	}
	if err := tx.Exec("UPDATE api_keys SET is_active = ?, revoked_at = ? WHERE revoked_at IS NULL AND user_id IN (SELECT id FROM users WHERE role = ?)",
		false, now, string(RoleCustomer)).Error; err != nil {
		return fmt.Errorf("api key lifetime: failed to revoke keys of customer accounts: %w", err)
	}
	return nil
}
