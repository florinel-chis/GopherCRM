package service

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// Mail product name (#292).
//
// The password reset mail (subject and first body line), the quote mail
// subject and the ticket reply mail subject name the product. The name is the
// stored general.company_name configuration entry, read on every send with no
// transaction held, or "GopherCRM" when that entry is missing, blank,
// unreadable or holds a value that cannot go in a mail header. Nothing here
// writes, seeds or caches a configuration value, and a failed read never
// blocks a send.

const (
	// ConfigGeneralCompanyName is the configuration key the mail product name
	// is read from. It already exists (seeded "GopherCRM"); no key is added.
	ConfigGeneralCompanyName = "general.company_name"
	// DefaultMailProductName is the fallback product name of every mail.
	DefaultMailProductName = "GopherCRM"
	// MailProductNameMaxRunes bounds an accepted stored name, counted in runes
	// after trimming. A longer value falls back to DefaultMailProductName.
	MailProductNameMaxRunes = 100
)

// Fallback reasons carried by the Warn line of ResolveMailProductName.
const (
	mailProductNameReasonLookupFailed = "lookup_failed"
	mailProductNameReasonInvalidValue = "invalid_value"
)

// MailProductNameSource is the one configuration read the mail product name
// needs. ConfigurationService satisfies it; tests hand in a stub.
type MailProductNameSource interface {
	GetString(key string) (string, error)
}

// ResolveMailProductName returns the product name mail subjects and the reset
// greeting use. It calls source.GetString(general.company_name) on every call
// (no cache) and returns the value trimmed of surrounding whitespace when it
// is non-blank, valid UTF-8, free of control characters (CR and LF included)
// and at most MailProductNameMaxRunes runes. Otherwise it returns
// DefaultMailProductName: for a nil source, any read error (a missing key
// included), a blank value, invalid UTF-8, a control character or an
// over-long value.
//
// A fallback caused by a read error or an invalid value logs one Warn line
// with config_key and reason (and, for a read error, the error through
// utils.LogError); a nil source or a blank value logs nothing. No log line
// carries the stored value, accepted or rejected.
func ResolveMailProductName(source MailProductNameSource) string {
	if isNilMailProductNameSource(source) {
		return DefaultMailProductName
	}
	value, err := source.GetString(ConfigGeneralCompanyName)
	if err != nil {
		entry := mailProductNameLogger().WithField("reason", mailProductNameReasonLookupFailed).
			WithError(utils.LogError(err))
		if errors.Is(err, apperrors.ErrNotFound) {
			entry = entry.WithField("missing", true)
		}
		entry.Warn("Mail product name unreadable; using the default")
		return DefaultMailProductName
	}
	name := strings.TrimSpace(value)
	if name == "" {
		return DefaultMailProductName
	}
	if !validMailProductName(name) {
		mailProductNameLogger().WithField("reason", mailProductNameReasonInvalidValue).
			Warn("Mail product name unusable in a mail header; using the default")
		return DefaultMailProductName
	}
	return name
}

// validMailProductName reports whether a trimmed, non-blank name is valid
// UTF-8, holds no control character and is at most MailProductNameMaxRunes
// runes long.
func validMailProductName(name string) bool {
	if !utf8.ValidString(name) {
		return false
	}
	if utf8.RuneCountInString(name) > MailProductNameMaxRunes {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// isNilMailProductNameSource also catches a typed nil pointer wrapped in the
// interface, so a service wired with a nil *configurationService falls back
// instead of panicking.
func isNilMailProductNameSource(source MailProductNameSource) bool {
	if source == nil {
		return true
	}
	if s, ok := source.(*configurationService); ok && s == nil {
		return true
	}
	return false
}

func mailProductNameLogger() *logrus.Entry {
	base := utils.Logger
	if base == nil {
		base = logrus.StandardLogger()
	}
	return base.WithField("config_key", ConfigGeneralCompanyName)
}
