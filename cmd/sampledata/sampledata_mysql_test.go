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
	"gorm.io/gorm"
)

// TestSampleDataMySQL runs the generator against the TEST_MYSQL_DSN server
// (MySQL 8 in the backend job, MariaDB 10.11 in the database gates), one
// subtest per profile at testScale. It skips without the variable and fails
// when the server is unreachable. The command creates each uniquely named
// database itself; the test drops it.
func TestSampleDataMySQL(t *testing.T) {
	server := testsupport.MySQLFromEnv(t)
	setTestEnv(t)

	admin, err := sql.Open("mysql", server.FormatDSN())
	require.NoError(t, err)
	require.NoError(t, admin.Ping(), "TEST_MYSQL_DSN server is unreachable")
	t.Cleanup(func() { _ = admin.Close() })

	// generateMySQL generates profile into a fresh, uniquely named database
	// and returns the command's arguments, its stdout and a connection.
	generateMySQL := func(t *testing.T, profile string) ([]string, string, *gorm.DB) {
		suffix := make([]byte, 6)
		_, err := rand.Read(suffix)
		require.NoError(t, err)
		name := "gocrm_sampledata_" + hex.EncodeToString(suffix)
		t.Cleanup(func() {
			_, err := admin.Exec("DROP DATABASE IF EXISTS `" + name + "`")
			assert.NoError(t, err)
		})

		target := server.Clone()
		target.DBName = name
		dsn := target.FormatDSN()
		args := []string{"--profile", profile, "--seed", "1", "--scale", testScale, "--dsn", dsn}

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
		return args, out, db
	}

	t.Run("halvenmark", func(t *testing.T) {
		args, _, db := generateMySQL(t, "halvenmark")

		assertScaledCounts(t, db)
		assert.Empty(t, scanReservedNames(t, db))
		assert.NoError(t, signIn(t, db, "admin@halvenmark.example", halvenmarkProfile().DemoPassword))

		users, leads := count(t, db, "users", ""), count(t, db, "leads", "")
		_, err := generate(t, args...)
		require.Error(t, err, "a second run without --force")
		assert.Contains(t, err.Error(), "--force")
		if server.Passwd != "" {
			assert.NotContains(t, err.Error(), server.Passwd)
		}
		assert.Equal(t, users, count(t, db, "users", ""))
		assert.Equal(t, leads, count(t, db, "leads", ""))
	})

	t.Run("pelicroft", func(t *testing.T) {
		_, out, db := generateMySQL(t, "pelicroft")

		assertPelicroftFormTickets(t, db)
		assertPelicroftSpam(t, db, out)
		assertPelicroftWholesale(t, db)
		assertPelicroftAutomation(t, db)
		assertPelicroftTimestamps(t, db)
		assertPelicroftSeasonality(t, db)
		assertPelicroftOffByDefault(t, db, out)
		assert.Empty(t, scanReservedNames(t, db))
		assert.NoError(t, signIn(t, db, "admin@pelicroft.example", pelicroftProfile().DemoPassword))
	})
}
