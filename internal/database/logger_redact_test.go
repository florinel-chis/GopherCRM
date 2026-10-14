package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// traceInto runs one failed statement through the SQL logger at LOG_LEVEL=info
// and returns what it wrote.
func traceInto(t *testing.T, err error) string {
	t.Helper()
	var buf bytes.Buffer
	l := newGormLoggerTo(&buf, "info")
	l.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "INSERT INTO `users` (`email`) VALUES (?)", 0
	}, err)
	return buf.String()
}

// TestGormLogger_RedactsValuesInDriverErrors: MySQL and MariaDB put the
// offending value into the duplicate-entry message itself, so a registration
// with an existing email would otherwise print the address at every level.
func TestGormLogger_RedactsValuesInDriverErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		message string
	}{
		{"plain", "Duplicate entry 'connor@example.invalid' for key 'users.email'"},
		{"apostrophe", "Duplicate entry 'o'connor@example.invalid' for key 'users.email'"},
		{"doubled_quote", "Duplicate entry 'o''connor@example.invalid' for key 'users.email'"},
		{"escaped_quote", "Duplicate entry 'o\\'connor@example.invalid' for key 'users.email'"},
		{"multiline", "Duplicate entry 'o'\nconnor@example.invalid' for key 'users.email'"},
		{"unmatched_quote", "Duplicate entry 'connor@example.invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := traceInto(t, &mysql.MySQLError{
				Number:   1062,
				SQLState: [5]byte{'2', '3', '0', '0', '0'},
				Message:  tt.message,
			})
			assert.Contains(t, out, "Error 1062 (23000): Duplicate entry",
				"the error number, SQL state and category must survive redaction")
			assert.Contains(t, out, "VALUES (?)", "the statement is still logged with placeholders")
			assert.NotContains(t, out, "connor@example.invalid",
				"quoted values must stay private even when their delimiters are ambiguous")
		})
	}
}

// TestGormLogger_KeepsPlainErrorText: errors without quoted values are logged
// as they are, so the SQLite message and connection errors stay readable.
func TestGormLogger_KeepsPlainErrorText(t *testing.T) {
	out := traceInto(t, errors.New("UNIQUE constraint failed: users.email"))
	assert.Contains(t, out, "UNIQUE constraint failed: users.email")
}

// TestGormLogger_IgnoresRecordNotFound: a First() miss is a 404 for the
// service layer, not a database error, and must not fill the log at Warn.
func TestGormLogger_IgnoresRecordNotFound(t *testing.T) {
	for _, err := range []error{
		gorm.ErrRecordNotFound,
		fmt.Errorf("lookup for 'connor@example.invalid': %w", gorm.ErrRecordNotFound),
	} {
		out := traceInto(t, err)
		assert.Empty(t, out, "record-not-found must not be logged as a failed statement")
	}
}
