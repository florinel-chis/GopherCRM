package models

import "time"

// Sign-in methods recorded on a refresh token. The values equal the sign-in
// audit vocabulary's AuthAuditMethodPassword and AuthAuditMethodOIDC.
const (
	RefreshTokenSignInPassword = "password"
	RefreshTokenSignInOIDC     = "oidc"
)

// RefreshToken represents a refresh token for JWT authentication
type RefreshToken struct {
	BaseModel
	UserID    uint      `gorm:"not null" json:"user_id"`
	User      User      `gorm:"foreignKey:UserID" json:"user,omitempty"`
	TokenHash string    `gorm:"not null;uniqueIndex;type:varchar(255)" json:"-"`
	ExpiresAt time.Time `gorm:"not null" json:"expires_at"`
	Revoked   bool      `gorm:"column:is_revoked;default:false" json:"revoked"`
	// FamilyID is shared by every token of one rotation chain: a sign-in
	// starts a family (32 lowercase hex characters) and each rotation copies
	// it to the successor. A row that predates the column holds its own id as
	// a decimal string (migration 20261129120004_refresh_token_session).
	FamilyID string `gorm:"type:varchar(64);not null;default:'';index:idx_refresh_tokens_family_id" json:"-"`
	// SignInMethod is how the session of this chain signed in:
	// RefreshTokenSignInPassword or RefreshTokenSignInOIDC. Rotation copies it.
	SignInMethod string `gorm:"type:varchar(16);not null;default:''" json:"-"`
	// SessionStartedAt is when the sign-in that started this chain happened.
	// Rotation copies it. Nullable only so the column can be added to a
	// populated table; the startup step fills legacy rows with created_at.
	SessionStartedAt *time.Time `json:"-"`
	// ProviderSessionHash refers to the identity provider's session a single
	// sign-on sign-in started in: the keyed hash of the issuer, a newline and
	// the ID token's sid, never the sid itself; '' for a password session and
	// for a sign-in whose ID token carried no sid. Rotation and the
	// single sign-on renewal of a password change copy it. Front-channel
	// sign-out finds the sessions to end by it (migration
	// 20261129120008_refresh_token_provider_session).
	ProviderSessionHash string `gorm:"type:varchar(64);not null;default:'';index:idx_refresh_tokens_provider_session_hash" json:"-"`
}
