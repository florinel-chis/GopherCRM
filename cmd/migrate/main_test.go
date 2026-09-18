package main

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckCommandSupported_SQLiteRejectsDatabaseCommands(t *testing.T) {
	for _, command := range []string{"up", "down", "goto", "drop", "force", "version"} {
		t.Run(command, func(t *testing.T) {
			err := checkCommandSupported(command, config.DriverSQLite)
			require.ErrorIs(t, err, ErrSQLiteUnsupported)
			assert.Equal(t,
				"SQL migrations are MySQL-only; SQLite schemas are created by auto-migration at startup",
				err.Error())
		})
	}
}

func TestCheckCommandSupported_SQLiteAllowsCreate(t *testing.T) {
	assert.NoError(t, checkCommandSupported(commandCreate, config.DriverSQLite))
}

func TestCheckCommandSupported_MySQLAllowsEveryCommand(t *testing.T) {
	for _, command := range []string{"up", "down", "goto", "drop", "force", "version", commandCreate, "bogus"} {
		assert.NoError(t, checkCommandSupported(command, config.DriverMySQL), "command %q", command)
	}
}
