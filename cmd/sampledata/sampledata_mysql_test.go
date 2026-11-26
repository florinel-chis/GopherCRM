package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/database"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSampleDataMySQL runs the generator against the TEST_MYSQL_DSN server
// (MySQL 8 in the backend job, MariaDB 10.11 in the database gates). It
// skips without the variable and fails when the server is unreachable. The
// command creates the uniquely named database itself; the test drops it.
func TestSampleDataMySQL(t *testing.T) {
	server := testsupport.MySQLFromEnv(t)
	setTestEnv(t)

	suffix := make([]byte, 6)
	_, err := rand.Read(suffix)
	require.NoError(t, err)
	name := "gocrm_sampledata_" + hex.EncodeToString(suffix)

	admin, err := sql.Open("mysql", server.FormatDSN())
	require.NoError(t, err)
	require.NoError(t, admin.Ping(), "TEST_MYSQL_DSN server is unreachable")
	t.Cleanup(func() {
		_, err := admin.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		assert.NoError(t, err)
		_ = admin.Close()
	})

	target := server.Clone()
	target.DBName = name
	dsn := target.FormatDSN()
	args := []string{"--profile", "halvenmark", "--seed", "1", "--scale", testScale, "--dsn", dsn}

	out, err := generate(t, args...)
	require.NoError(t, err)
	assert.NotContains(t, out, dsn)
	if server.Passwd != "" {
		assert.NotContains(t, out, server.Passwd, "stdout carries the DSN password")
	}
	assert.Contains(t, out, name)

	opts, err := parseOptions(args)
	require.NoError(t, err)
	dbCfg, err := databaseConfig(opts)
	require.NoError(t, err)
	db, err := database.Open(dbCfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	assertScaledCounts(t, db)
	assert.Empty(t, scanReservedNames(t, db))
	assert.NoError(t, signIn(t, db, "admin@halvenmark.example", halvenmarkProfile().DemoPassword))

	users, leads := count(t, db, "users", ""), count(t, db, "leads", "")
	_, err = generate(t, args...)
	require.Error(t, err, "a second run without --force")
	assert.Contains(t, err.Error(), "--force")
	if server.Passwd != "" {
		assert.NotContains(t, err.Error(), server.Passwd)
	}
	assert.Equal(t, users, count(t, db, "users", ""))
	assert.Equal(t, leads, count(t, db, "leads", ""))
}
