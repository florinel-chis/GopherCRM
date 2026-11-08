package tests

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Acceptance roots for GET /leads/export (#234) at the HTTP boundary: the real
// lead routes with their RequireRole guard, the real auth middleware, the real
// service and repository, on SQLite and on a disposable MySQL schema. The CSV
// body is parsed with encoding/csv and checked for the header, the exact ordered
// id sequence over more than 1,000 rows, uniqueness, erased rows absent and
// converted rows present, the quoting of commas, quotes and newlines, and the
// apostrophe prefix on formula-like cells.
//
// Sort values run against id order with runs of duplicates; every sort value is
// lowercase ASCII whose order is decided by characters Go's byte order, MySQL's
// case-insensitive collation and SQLite's BINARY collation agree on.
const leadExportCSVBulkRows = 1103

var (
	leadExportCSVBase    = time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC)
	leadExportCSVSpecial = leadExportCSVBase.Add(24 * time.Hour)
	leadExportCSVHeader  = []string{
		"id", "first_name", "last_name", "email", "phone", "company", "position", "source", "status",
		"classification", "external_id", "notes", "owner_id", "customer_id", "company_id", "created_at", "updated_at",
	}
	leadExportCSVClasses = []models.LeadClassification{
		models.LeadClassificationHotLead, models.LeadClassificationLead, models.LeadClassificationSpam,
		models.LeadClassificationTest, models.LeadClassificationUnclassified,
	}
	leadExportCSVModels = []interface{}{
		&models.User{}, &models.APIKey{}, &models.Company{}, &models.Customer{}, &models.Lead{},
		// The erasure closure of a lead.
		&models.RefreshToken{}, &models.PasswordResetToken{}, &models.Ticket{}, &models.TicketComment{},
		&models.Form{}, &models.FormSubmission{}, &models.FormConfirmationToken{},
	}
)

type leadExportCSVHarness struct {
	db     *gorm.DB
	router *gin.Engine
	tokens map[string]string
	ids    map[string]uint
}

func TestLeadExportCSVSQLite(t *testing.T) {
	runLeadExportCSV(t, openLeadExportCSVDB(t, "lead_export_csv", false))
}

func TestLeadExportCSVMySQL(t *testing.T) {
	runLeadExportCSV(t, openLeadExportCSVDB(t, "lead_export_csv", true))
}

func TestLeadExportRolesSQLite(t *testing.T) {
	runLeadExportRoles(t, openLeadExportCSVDB(t, "lead_export_roles", false))
}

func TestLeadExportRolesMySQL(t *testing.T) {
	runLeadExportRoles(t, openLeadExportCSVDB(t, "lead_export_roles", true))
}

// openLeadExportCSVDB opens a private SQLite database or a uniquely named
// disposable schema on the TEST_MYSQL_DSN server. The MySQL roots skip without
// the variable and fail when it names a server that cannot be reached.
func openLeadExportCSVDB(t *testing.T, name string, useMySQL bool) *gorm.DB {
	t.Helper()
	if !useMySQL {
		db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{
			Logger: logger.Discard,
		})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}

	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	cfg, err := mysqldriver.ParseDSN(raw)
	require.NoError(t, err)
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
	server, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	schema := fmt.Sprintf("gocrm_%s_%d", name, time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + schema + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + schema + "`")
	require.NoError(t, err, "TEST_MYSQL_DSN is configured but the server is unavailable")
	cfg.DBName = schema
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// newLeadExportCSVHarness wires the production lead routes behind the real JWT
// middleware and registers the given users (label -> role).
func newLeadExportCSVHarness(t *testing.T, db *gorm.DB, users map[string]models.UserRole) *leadExportCSVHarness {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	require.NoError(t, db.AutoMigrate(leadExportCSVModels...))

	userRepo := repository.NewUserRepository(db)
	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db), config.JWTConfig{
		Secret:      "lead-export-csv-regression-secret",
		ExpiryHours: 1,
	})
	userService := service.NewUserService(userRepo)
	leadService := service.NewLeadService(repository.NewLeadRepository(db), repository.NewCustomerRepository(db),
		repository.NewCompanyRepository(db), userRepo, utils.NewTransactionManager(db))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupLeadRoutes(protected, handler.NewLeadHandler(leadService))

	h := &leadExportCSVHarness{db: db, router: router, tokens: map[string]string{}, ids: map[string]uint{}}
	labels := make([]string, 0, len(users))
	for label := range users {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		user := &models.User{
			Email:     fmt.Sprintf("%s@lead-export-csv.test", label),
			FirstName: "Export",
			LastName:  label,
			Role:      users[label],
			IsActive:  true,
		}
		require.NoError(t, userService.Register(user, "password123"))
		token, err := authService.Login(user.Email, "password123")
		require.NoError(t, err)
		h.tokens[label] = token
		h.ids[label] = user.ID
	}
	return h
}

