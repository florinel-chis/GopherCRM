package utils

import (
	"errors"
	"fmt"
	"net/textproto"
	"strings"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/go-sql-driver/mysql"
)

// LogError returns a log-only representation of an error. Validation errors
// with custom field names can provide a safe summary through LogMessage. Other
// errors keep their diagnostic prefix, but quoted values are omitted: database
// errors and domain validation often echo user input in those values. Cutting
// at the first quote also handles embedded or unmatched quotes safely.
// Unquoted diagnostics are retained. Callers must return the original error,
// preserving its identity and details.
func LogError(err error) error {
	if err == nil {
		return nil
	}
	var summary interface{ LogMessage() string }
	if errors.As(err, &summary) {
		return errors.New(summary.LogMessage())
	}

	// SMTP responses can repeat recipient addresses without quoting them.
	// Keep the protocol code while omitting the untrusted response text.
	var smtpError *textproto.Error
	if errors.As(err, &smtpError) {
		return fmt.Errorf("SMTP request failed (smtp_code=%d)", smtpError.Code)
	}

	message := err.Error()
	quote := strings.IndexAny(message, "'\"")
	if quote < 0 {
		return err
	}
	message = message[:quote] + "[redacted]"
	// Keep the error's category even when the value precedes its explanation.
	for _, category := range []error{
		apperrors.ErrDuplicatePrompt,
		apperrors.ErrDuplicateCompanyDomain,
		apperrors.ErrDuplicateLabelName,
		apperrors.ErrInvalidLabelColor,
		apperrors.ErrValidation,
		apperrors.ErrNotFound,
	} {
		if errors.Is(err, category) {
			message += ": " + category.Error()
			break
		}
	}
	var driverError *mysql.MySQLError
	if errors.As(err, &driverError) {
		message += fmt.Sprintf(" (mysql_code=%d)", driverError.Number)
	}
	return errors.New(message)
}
