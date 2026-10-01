package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SERVER_HOST selects the interface the HTTP server binds to. These tests pin
// the default (empty host, every interface, ":8080" exactly as before the
// setting existed) and the two address shapes net.JoinHostPort has to get
// right: a plain IPv4 literal and a bracketed IPv6 literal.
//
// withCleanEnv does not clear SERVER_HOST or SERVER_PORT, so each test sets
// (or unsets) them via t.Setenv, which restores the caller's values when the
// test ends and keeps a developer shell's exports from leaking in.

func TestServerConfig_Address(t *testing.T) {
	cases := []struct {
		name     string
		host     string
		port     int
		expected string
	}{
		{name: "empty host binds every interface", host: "", port: 8080, expected: ":8080"},
		{name: "ipv4 loopback", host: "127.0.0.1", port: 8080, expected: "127.0.0.1:8080"},
		{name: "ipv6 loopback is bracketed", host: "::1", port: 8080, expected: "[::1]:8080"},
		{name: "hostname", host: "localhost", port: 9090, expected: "localhost:9090"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ServerConfig{Host: tc.host, Port: tc.port}
			assert.Equal(t, tc.expected, cfg.Address())
		})
	}
}

func TestLoad_ServerHostDefaultsToEveryInterface(t *testing.T) {
	// t.Setenv registers the restore; the unset makes the variables truly absent.
	t.Setenv("SERVER_HOST", "")
	t.Setenv("SERVER_PORT", "")
	require.NoError(t, os.Unsetenv("SERVER_HOST"))
	require.NoError(t, os.Unsetenv("SERVER_PORT"))

	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
	}, func() {
		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, "", cfg.Server.Host,
			"with SERVER_HOST unset the host should stay empty")
		assert.Equal(t, ":8080", cfg.Server.Address(),
			"the default listen address must be exactly what the server bound to before SERVER_HOST existed")
	})
}

func TestLoad_ServerHostIPv4(t *testing.T) {
	t.Setenv("SERVER_HOST", "127.0.0.1")
	t.Setenv("SERVER_PORT", "")
	require.NoError(t, os.Unsetenv("SERVER_PORT"))

	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
	}, func() {
		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, "127.0.0.1", cfg.Server.Host)
		assert.Equal(t, "127.0.0.1:8080", cfg.Server.Address())
	})
}

func TestLoad_ServerHostIPv6(t *testing.T) {
	t.Setenv("SERVER_HOST", "::1")
	t.Setenv("SERVER_PORT", "")
	require.NoError(t, os.Unsetenv("SERVER_PORT"))

	withCleanEnv(t, map[string]string{
		"JWT_SECRET": validSecret(),
	}, func() {
		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, "::1", cfg.Server.Host)
		assert.Equal(t, "[::1]:8080", cfg.Server.Address(),
			"an IPv6 literal must be bracketed or net.Listen reads the last group as the port")
	})
}
