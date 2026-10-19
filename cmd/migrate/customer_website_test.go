package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestCustomerWebsiteMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	for _, autoMigrateFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			server, err := sql.Open("mysql", migrationDSN(baseDSN))
			require.NoError(t, err)
			defer server.Close()
			name := fmt.Sprintf("gocrm_website_%d", time.Now().UnixNano())
			_, err = server.Exec("CREATE DATABASE " + name)
			require.NoError(t, err)
			defer server.Exec("DROP DATABASE " + name)
			dsn := baseDSN + name + "?multiStatements=true&parseTime=true"
			db, err := sql.Open("mysql", dsn)
			require.NoError(t, err)
			defer db.Close()
			driver, err := mysql.WithInstance(db, &mysql.Config{})
			require.NoError(t, err)
			path, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
			require.NoError(t, err)
			m, err := migrate.NewWithDatabaseInstance("file://"+path, "mysql", driver)
			require.NoError(t, err)
			defer m.Close()
			// Upgrade from the exact SQL head before website existed, with a real row.
			require.NoError(t, m.Migrate(20261017120000))
			_, err = db.Exec("INSERT INTO customers (first_name, last_name, email) VALUES ('Existing', 'Customer', 'existing@example.com')")
			require.NoError(t, err)
			appDB, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()
			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.Customer{}))
				_, err = db.Exec("UPDATE customers SET website = 'https://preserved.example'")
				require.NoError(t, err)
			}
			require.NoError(t, m.Up())
			var website string
			require.NoError(t, db.QueryRow("SELECT website FROM customers WHERE email = 'existing@example.com'").Scan(&website))
			if autoMigrateFirst {
				assert.Equal(t, "https://preserved.example", website)
			} else {
				assert.Empty(t, website)
			}
			_, err = db.Exec("UPDATE customers SET website = 'https://example.com'")
			require.NoError(t, err)
			require.ErrorIs(t, m.Up(), migrate.ErrNoChange)
			require.NoError(t, appDB.AutoMigrate(&models.Customer{}))
			require.NoError(t, db.QueryRow("SELECT website FROM customers WHERE email = 'existing@example.com'").Scan(&website))
			assert.Equal(t, "https://example.com", website)
			require.NoError(t, m.Steps(-1))
			assert.False(t, appDB.Migrator().HasColumn(&models.Customer{}, "website"))
			var email string
			require.NoError(t, db.QueryRow("SELECT email FROM customers").Scan(&email))
			assert.Equal(t, "existing@example.com", email)
			require.NoError(t, m.Up())
			require.NoError(t, db.QueryRow("SELECT website FROM customers").Scan(&website))
			assert.Empty(t, website, "rollback removes websites; reapplying starts empty")
		})
	}
}