func (h *leadExportCSVHarness) request(t *testing.T, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// export downloads the file as the given user and returns the parsed data
// records (header verified and stripped) and the raw body.
func (h *leadExportCSVHarness) export(t *testing.T, label, query string) ([][]string, string) {
	t.Helper()
	rec := h.request(t, http.MethodGet, "/api/v1/leads/export"+query, h.tokens[label])
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "attachment; filename=leads-export.csv", rec.Header().Get("Content-Disposition"))
	body := rec.Body.String()
	records, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	require.NoError(t, err, "the body must parse as CSV with a consistent field count")
	require.NotEmpty(t, records)
	require.Equal(t, leadExportCSVHeader, records[0])
	return records[1:], body
}

// erase deletes a lead through the real DELETE route (an erasure).
func (h *leadExportCSVHarness) erase(t *testing.T, label string, id uint) {
	t.Helper()
	rec := h.request(t, http.MethodDelete, fmt.Sprintf("/api/v1/leads/%d", id), h.tokens[label])
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

// convert marks a lead converted into a fresh customer, as the conversion
// writes it (status converted, customer_id set).
func (h *leadExportCSVHarness) convert(t *testing.T, id uint) {
	t.Helper()
	customer := models.Customer{FirstName: "conv", LastName: fmt.Sprint(id), Email: fmt.Sprintf("conv%d@lead-export-csv.test", id)}
	require.NoError(t, h.db.Create(&customer).Error)
	require.NoError(t, h.db.Model(&models.Lead{}).Where("id = ?", id).UpdateColumns(map[string]interface{}{
		"status": models.LeadStatusConverted, "customer_id": customer.ID,
	}).Error)
}

func (h *leadExportCSVHarness) live(t *testing.T) []models.Lead {
	t.Helper()
	var rows []models.Lead
	require.NoError(t, h.db.Order("id").Find(&rows).Error)
	return rows
}

func leadRecordIDs(t *testing.T, records [][]string) []uint {
	t.Helper()
	ids := make([]uint, len(records))
	seen := make(map[uint]bool, len(records))
	for i, record := range records {
		id, err := strconv.ParseUint(record[0], 10, 32)
		require.NoError(t, err, "row %d has a non-numeric id %q", i, record[0])
		require.False(t, seen[uint(id)], "lead %d exported twice", id)
		seen[uint(id)] = true
		ids[i] = uint(id)
	}
	return ids
}

func leadFindRecord(t *testing.T, records [][]string, id uint) []string {
	t.Helper()
	want := strconv.FormatUint(uint64(id), 10)
	for _, record := range records {
		if record[0] == want {
			return record
		}
	}
	require.Failf(t, "row missing", "lead %d is not in the export", id)
	return nil
}

func leadCompareCSVColumn(column string, a, b *models.Lead) int {
	switch column {
	case "created_at":
		switch {
		case a.CreatedAt.Before(b.CreatedAt):
			return -1
		case a.CreatedAt.After(b.CreatedAt):
			return 1
		}
		return 0
	case "first_name":
		return strings.Compare(a.FirstName, b.FirstName)
	case "email":
		return strings.Compare(a.Email, b.Email)
	case "company":
		return strings.Compare(a.Company, b.Company)
	case "classification":
		return strings.Compare(string(a.Classification), string(b.Classification))
	}
	panic("unknown lead export sort column " + column)
}

// leadExpectedCSVIDs is the contract: the rows kept by keep, ordered by the
// column and then id in the requested direction; no column means id ascending.
func leadExpectedCSVIDs(rows []models.Lead, keep func(*models.Lead) bool, column string, desc bool) []uint {
	kept := make([]models.Lead, 0, len(rows))
	for i := range rows {
		if keep == nil || keep(&rows[i]) {
			kept = append(kept, rows[i])
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		c := 0
		if column != "" {
			c = leadCompareCSVColumn(column, &kept[i], &kept[j])
		}
		if c == 0 {
			switch {
			case kept[i].ID < kept[j].ID:
				c = -1
			case kept[i].ID > kept[j].ID:
				c = 1
			}
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
	ids := make([]uint, len(kept))
	for i := range kept {
		ids[i] = kept[i].ID
	}
	return ids
}

func leadOptionalID(id *uint) string {
	if id == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*id), 10)
}

func runLeadExportCSV(t *testing.T, db *gorm.DB) {
	h := newLeadExportCSVHarness(t, db, map[string]models.UserRole{
		"admin": models.RoleAdmin, "sales": models.RoleSales,
	})

	// The bulk fixture.
	rows := make([]models.Lead, 0, leadExportCSVBulkRows)
	for i := 0; i < leadExportCSVBulkRows; i++ {
		rank := leadExportCSVBulkRows - 1 - i
		notes := "beta-notes"
		if i%3 != 0 {
			notes = "alpha-notes"
		}
		company := fmt.Sprintf("co%04d", rank/13)
		if rank%17 == 0 {
			company = ""
		}
		owner := h.ids["admin"]
		if i%2 == 1 {
			owner = h.ids["sales"]
		}
		rows = append(rows, models.Lead{
			BaseModel: models.BaseModel{
				CreatedAt: leadExportCSVBase.Add(time.Duration(rank/9) * time.Second),
				UpdatedAt: leadExportCSVBase.Add(time.Duration(rank/3) * time.Second),
			},
			FirstName:      fmt.Sprintf("fn%04d", rank/7),
			LastName:       fmt.Sprintf("ln%04d", rank/11),
			Email:          fmt.Sprintf("u%04d@lead-export.test", rank),
			Phone:          fmt.Sprintf("+1 555 %04d", i),
			Company:        company,
			Position:       "buyer",
			Source:         "website",
			Status:         models.LeadStatusNew,
			Classification: leadExportCSVClasses[rank*len(leadExportCSVClasses)/leadExportCSVBulkRows],
			ExternalID:     fmt.Sprintf("ext-%04d", i),
			Notes:          notes,
			OwnerID:        owner,
		})
	}
	require.NoError(t, h.db.CreateInBatches(&rows, 200).Error)
	for i := 1; i < len(rows); i++ {
		require.Greater(t, rows[i].ID, rows[i-1].ID, "ids must grow in insertion order")
	}

	// The awkward content. Sort-column values start with a letter no bulk value
	// starts with and differ from each other in that first letter.
	at := func(k int) models.BaseModel {
		return models.BaseModel{
			CreatedAt: leadExportCSVSpecial.Add(time.Duration(k) * time.Minute),
			UpdatedAt: leadExportCSVSpecial.Add(time.Duration(k) * time.Minute),
		}
	}
	specials := []struct {
		label string
		row   models.Lead
	}{
		{"comma", models.Lead{BaseModel: at(1), FirstName: "ada, jr", LastName: "quoted", Email: "ada@special.test",
			Phone: "+1 555 9001", Company: "acme, inc", Position: "head, sales", Notes: "likes, commas"}},
		{"quote", models.Lead{BaseModel: at(2), FirstName: "bea", LastName: `o"brien`, Email: "obrien@special.test",
			Phone: "+1 555 9002", Company: "brien", Position: `the "boss"`, Notes: `said "hello"`}},
		{"newline", models.Lead{BaseModel: at(3), FirstName: "dee", LastName: "multi", Email: "newline@special.test",
			Phone: "+1 555 9003", Company: "dune", Notes: "line one\nline two"}},
		{"formula", models.Lead{BaseModel: at(4), FirstName: "=eve", LastName: "@spread", Email: "formula@special.test",
			Phone: "=cmd|' /c calc'!a0", Company: "excel", Position: "@cmd", Source: "-not a number", ExternalID: "+sum(a1)", Notes: "=1+1"}},
		{"numeric", models.Lead{BaseModel: at(5), FirstName: "gus", LastName: "numeric", Email: "phone@special.test",
			Phone: "+1 (555) 010-0100", Company: "gauge", Source: "\ttabbed", Notes: "-12.5"}},
	}
	specialIDs := map[string]uint{}
	for _, special := range specials {
		row := special.row
		row.Status = models.LeadStatusQualified
		row.Classification = models.LeadClassificationLead
		row.OwnerID = h.ids["admin"]
		require.NoError(t, h.db.Create(&row).Error)
		specialIDs[special.label] = row.ID
	}

	// Converted leads stay; erased leads go (through the real DELETE route).
	var converted, erased []uint
	var erasedEmails, erasedExternalIDs []string
	for i := 20; i < leadExportCSVBulkRows; i += 97 {
		h.convert(t, rows[i].ID)
		converted = append(converted, rows[i].ID)
	}
	for i := 7; i < leadExportCSVBulkRows; i += 50 {
		h.erase(t, "admin", rows[i].ID)
		erased = append(erased, rows[i].ID)
		erasedEmails = append(erasedEmails, rows[i].Email)
		erasedExternalIDs = append(erasedExternalIDs, rows[i].ExternalID)
	}
	live := h.live(t)
	require.Len(t, live, leadExportCSVBulkRows+len(specials)-len(erased))
	require.Greater(t, len(live), 1000)
	byID := make(map[uint]models.Lead, len(live))
	for _, row := range live {
		byID[row.ID] = row
	}

	assertOrder := func(t *testing.T, query string, keep func(*models.Lead) bool, column string, desc bool) [][]string {
		t.Helper()
		records, _ := h.export(t, "admin", query)
		expected := leadExpectedCSVIDs(live, keep, column, desc)
		require.Len(t, records, len(expected), "every matching lead exactly once")
		require.Equal(t, expected, leadRecordIDs(t, records), "rows out of the expected order")
		return records
	}

	t.Run("fixture_is_adversarial", func(t *testing.T) {
		for _, column := range []string{"classification", "company", "email", "created_at"} {
			ids := leadExpectedCSVIDs(live, nil, column, false)
			assert.False(t, sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] < ids[j] }),
				"%s ascending must disagree with id order", column)
		}
	})

	t.Run("admin_gets_every_live_lead_in_id_order", func(t *testing.T) {
		records := assertOrder(t, "", nil, "", false)
		require.Len(t, records, len(live))
	})

	for _, column := range []string{"classification", "company", "email", "created_at"} {
		for _, direction := range []string{"asc", "desc"} {
			column, direction := column, direction
			t.Run(column+"_"+direction, func(t *testing.T) {
				assertOrder(t, "?sort_by="+column+"&sort_order="+direction, nil, column, direction == "desc")
			})
		}
	}

	t.Run("admin_precedence_follows_the_list", func(t *testing.T) {
		alpha := func(l *models.Lead) bool {
			for _, field := range []string{l.FirstName, l.LastName, l.Email, l.Company, l.Phone, l.Notes} {
				if strings.Contains(field, "alpha") {
					return true
				}
			}
			return false
		}
		// search wins over classification and keeps the sort
		assertOrder(t, "?search=alpha&classification=spam&sort_by=email&sort_order=desc", alpha, "email", true)
		// classification wins over the sort: id ascending
		assertOrder(t, "?classification=spam&sort_by=email&sort_order=desc",
			func(l *models.Lead) bool { return l.Classification == models.LeadClassificationSpam }, "", false)
		// an unknown sort column is dropped, a bad direction becomes asc
		assertOrder(t, "?sort_by=notes%3BDROP+TABLE+leads&sort_order=desc", nil, "", false)
		assertOrder(t, "?sort_by=company&sort_order=sideways", nil, "company", false)
		// external_id and paging are not read
		assertOrder(t, "?external_id=ext-0001&offset=40&limit=5&page=3", nil, "", false)
	})

	t.Run("erased_absent_converted_present", func(t *testing.T) {
		records, body := h.export(t, "admin", "?sort_by=created_at&sort_order=desc")
		exported := map[uint]bool{}
		for _, id := range leadRecordIDs(t, records) {
			exported[id] = true
			_, ok := byID[id]
			require.True(t, ok, "lead %d is not live", id)
		}
		for k, id := range erased {
			assert.False(t, exported[id], "erased lead %d is in the export", id)
			// Nor is anything of what the erased row held.
			assert.NotContains(t, body, erasedEmails[k])
			assert.NotContains(t, body, erasedExternalIDs[k]+",")
		}
		for _, id := range converted {
			require.True(t, exported[id], "converted lead %d is missing", id)
			record := leadFindRecord(t, records, id)
			assert.Equal(t, "converted", record[8])
			assert.NotEmpty(t, record[13], "a converted lead carries its customer_id")
		}
	})

	t.Run("rows_are_rendered_in_the_pinned_column_order", func(t *testing.T) {
		records, _ := h.export(t, "admin", "")
		for _, id := range []uint{converted[0], rows[1].ID, specialIDs["comma"]} {
			row := byID[id]
			assert.Equal(t, []string{
				strconv.FormatUint(uint64(row.ID), 10), row.FirstName, row.LastName, row.Email, row.Phone,
				row.Company, row.Position, row.Source, string(row.Status), string(row.Classification),
				row.ExternalID, row.Notes, strconv.FormatUint(uint64(row.OwnerID), 10),
				leadOptionalID(row.CustomerID), leadOptionalID(row.CompanyID),
				row.CreatedAt.UTC().Format(time.RFC3339), row.UpdatedAt.UTC().Format(time.RFC3339),
			}, leadFindRecord(t, records, id))
		}
		assert.Equal(t, "2026-05-02T00:01:00Z", leadFindRecord(t, records, specialIDs["comma"])[15],
			"timestamps are RFC 3339 in UTC")
	})

	t.Run("commas_quotes_and_newlines_round_trip", func(t *testing.T) {
		records, body := h.export(t, "admin", "")

		comma := leadFindRecord(t, records, specialIDs["comma"])
		assert.Equal(t, "ada, jr", comma[1])
		assert.Equal(t, "acme, inc", comma[5])
		assert.Equal(t, "head, sales", comma[6])
		assert.Equal(t, "likes, commas", comma[11])
		assert.Contains(t, body, `"ada, jr"`)

		quote := leadFindRecord(t, records, specialIDs["quote"])
		assert.Equal(t, `o"brien`, quote[2])
		assert.Equal(t, `the "boss"`, quote[6])
		assert.Equal(t, `said "hello"`, quote[11])
		assert.Contains(t, body, `"o""brien"`)

		newline := leadFindRecord(t, records, specialIDs["newline"])
		assert.Equal(t, "line one\nline two", newline[11])
		assert.Contains(t, body, "\"line one\nline two\"")
	})

	t.Run("formula_like_cells_are_apostrophe_prefixed", func(t *testing.T) {
		records, _ := h.export(t, "admin", "")

		formula := leadFindRecord(t, records, specialIDs["formula"])
		assert.Equal(t, "'=eve", formula[1])
		assert.Equal(t, "'@spread", formula[2])
		assert.Equal(t, "'=cmd|' /c calc'!a0", formula[4])
		assert.Equal(t, "excel", formula[5], "an ordinary cell is left alone")
		assert.Equal(t, "'@cmd", formula[6])
		assert.Equal(t, "'-not a number", formula[7])
		assert.Equal(t, "'+sum(a1)", formula[10])
		assert.Equal(t, "'=1+1", formula[11])

		numeric := leadFindRecord(t, records, specialIDs["numeric"])
		assert.Equal(t, "+1 (555) 010-0100", numeric[4], "a dialling code is not a formula")
		assert.Equal(t, "'\ttabbed", numeric[7])
		assert.Equal(t, "-12.5", numeric[11], "a negative figure is not a formula")
	})

	t.Run("output_is_repeatable", func(t *testing.T) {
		_, first := h.export(t, "admin", "?search=alpha&sort_by=company&sort_order=desc")
		_, second := h.export(t, "admin", "?search=alpha&sort_by=company&sort_order=desc")
		assert.Equal(t, first, second)
	})

	// Last, because it destroys the table: a failure before the first byte is
	// the JSON error envelope, never a 200 with a partial or empty file.
	t.Run("failure_before_the_response_is_a_json_error", func(t *testing.T) {
		require.NoError(t, h.db.Migrator().DropTable(&models.Lead{}))
		rec := h.request(t, http.MethodGet, "/api/v1/leads/export?sort_by=email", h.tokens["admin"])
		leadExportAssertJSONError(t, rec, http.StatusInternalServerError, "INTERNAL_ERROR")
	})
}

