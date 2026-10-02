package database

import (
	"bytes"
	"context"
	"errors"
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
	out := traceInto(t, &mysql.MySQLError{
		Number:   1062,
		SQLState: [5]byte{'2', '3', '0', '0', '0'},
		Message:  "Duplicate entry 'alice@example.com' for key 'users.email'",
	})
	assert.Contains(t, out, "1062", "the error number must survive redaction")
	assert.Contains(t, out, "VALUES (?)", "the statement is still logged with placeholders")
	assert.NotContains(t, out, "alice@example.com", "the driver's error text must not carry the value")
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
	out := traceInto(t, gorm.ErrRecordNotFound)
	assert.Empty(t, out, "record-not-found must not be logged as a failed statement")
}
