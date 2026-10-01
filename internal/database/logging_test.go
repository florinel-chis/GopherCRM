//go:build unix

package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	"gorm.io/gorm"
)

// probeEmail is a synthetic address in the reserved .invalid domain. It stands
// in for the personal data a real insert carries (emails, names, token hashes).
const probeEmail = "sql-log-probe@example.invalid"

// logProbe is the smallest model whose insert binds a personal value.
type logProbe struct {
	ID    uint
	Email string
}

// captureProcessOutput runs fn with file descriptors 1 and 2 redirected to a
// temporary file and returns what was written. Redirecting the descriptors,
// rather than swapping a writer variable, captures what an operator sees on
// stdout and stderr (the systemd journal in production), whichever writer the
// GORM logger was built with.
func captureProcessOutput(t *testing.T, fn func()) string {
	t.Helper()

	out, err := os.CreateTemp(t.TempDir(), "process-output-*.log")
	require.NoError(t, err)
	defer out.Close()

	fds := []int{1, 2}
	saved := make([]int, 0, len(fds))
	restore := func() {
		for i, fd := range saved {
			_ = unix.Dup2(fd, fds[i])
			_ = unix.Close(fd)
		}
		saved = saved[:0]
	}
	func() {
		defer restore()
		for _, fd := range fds {
			dup, err := unix.Dup(fd)
			require.NoError(t, err)
			saved = append(saved, dup)
			require.NoError(t, unix.Dup2(int(out.Fd()), fd))
		}
		fn()
	}()

	captured, err := os.ReadFile(out.Name())
	require.NoError(t, err)
	return string(captured)
}

// openWithLogLevel opens a fresh SQLite file the way the application does:
// configuration from config.Load with the given LOG_LEVEL, then database.Open.
func openWithLogLevel(t *testing.T, level string) *gorm.DB {
	t.Helper()

	t.Setenv("JWT_SECRET", "test-only-jwt-secret-of-sufficient-length")
	t.Setenv("DB_DRIVER", config.DriverSQLite)
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "sql-log-"+level+".db"))
	t.Setenv("LOG_LEVEL", level)
	t.Setenv("LOG_FORMAT", "json")

	cfg, err := config.Load()
	require.NoError(t, err)

	db, err := Open(&cfg.Database)
	require.NoError(t, err)
	t.Cleanup(func() { closeDB(t, db) })
	require.NoError(t, db.AutoMigrate(&logProbe{}))
	return db
}

func insertProbe(t *testing.T, db *gorm.DB) string {
	t.Helper()
	return captureProcessOutput(t, func() {
		require.NoError(t, db.Create(&logProbe{Email: probeEmail}).Error)
	})
}

// TestOpen_InfoLevelKeepsBoundValuesOutOfTheLog: with LOG_LEVEL=info, an
// insert that binds an email address must not put that address on stdout or
// stderr.
func TestOpen_InfoLevelKeepsBoundValuesOutOfTheLog(t *testing.T) {
	db := openWithLogLevel(t, "info")

	output := insertProbe(t, db)

	assert.NotContains(t, output, probeEmail,
		"LOG_LEVEL=info: the SQL log must not carry bound parameter values")
}

// TestOpen_DebugLevelLogsStatementsWithPlaceholders: with LOG_LEVEL=debug the
// statement is logged, with placeholders in place of the bound values.
func TestOpen_DebugLevelLogsStatementsWithPlaceholders(t *testing.T) {
	db := openWithLogLevel(t, "debug")

	output := insertProbe(t, db)

	assert.Contains(t, output, "INSERT INTO `log_probes`",
		"LOG_LEVEL=debug: the statement must be logged")
	assert.Contains(t, output, "VALUES (?)",
		"LOG_LEVEL=debug: the statement must show placeholders")
	assert.NotContains(t, output, probeEmail,
		"LOG_LEVEL=debug: the SQL log must not carry bound parameter values")
}

// TestOpen_InfoLevelStillLogsFailedStatements: info does not mean silence.
// A failing statement is still reported, with placeholders in place of the
// bound values.
func TestOpen_InfoLevelStillLogsFailedStatements(t *testing.T) {
	db := openWithLogLevel(t, "info")

	output := captureProcessOutput(t, func() {
		err := db.Exec("INSERT INTO missing_probe_table (email) VALUES (?)", probeEmail).Error
		require.Error(t, err)
	})

	assert.Contains(t, output, "missing_probe_table",
		"LOG_LEVEL=info: a failed statement must still be logged")
	assert.NotContains(t, output, probeEmail,
		"LOG_LEVEL=info: the SQL log must not carry bound parameter values")
}