func leadExportAssertJSONError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assert.Equal(t, status, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Header().Get("Content-Type"), "text/csv")
	assert.Empty(t, rec.Header().Get("Content-Disposition"))
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	assert.False(t, response.Success)
	require.NotNil(t, response.Error)
	assert.Equal(t, code, response.Error.Code)
}

func runLeadExportRoles(t *testing.T, db *gorm.DB) {
	h := newLeadExportCSVHarness(t, db, map[string]models.UserRole{
		"admin": models.RoleAdmin, "sales": models.RoleSales, "sales2": models.RoleSales,
		"support": models.RoleSupport, "customer": models.RoleCustomer,
	})

	// Leads of three owners, interleaved so the sales user's ids are not
	// contiguous, with sort values against id order.
	owners := []string{"sales", "admin", "sales2"}
	var created []models.Lead
	for i := 0; i < 90; i++ {
		rank := 89 - i
		classification := models.LeadClassificationLead
		if i%4 == 0 {
			classification = models.LeadClassificationSpam
		}
		lead := models.Lead{
			FirstName:      fmt.Sprintf("fn%03d", rank),
			LastName:       "roles",
			Email:          fmt.Sprintf("r%03d@lead-export-roles.test", rank),
			Company:        fmt.Sprintf("co%03d", rank/5),
			Status:         models.LeadStatusNew,
			Classification: classification,
			Notes:          "alpha",
			OwnerID:        h.ids[owners[i%3]],
		}
		require.NoError(t, h.db.Create(&lead).Error)
		created = append(created, lead)
	}
	// The sales user erases two of its own leads and has one converted.
	salesLeads := []uint{}
	for _, lead := range created {
		if lead.OwnerID == h.ids["sales"] {
			salesLeads = append(salesLeads, lead.ID)
		}
	}
	h.erase(t, "sales", salesLeads[1])
	h.erase(t, "sales", salesLeads[4])
	h.convert(t, salesLeads[2])

	var expected []uint
	for _, row := range h.live(t) {
		if row.OwnerID == h.ids["sales"] {
			expected = append(expected, row.ID)
		}
	}
	require.Len(t, expected, 28)

	t.Run("sales_gets_exactly_its_own_live_leads_in_id_order", func(t *testing.T) {
		for _, query := range []string{
			"",
			"?search=r001&classification=spam&sort_by=email&sort_order=desc",
			"?classification=spam",
			"?sort_by=company&sort_order=desc",
			"?search=nothing-matches-this",
			"?external_id=x&offset=10&limit=2&page=4",
		} {
			records, _ := h.export(t, "sales", query)
			assert.Equal(t, expected, leadRecordIDs(t, records), "query %q", query)
			for _, record := range records {
				assert.Equal(t, strconv.FormatUint(uint64(h.ids["sales"]), 10), record[12], "owner_id of lead %s", record[0])
			}
		}
		records, _ := h.export(t, "sales", "")
		assert.Equal(t, "converted", leadFindRecord(t, records, salesLeads[2])[8], "a converted lead stays in the export")
	})

	t.Run("admin_gets_every_owner", func(t *testing.T) {
		records, _ := h.export(t, "admin", "")
		assert.Len(t, records, len(created)-2)
	})

	t.Run("support_and_customer_get_the_json_403", func(t *testing.T) {
		for _, label := range []string{"support", "customer"} {
			rec := h.request(t, http.MethodGet, "/api/v1/leads/export", h.tokens[label])
			leadExportAssertJSONError(t, rec, http.StatusForbidden, "FORBIDDEN")
		}
	})

	t.Run("no_credentials_get_the_json_401", func(t *testing.T) {
		for _, token := range []string{"", "not-a-token"} {
			rec := h.request(t, http.MethodGet, "/api/v1/leads/export", token)
			leadExportAssertJSONError(t, rec, http.StatusUnauthorized, "UNAUTHORIZED")
		}
	})

	t.Run("the_static_route_does_not_shadow_lead_by_id", func(t *testing.T) {
		id := created[1].ID // the admin's
		rec := h.request(t, http.MethodGet, fmt.Sprintf("/api/v1/leads/%d", id), h.tokens["admin"])
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
		var response struct {
			Success bool            `json:"success"`
			Data    json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		assert.True(t, response.Success)
		var data struct {
			ID uint `json:"id"`
		}
		require.NoError(t, json.Unmarshal(response.Data, &data))
		assert.Equal(t, id, data.ID)

		rec = h.request(t, http.MethodGet, fmt.Sprintf("/api/v1/leads/%d", salesLeads[0]), h.tokens["sales"])
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}
