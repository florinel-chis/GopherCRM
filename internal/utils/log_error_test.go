package utils

import (
	"errors"
	"fmt"
	"net/textproto"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogError_OmitsQuotedInputAndKeepsDiagnostics(t *testing.T) {
	const canary = "privacy-canary@example.test"
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"domain validation", fmt.Errorf("%q is not a domain: %w", canary, apperrors.ErrValidation), "validation failed"},
		{"duplicate prompt", fmt.Errorf("prompt %q already exists: %w", canary, apperrors.ErrDuplicatePrompt), "a prompt with this text already exists"},
		{"mysql duplicate", &mysql.MySQLError{Number: 1062, Message: "Duplicate entry '" + canary + "' for key 'customers.email'"}, "Duplicate entry [redacted] (mysql_code=1062)"},
		{"wrapped mysql", fmt.Errorf("%q: %w", canary, &mysql.MySQLError{Number: 1062, Message: "Duplicate entry '" + canary + "'"}), "mysql_code=1062"},
		{"embedded quote", errors.New("Duplicate entry 'first'" + canary), "Duplicate entry [redacted]"},
		{"unmatched quote", errors.New("Duplicate entry '" + canary), "Duplicate entry [redacted]"},
		{"SMTP response", fmt.Errorf("send mail: %w", &textproto.Error{Code: 550, Msg: "5.1.1 " + canary + ": Recipient address rejected"}), "smtp_code=550"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := tt.err.Error()
			logged := LogError(tt.err)
			require.NotNil(t, logged)
			assert.NotContains(t, logged.Error(), canary)
			assert.Contains(t, logged.Error(), tt.want)
			assert.Equal(t, original, tt.err.Error())
		})
	}
}

func TestLogError_PreservesUnquotedDiagnostics(t *testing.T) {
	assert.Nil(t, LogError(nil))
	err := errors.New("database connection refused")
	assert.Same(t, err, LogError(err))
	assert.Same(t, apperrors.ErrNotFound, LogError(apperrors.ErrNotFound))
}
