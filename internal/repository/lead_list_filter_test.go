package repository

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// #426: ListByFilter and CountByFilter share whereLeadListFilter. These roots
// seed leads across five statuses, two owners and two classifications, a
// search token reached through company on some rows and only through notes on
// one row of another status and owner, tied emails and one erased
// (soft-deleted) lead, then check the rows and the count of every filter. The
// notes-only row proves that the search alternatives stay parenthesised: were
// they not, the OR would let it through a status or an owner filter. The roots
// run on a file-backed SQLite database with one connection and, when
// TEST_MYSQL_DSN is set, on a disposable MySQL schema; the MySQL root skips
// without the variable and fails when it is unreachable.

func TestLeadListFilterRepositorySQLite(t *testing.T) {
	runLeadListFilterRepository(t, openLeadListFilterDB(t, false))
}

func TestLeadListFilterRepositoryMySQL(t *testing.T) {
	runLeadListFilterRepository(t, openLeadListFilterDB(t, true))
}

func openLeadListFilterDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{Logger: logger.Discard}
	var db *gorm.DB
	if !useMySQL {
		var err error
		dsn := filepath.Join(t.TempDir(), "lead_list_filter.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
		db, err = gorm.Open(sqlite.Open(dsn), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	} else {
		raw := os.Getenv("TEST_MYSQL_DSN")
		if raw == "" {
			t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
		}
		dsnCfg, err := mysqldriver.ParseDSN(raw)
		require.NoError(t, err)
		dsnCfg.DBName = ""
		dsnCfg.ParseTime = true
		dsnCfg.Timeout, dsnCfg.ReadTimeout, dsnCfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
		server, err := sql.Open("mysql", dsnCfg.FormatDSN())
		require.NoError(t, err)
		name := fmt.Sprintf("gocrm_lead_list_filter_%d", time.Now().UnixNano())
		t.Cleanup(func() {
			_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
			_ = server.Close()
		})
		_, err = server.Exec("CREATE DATABASE `" + name + "`")
		require.NoError(t, err, "TEST_MYSQL_DSN is configured but the server is unavailable")
		dsnCfg.DBName = name
		db, err = gorm.Open(gormmysql.Open(dsnCfg.FormatDSN()), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{}))
	return db
}

const leadFilterToken = "tok426"

func runLeadListFilterRepository(t *testing.T, db *gorm.DB) {
	owner1 := &models.User{Email: "owner1@lead-filter.test", Password: "x", FirstName: "One", LastName: "Owner", Role: models.RoleSales, IsActive: true}
	owner2 := &models.User{Email: "owner2@lead-filter.test", Password: "x", FirstName: "Two", LastName: "Owner", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(owner1).Error)
	require.NoError(t, db.Create(owner2).Error)

	seed := func(first, email, company, notes string, owner *models.User, status models.LeadStatus, class models.LeadClassification) uint {
		t.Helper()
		lead := &models.Lead{FirstName: first, LastName: "Lead", Email: email, Company: company, Notes: notes,
			OwnerID: owner.ID, Status: status, Classification: class}
		require.NoError(t, db.Create(lead).Error)
		return lead.ID
	}
	// A and B match the token in company; C matches it only in notes and
	// differs from A in status and owner.
	a := seed("A", "a@lead-filter.test", "acme "+leadFilterToken, "", owner1, models.LeadStatusQualified, models.LeadClassificationLead)
	b := seed("B", "b@lead-filter.test", leadFilterToken+" gmbh", "", owner2, models.LeadStatusQualified, models.LeadClassificationHotLead)
	c := seed("C", "c@lead-filter.test", "plain", "met "+leadFilterToken+" at the fair", owner2, models.LeadStatusContacted, models.LeadClassificationLead)
	// D and E share an email so the sort needs the id tie-break.
	d := seed("D", "same@lead-filter.test", "other", "", owner1, models.LeadStatusQualified, models.LeadClassificationLead)
	e := seed("E", "same@lead-filter.test", "other", "", owner2, models.LeadStatusQualified, models.LeadClassificationHotLead)
	f := seed("F", "f@lead-filter.test", "other", "", owner1, models.LeadStatusNew, models.LeadClassificationHotLead)
	g := seed("G", "g@lead-filter.test", "other", "", owner1, models.LeadStatusContacted, models.LeadClassificationLead)
	h := seed("H", "h@lead-filter.test", "other", "", owner2, models.LeadStatusUnqualified, models.LeadClassificationLead)
	erased := seed("Erased", "erased@lead-filter.test", "gone "+leadFilterToken, "", owner1, models.LeadStatusQualified, models.LeadClassificationLead)
	require.NoError(t, db.Delete(&models.Lead{}, erased).Error)

	repo := NewLeadRepository(db)
	ids := func(leads []models.Lead) []uint {
		out := make([]uint, 0, len(leads))
		for _, l := range leads {
			out = append(out, l.ID)
		}
		return out
	}
	check := func(t *testing.T, filter LeadListFilter, offset, limit int, sortBy, sortOrder string, wantRows []uint, wantCount int64) {
		t.Helper()
		rows, err := repo.ListByFilter(filter, offset, limit, sortBy, sortOrder, "Owner")
		require.NoError(t, err)
		assert.Equal(t, wantRows, ids(rows))
		for _, row := range rows {
			assert.NotEqual(t, erased, row.ID, "an erased lead is never returned")
			assert.Equal(t, row.OwnerID, row.Owner.ID, "the owner is preloaded")
		}
		count, err := repo.CountByFilter(filter)
		require.NoError(t, err)
		assert.Equal(t, wantCount, count)
	}
	o1 := owner1.ID

	t.Run("status alone", func(t *testing.T) {
		check(t, LeadListFilter{Status: models.LeadStatusQualified}, 0, 20, "", "", []uint{a, b, d, e}, 4)
		check(t, LeadListFilter{Status: models.LeadStatusContacted}, 0, 20, "", "", []uint{c, g}, 2)
		check(t, LeadListFilter{Status: models.LeadStatusNew}, 0, 20, "", "", []uint{f}, 1)
		check(t, LeadListFilter{Status: models.LeadStatusUnqualified}, 0, 20, "", "", []uint{h}, 1)
		check(t, LeadListFilter{Status: models.LeadStatusConverted}, 0, 20, "", "", []uint{}, 0)
	})

	t.Run("status and search keep the OR parenthesised", func(t *testing.T) {
		check(t, LeadListFilter{Status: models.LeadStatusQualified, Search: leadFilterToken}, 0, 20, "", "", []uint{a, b}, 2)
		check(t, LeadListFilter{Status: models.LeadStatusContacted, Search: leadFilterToken}, 0, 20, "", "", []uint{c}, 1)
	})

	t.Run("owner and search keep the OR parenthesised", func(t *testing.T) {
		check(t, LeadListFilter{OwnerID: &o1, Search: leadFilterToken}, 0, 20, "", "", []uint{a}, 1)
	})

	t.Run("status and classification", func(t *testing.T) {
		check(t, LeadListFilter{Status: models.LeadStatusQualified, Classification: models.LeadClassificationLead}, 0, 20, "", "", []uint{a, d}, 2)
		check(t, LeadListFilter{Status: models.LeadStatusQualified, Classification: models.LeadClassificationHotLead}, 0, 20, "", "", []uint{b, e}, 2)
	})

	t.Run("status and owner", func(t *testing.T) {
		check(t, LeadListFilter{Status: models.LeadStatusQualified, OwnerID: &o1}, 0, 20, "", "", []uint{a, d}, 2)
		none := uint(0)
		check(t, LeadListFilter{Status: models.LeadStatusQualified, OwnerID: &none}, 0, 20, "", "", []uint{}, 0)
	})

	t.Run("every predicate together", func(t *testing.T) {
		check(t, LeadListFilter{Status: models.LeadStatusQualified, OwnerID: &o1, Classification: models.LeadClassificationLead, Search: leadFilterToken},
			0, 20, "", "", []uint{a}, 1)
	})

	t.Run("status with the email sort and the id tie-break", func(t *testing.T) {
		check(t, LeadListFilter{Status: models.LeadStatusQualified}, 0, 20, "email", "desc", []uint{e, d, b, a}, 4)
		check(t, LeadListFilter{Status: models.LeadStatusQualified}, 0, 20, "email", "asc", []uint{a, b, d, e}, 4)
	})

	t.Run("paging applies after the filter", func(t *testing.T) {
		check(t, LeadListFilter{Status: models.LeadStatusQualified}, 1, 1, "", "", []uint{b}, 4)
		check(t, LeadListFilter{Status: models.LeadStatusQualified}, 1, 2, "email", "desc", []uint{d, b}, 4)
		check(t, LeadListFilter{Status: models.LeadStatusQualified}, 4, 2, "", "", []uint{}, 4)
	})

	t.Run("an unknown sort column is an error", func(t *testing.T) {
		_, err := repo.ListByFilter(LeadListFilter{Status: models.LeadStatusQualified}, 0, 20, "notes; DROP", "asc")
		require.Error(t, err)
	})
}
