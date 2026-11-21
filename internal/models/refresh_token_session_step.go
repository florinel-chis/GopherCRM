package models

import (
	"fmt"

	"gorm.io/gorm"
)

// backfillRefreshTokenSessions is the startup half of migration
// 20261129120004_refresh_token_session, on every driver. It gives every
// refresh token that predates the session columns its values: family_id is
// the row's own id as a decimal string (each legacy row is its own family),
// sign_in_method is RefreshTokenSignInOIDC when the owner has any
// user_identities row and RefreshTokenSignInPassword otherwise, and
// session_started_at is created_at. Revoked and soft-deleted rows are
// included. The statements are raw UPDATEs, so updated_at is not touched.
//
// Only empty or NULL columns are written, so a row that already holds values
// is left alone and a second run matches no row.
func backfillRefreshTokenSessions(tx *gorm.DB) error {
	if err := tx.Exec("UPDATE refresh_tokens SET family_id = CAST(id AS CHAR) WHERE family_id = ''").Error; err != nil {
		return fmt.Errorf("refresh token session: failed to fill family_id: %w", err)
	}
	if err := tx.Exec("UPDATE refresh_tokens SET sign_in_method = ? WHERE sign_in_method = '' AND user_id IN (SELECT user_id FROM user_identities)",
		RefreshTokenSignInOIDC).Error; err != nil {
		return fmt.Errorf("refresh token session: failed to fill sign_in_method: %w", err)
	}
	if err := tx.Exec("UPDATE refresh_tokens SET sign_in_method = ? WHERE sign_in_method = ''",
		RefreshTokenSignInPassword).Error; err != nil {
		return fmt.Errorf("refresh token session: failed to fill sign_in_method: %w", err)
	}
	if err := tx.Exec("UPDATE refresh_tokens SET session_started_at = created_at WHERE session_started_at IS NULL").Error; err != nil {
		return fmt.Errorf("refresh token session: failed to fill session_started_at: %w", err)
	}
	return nil
}
