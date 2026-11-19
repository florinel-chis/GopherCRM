package models

import "time"

// Widths of the auth_audit_log columns.
const (
	AuthAuditMethodMaxLength        = 16
	AuthAuditOutcomeMaxLength       = 16
	AuthAuditReasonMaxLength        = 64
	AuthAuditRequestIDMaxLength     = 64
	AuthAuditSubjectRefMaxLength    = 64
	AuthAuditClientNetworkMaxLength = 64
	AuthAuditUserAgentMaxLength     = 64
)

// AuthAuditLog is one sign-in outcome (#454): a password login, a single
// sign-on callback, a refresh, or a failed API-key authentication. The row is
// pseudonymous: it holds ids, a stable reason code, the client's /24 (IPv4) or
// /48 (IPv6) network, a user-agent family from a fixed vocabulary and, for a
// single sign-on refusal after the ID token verified, an HMAC of the issuer
// and subject. It never holds an email, a name, a subject, a token, a code, a
// state, a nonce, a password, a full IP address or a full user agent. No
// foreign key and no soft delete: erasure leaves the rows alone and the stale
// bulk operation sweep deletes them after AUTH_AUDIT_RETENTION_DAYS.
type AuthAuditLog struct {
	ID uint `gorm:"primarykey" json:"id"`
	// Written explicitly in UTC.
	CreatedAt time.Time `gorm:"not null;index:idx_auth_audit_log_created_at" json:"created_at"`
	// password, oidc, refresh or api_key.
	Method string `gorm:"type:varchar(16);not null" json:"method"`
	// success or refused.
	Outcome string `gorm:"type:varchar(16);not null" json:"outcome"`
	// The stable reason code of a refusal; empty on success.
	Reason string `gorm:"type:varchar(64);not null" json:"reason"`
	// The account the outcome concerns, when it is known.
	UserID *uint `gorm:"index:idx_auth_audit_log_user_id" json:"user_id,omitempty"`
	// The oidc_login_attempts row of a single sign-on callback, when claimed.
	AttemptID *uint `json:"attempt_id,omitempty"`
	// The request id, when it matched ^[A-Za-z0-9._:-]{1,64}$; empty otherwise.
	RequestID string `gorm:"type:varchar(64);not null" json:"request_id"`
	// hex(HMAC-SHA256) of the refused issuer and subject; NULL otherwise.
	SubjectRef *string `gorm:"type:varchar(64);index:idx_auth_audit_log_subject_ref" json:"subject_ref,omitempty"`
	// JSON arrays of the roles and groups claim values used for the mapping.
	ClaimRoles  *string `gorm:"type:text" json:"claim_roles,omitempty"`
	ClaimGroups *string `gorm:"type:text" json:"claim_groups,omitempty"`
	// The client's network: a.b.c.0/24 or an IPv6 /48; empty when unknown.
	ClientNetwork string `gorm:"type:varchar(64);not null" json:"client_network"`
	// "<Browser> on <OS>" from a fixed vocabulary; empty without a user agent.
	UserAgentFamily string `gorm:"type:varchar(64);not null" json:"user_agent_family"`
}

// TableName pins the table name.
func (AuthAuditLog) TableName() string { return "auth_audit_log" }
