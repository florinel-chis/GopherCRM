package models

import "time"

// OIDCLoginAttempt is one started OpenID Connect sign-in (#435): what the
// callback needs to finish it. Only the HMAC-SHA256 of the state is stored,
// keyed like the other opaque tokens; the raw state exists in the
// authorization URL alone. The row is single-use: before it talks to the
// issuer, the callback reads the row by its state hash and then spends it with
// one conditional update by id (used_at IS NULL AND expires_at > now). The
// nonce and the PKCE verifier are blanked (set to the empty string) when that
// update spends the row, or when the callback finds the row expired (#453).
// No foreign key, no user data; the stale bulk operation sweep prunes rows one
// day after their creation.
type OIDCLoginAttempt struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	// hex(HMAC-SHA256(state)); never serialised.
	StateHash string `gorm:"type:varchar(64);not null;uniqueIndex:idx_oidc_login_attempts_state_hash" json:"-"`
	// The nonce the ID token must carry.
	Nonce string `gorm:"type:varchar(64);not null" json:"-"`
	// The PKCE code verifier of the authorization request.
	CodeVerifier string `gorm:"type:varchar(128);not null" json:"-"`
	// The issuer the attempt was started against; the callback refuses the
	// attempt when the configured issuer has changed since.
	Issuer string `gorm:"type:varchar(2048);not null" json:"issuer"`
	// When the attempt stops being usable (ten minutes after the start).
	ExpiresAt time.Time `gorm:"not null" json:"expires_at"`
	// When the callback spent the attempt; NULL while unused.
	UsedAt *time.Time `json:"used_at,omitempty"`
}

// TableName pins the table name.
func (OIDCLoginAttempt) TableName() string { return "oidc_login_attempts" }
