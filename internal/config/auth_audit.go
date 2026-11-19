package config

import (
	"errors"
	"strconv"
	"strings"
)

// AuthAuditConfig configures the sign-in audit log (#454).
type AuthAuditConfig struct {
	// RetentionDays is how long an auth_audit_log row is kept, from
	// AUTH_AUDIT_RETENTION_DAYS. See parseAuthAuditRetentionDays.
	RetentionDays int
}

// Bounds of AUTH_AUDIT_RETENTION_DAYS. The ceiling keeps the age inside what
// a time.Duration can hold.
const (
	AuthAuditRetentionDaysDefault = 90
	AuthAuditRetentionDaysMax     = 36500
)

// parseAuthAuditRetentionDays reads AUTH_AUDIT_RETENTION_DAYS. An unset,
// empty, unparseable, zero or negative value keeps the default of 90 days: the
// audit log is always pruned, so a typo can neither keep it forever nor turn
// it off. A value above the ceiling, including one too large for an int, is
// lowered to it.
func parseAuthAuditRetentionDays(raw string) int {
	raw = strings.TrimSpace(raw)
	days, err := strconv.Atoi(raw)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) && raw != "" && !strings.HasPrefix(raw, "-") {
			return AuthAuditRetentionDaysMax
		}
		return AuthAuditRetentionDaysDefault
	}
	switch {
	case days <= 0:
		return AuthAuditRetentionDaysDefault
	case days > AuthAuditRetentionDaysMax:
		return AuthAuditRetentionDaysMax
	default:
		return days
	}
}
