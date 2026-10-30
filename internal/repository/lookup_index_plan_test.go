package repository

import (
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// #109: the lead lookups by email, classification and external id, and the
// company duplicate-domain check, must be answered through an index rather
// than a scan of the table. The statements are captured from the real
// repository methods, so a change to a predicate that hides the column from
// the index (LOWER(domain) did exactly that) fails here.
//
// SQLite is checked on every run. The MySQL and MariaDB plans were measured
// by hand and are recorded in docs/datamodel.md; the SQL migration's index
// names and columns are pinned against AutoMigrate by
// cmd/migrate TestLookupIndexesMigration.

type capturedStatement struct {
	sql  string
	vars []interface{}
}

// captureQueries records every query statement the handle runs.
func captureQueries(t *testing.T, db *gorm.DB) *[]capturedStatement {
	t.Helper()
	var captured []capturedStatement
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:capture_plan", func(tx *gorm.DB) {
		captured = append(captured, capturedStatement{
			sql:  tx.Statement.SQL.String(),
			vars: append([]interface{}(nil), tx.Statement.Vars...),
		})
	}))
	return &captured
}

func sqlitePlan(t *testing.T, db *gorm.DB, statement capturedStatement) string {
	t.Helper()
	rows, err := db.Raw("EXPLAIN QUERY PLAN "+statement.sql, statement.vars...).Rows()
	require.NoError(t, err)
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
		lines = append(lines, detail)
	}
	require.NoError(t, rows.Err())
	return strings.Join(lines, "\n")
}

func TestLookupIndexesServeLookupsSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:lookup_index_plan?mode=memory&cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{
		Logger: logger.Discard,
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{}))

	owner := models.User{Email: "owner@plan.test", Password: "x", FirstName: "o", LastName: "o", Role: models.RoleSales}
	require.NoError(t, db.Create(&owner).Error)
	classes := []models.LeadClassification{models.LeadClassificationLead, models.LeadClassificationSpam, models.LeadClassificationTest}
	leads := make([]models.Lead, 0, 600)
	for i := 0; i < 600; i++ {
		leads = append(leads, models.Lead{FirstName: "a", LastName: "b", Email: fmt.Sprintf("lead%04d@plan.test", i%500),
			Classification: classes[i%len(classes)], ExternalID: fmt.Sprintf("ext-%04d", i), OwnerID: owner.ID})
	}
	require.NoError(t, db.CreateInBatches(leads, 200).Error)
	companies := make([]models.Company, 0, 300)
	for i := 0; i < 300; i++ {
		companies = append(companies, models.Company{Name: "c", Domain: fmt.Sprintf("c%04d.plan.test", i)})
	}
	require.NoError(t, db.CreateInBatches(companies, 100).Error)
	require.NoError(t, db.Exec("ANALYZE").Error)

	captured := captureQueries(t, db)
	leadRepo := NewLeadRepository(db)
	companyRepo := NewCompanyRepository(db)

	cases := []struct {
		name  string
		index string
		table string
		run   func() error
	}{
		{"GetLatestByEmail", "idx_leads_email", "leads", func() error {
			_, err := leadRepo.GetLatestByEmail("lead0042@plan.test")
			return err
		}},
		{"GetByClassification", "idx_leads_classification", "leads", func() error {
			_, err := leadRepo.GetByClassification(models.LeadClassificationSpam, 20, 20)
			return err
		}},
		{"CountByClassification", "idx_leads_classification", "leads", func() error {
			_, err := leadRepo.CountByClassification(models.LeadClassificationSpam)
			return err
		}},
		{"GetByExternalID", "idx_leads_external_id", "leads", func() error {
			_, err := leadRepo.GetByExternalID("ext-0042")
			return err
		}},
		{"ExistsByDomain", "idx_companies_domain", "companies", func() error {
			exists, err := companyRepo.ExistsByDomain("C0042.Plan.Test", 7)
			assert.True(t, exists, "the lower-cased argument still finds the stored domain")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			*captured = nil
			require.NoError(t, tc.run())
			require.Len(t, *captured, 1, "one statement per lookup")
			plan := sqlitePlan(t, db, (*captured)[0])
			assert.Contains(t, plan, "INDEX "+tc.index, "statement %q", (*captured)[0].sql)
			assert.NotContains(t, plan, "SCAN "+tc.table, "statement %q", (*captured)[0].sql)
		})
	}
}
