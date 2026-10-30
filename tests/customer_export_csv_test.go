package tests

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// Acceptance roots for #105 at the HTTP boundary: the real customer routes,
// the real auth middleware and the real repository on SQLite and on a
// disposable MySQL schema. The CSV body is parsed with encoding/csv and checked
// for the exact ordered id sequence, the row count, uniqueness, the header, the
// quoting of commas, quotes and newlines, and the apostrophe prefix on
// formula-like cells. Authorization and the failure-before-response contract are
// re-asserted here because the traversal change sits behind them.
//
// The bulk fixture mirrors internal/repository/customer_export_traversal_test.go:
// more rows than two of the old 500-row batches, every sort value running
// against id order with runs of duplicates, so the export that used to lose
// rows past the first batch fails loudly if it ever regresses. A few rows are
// erased through the real DELETE endpoint; five extra rows carry the awkward
// CSV content. All sort values are lowercase ASCII whose first character decides
// the order, so Go's byte order, MySQL's case-insensitive collation and
// SQLite's BINARY collation agree on the expected sequence.
const (
	exportCSVBulkRows = 1103
	exportCSVOldBatch = 500
)

var (
	exportCSVBase    = time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	exportCSVSpecial = exportCSVBase.Add(24 * time.Hour)
	exportCSVHeader  = []string{
		"id", "first_name", "last_name", "email", "phone", "company",
		"address", "notes", "assigned_to_id", "created_at", "updated_at", "website",
	}
	exportCSVSortColumns = []string{"created_at", "updated_at", "first_name", "last_name", "email", "company"}
)

type customerExportCSVHarness struct {
	db      *gorm.DB
	router  *gin.Engine
	tokens  map[string]string
	salesID uint
	// live is every non-erased row as stored, in id order.
	live []models.Customer
	// specials maps a label to the id of the row carrying awkward CSV content.
	specials map[string]uint
}

func TestCustomerExportCSVSQLite(t *testing.T) {
	runCustomerExportCSV(t, openCustomerExportCSVDB(t, false))
}

func TestCustomerExportCSVMySQL(t *testing.T) {
	runCustomerExportCSV(t, openCustomerExportCSVDB(t, true))
}

// openCustomerExportCSVDB opens a private SQLite database or a uniquely named
// disposable schema on the TEST_MYSQL_DSN server. The MySQL root skips without
// the variable and fails when it names a server that cannot be reached.
func openCustomerExportCSVDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	if !useMySQL {
		db, err := gorm.Open(sqlite.Open("file:customer_export_csv?mode=memory&cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{
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
	name := fmt.Sprintf("gocrm_customer_export_csv_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "`")
	require.NoError(t, err, "TEST_MYSQL_DSN is configured but the server is unavailable")
	cfg.DBName = name
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// newCustomerExportCSVHarness wires the production customer routes, with their
// RequireRole guards, behind the real JWT middleware and seeds one user per
// role.
func newCustomerExportCSVHarness(t *testing.T, db *gorm.DB) *customerExportCSVHarness {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.APIKey{}, &models.Company{}, &models.Customer{},
		// The erasure closure: deleting a customer erases the whole person.
		&models.Lead{}, &models.RefreshToken{}, &models.PasswordResetToken{}, &models.Ticket{}, &models.TicketComment{}, &models.Form{}, &models.FormSubmission{}, &models.FormConfirmationToken{}))

	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	authService := service.NewAuthService(userRepo, apiKeyRepo, config.JWTConfig{
		Secret:      "customer-export-csv-regression-secret",
		ExpiryHours: 1,
	})
	userService := service.NewUserService(userRepo)
	customerService := service.NewCustomerService(customerRepo, userRepo, repository.NewCompanyRepository(db))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupCustomerRoutes(protected, handler.NewCustomerHandler(customerService))

	h := &customerExportCSVHarness{db: db, router: router, tokens: map[string]string{}, specials: map[string]uint{}}
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer} {
		user := &models.User{
			Email:     fmt.Sprintf("%s@export-csv.test", role),
			FirstName: "Export",
			LastName:  string(role),
			Role:      role,
			IsActive:  true,
		}
		require.NoError(t, userService.Register(user, "password123"))
		token, err := authService.Login(user.Email, "password123")
		require.NoError(t, err)
		h.tokens[string(role)] = token
		if role == models.RoleSales {
			h.salesID = user.ID
		}
	}
	return h
}

func (h *customerExportCSVHarness) request(t *testing.T, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// seed inserts the bulk fixture, the five special rows, and erases every
// fiftieth bulk row through the real DELETE endpoint.
func (h *customerExportCSVHarness) seed(t *testing.T) {
	t.Helper()

	rows := make([]models.Customer, 0, exportCSVBulkRows)
	for i := 0; i < exportCSVBulkRows; i++ {
		rank := exportCSVBulkRows - 1 - i
		notes := "beta-notes"
		if i%3 != 0 {
			notes = "alpha-notes"
		}
		company := fmt.Sprintf("co%04d", rank/13)
		if rank%17 == 0 {
			company = ""
		}
		row := models.Customer{
			BaseModel: models.BaseModel{
				CreatedAt: exportCSVBase.Add(time.Duration(rank/9) * time.Second),
				UpdatedAt: exportCSVBase.Add(time.Duration(rank/3) * time.Second),
			},
			FirstName: fmt.Sprintf("fn%04d", rank/7),
			LastName:  fmt.Sprintf("ln%04d", rank/11),
			Email:     fmt.Sprintf("u%04d@export.test", rank),
			Phone:     fmt.Sprintf("+1 555 %04d", i),
			Company:   company,
			Address:   fmt.Sprintf("%d bulk road", i),
			Notes:     notes,
			Website:   fmt.Sprintf("https://bulk%04d.example", i),
		}
		if i%5 == 0 {
			assignee := h.salesID
			row.AssignedToID = &assignee
		}
		rows = append(rows, row)
	}
	require.NoError(t, h.db.CreateInBatches(&rows, 200).Error)
	for i := 1; i < len(rows); i++ {
		require.Greater(t, rows[i].ID, rows[i-1].ID, "ids must grow in insertion order")
	}

	// The awkward content. Sort-column values start with a letter no bulk value
	// starts with ('f', 'l', 'u', 'c') and differ from each other in that first
	// letter, so every engine orders them the same way Go does.
	at := func(k int) models.BaseModel {
		return models.BaseModel{
			CreatedAt: exportCSVSpecial.Add(time.Duration(k) * time.Minute),
			UpdatedAt: exportCSVSpecial.Add(time.Duration(k) * time.Minute),
		}
	}
	specials := []struct {
		label string
		row   models.Customer
	}{
		{"comma", models.Customer{BaseModel: at(1), FirstName: "ada, jr", LastName: "quoted", Email: "ada@special.test",
			Phone: "+1 555 9001", Company: "acme, inc", Address: "1 comma road, east", Notes: "likes, commas"}},
		{"quote", models.Customer{BaseModel: at(2), FirstName: "bea", LastName: `o"brien`, Email: "obrien@special.test",
			Phone: "+1 555 9002", Company: "brien", Address: `2 "quote" road`, Notes: `said "hello"`}},
		{"newline", models.Customer{BaseModel: at(3), FirstName: "dee", LastName: "multi", Email: "newline@special.test",
			Phone: "+1 555 9003", Company: "dune", Address: "3 main st\nsuite 2", Notes: "line one\nline two"}},
		{"formula", models.Customer{BaseModel: at(4), FirstName: "eve", LastName: "spread", Email: "formula@special.test",
			Phone: "=cmd|' /c calc'!a0", Company: "excel", Address: "@cmd", Notes: "=1+1", Website: "+sum(a1)"}},
		{"phone", models.Customer{BaseModel: at(5), FirstName: "gus", LastName: "numeric", Email: "phone@special.test",
			Phone: "+1 (555) 010-0100", Company: "gauge", Address: "-not a number", Notes: "-12.5", Website: "\ttabbed"}},
	}
	for _, special := range specials {
		row := special.row
		require.NoError(t, h.db.Create(&row).Error)
		h.specials[special.label] = row.ID
	}

	for i := 7; i < exportCSVBulkRows; i += 50 {
		rec := h.request(t, http.MethodDelete, fmt.Sprintf("/api/v1/customers/%d", rows[i].ID), h.tokens["admin"])
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	}

	require.NoError(t, h.db.Order("id").Find(&h.live).Error)
	require.Len(t, h.live, exportCSVBulkRows+len(specials)-22)
}

func compareExportCSVColumn(column string, a, b *models.Customer) int {
	times := func(x, y time.Time) int {
		switch {
		case x.Before(y):
			return -1
		case x.After(y):
			return 1
		}
		return 0
	}
	switch column {
	case "created_at":
		return times(a.CreatedAt, b.CreatedAt)
	case "updated_at":
		return times(a.UpdatedAt, b.UpdatedAt)
	case "first_name":
		return strings.Compare(a.FirstName, b.FirstName)
	case "last_name":
		return strings.Compare(a.LastName, b.LastName)
	case "email":
		return strings.Compare(a.Email, b.Email)
	case "company":
		return strings.Compare(a.Company, b.Company)
	}
	panic("unknown export sort column " + column)
}

// expectedIDs is the contract: the column in the requested direction, then id
// in the same direction; no column means id ascending. The search mirrors the
// repository's LIKE over the six searchable fields.
func (h *customerExportCSVHarness) expectedIDs(column string, desc bool, search string) []uint {
	rows := make([]models.Customer, 0, len(h.live))
	for _, row := range h.live {
		if search == "" {
			rows = append(rows, row)
			continue
		}
		for _, field := range []string{row.FirstName, row.LastName, row.Email, row.Company, row.Phone, row.Notes} {
			if strings.Contains(field, search) {
				rows = append(rows, row)
				break
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		c := 0
		if column != "" {
			c = compareExportCSVColumn(column, &rows[i], &rows[j])
		}
		if c == 0 {
			switch {
			case rows[i].ID < rows[j].ID:
				c = -1
			case rows[i].ID > rows[j].ID:
				c = 1
			}
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
	ids := make([]uint, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids
}

// exportCSV downloads the file as admin and returns the parsed data records
// (header verified and stripped) plus the raw body.
func (h *customerExportCSVHarness) exportCSV(t *testing.T, query string) ([][]string, string) {
	t.Helper()
	rec := h.request(t, http.MethodGet, "/api/v1/customers/export"+query, h.tokens["admin"])
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "attachment; filename=customers-export.csv", rec.Header().Get("Content-Disposition"))

	body := rec.Body.String()
	records, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	require.NoError(t, err, "the body must parse as CSV with a consistent field count")
	require.NotEmpty(t, records)
	require.Equal(t, exportCSVHeader, records[0])
	return records[1:], body
}

func recordIDs(t *testing.T, records [][]string) []uint {
	t.Helper()
	ids := make([]uint, len(records))
	seen := make(map[uint]bool, len(records))
	for i, record := range records {
		id, err := strconv.ParseUint(record[0], 10, 32)
		require.NoError(t, err, "row %d has a non-numeric id %q", i, record[0])
		require.False(t, seen[uint(id)], "customer %d exported twice", id)
		seen[uint(id)] = true
		ids[i] = uint(id)
	}
	return ids
}

func (h *customerExportCSVHarness) assertExport(t *testing.T, query, column string, desc bool, search string) [][]string {
	t.Helper()
	records, _ := h.exportCSV(t, query)
	expected := h.expectedIDs(column, desc, search)
	require.Len(t, records, len(expected), "every matching row exactly once")
	require.Equal(t, expected, recordIDs(t, records), "rows out of the requested order")
	return records
}

func findRecord(t *testing.T, records [][]string, id uint) []string {
	t.Helper()
	want := strconv.FormatUint(uint64(id), 10)
	for _, record := range records {
		if record[0] == want {
			return record
		}
	}
	require.Failf(t, "row missing", "customer %d is not in the export", id)
	return nil
}

func runCustomerExportCSV(t *testing.T, db *gorm.DB) {
	h := newCustomerExportCSVHarness(t, db)
	h.seed(t)
	live := make(map[uint]models.Customer, len(h.live))
	for _, row := range h.live {
		live[row.ID] = row
	}

	t.Run("fixture_is_adversarial", func(t *testing.T) {
		require.Greater(t, len(h.live), 2*exportCSVOldBatch)
		require.Greater(t, len(h.expectedIDs("", false, "alpha")), exportCSVOldBatch)
		for _, column := range exportCSVSortColumns {
			ids := h.expectedIDs(column, false, "")
			assert.False(t, sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] < ids[j] }),
				"%s ascending must disagree with id order", column)
		}
	})

	t.Run("default_order_is_complete_and_id_ascending", func(t *testing.T) {
		records := h.assertExport(t, "", "", false, "")
		require.Len(t, records, len(h.live))
	})

	for _, column := range exportCSVSortColumns {
		for _, direction := range []string{"asc", "desc"} {
			desc := direction == "desc"
			t.Run(column+"_"+direction, func(t *testing.T) {
				h.assertExport(t, "?sort_by="+column+"&sort_order="+direction, column, desc, "")
			})
			t.Run(column+"_"+direction+"_search", func(t *testing.T) {
				h.assertExport(t, "?search=alpha&sort_by="+column+"&sort_order="+direction, column, desc, "alpha")
			})
		}
	}

	t.Run("search_matches_only_the_filtered_rows", func(t *testing.T) {
		records := h.assertExport(t, "?search="+url.QueryEscape("line one"), "", false, "line one")
		require.Len(t, records, 1)
		assert.Equal(t, strconv.FormatUint(uint64(h.specials["newline"]), 10), records[0][0])
	})

	t.Run("invalid_sort_parameters_are_dropped_not_rejected", func(t *testing.T) {
		h.assertExport(t, "?sort_by=phone%3BDROP+TABLE+customers&sort_order=desc", "", false, "")
		h.assertExport(t, "?sort_by=email&sort_order=sideways", "email", false, "")
	})

	t.Run("rows_are_rendered_in_the_pinned_column_order", func(t *testing.T) {
		records, _ := h.exportCSV(t, "")
		var assigned, unassigned *models.Customer
		for i := range h.live {
			row := &h.live[i]
			if strings.HasPrefix(row.Address, "0 bulk") || strings.HasPrefix(row.Address, "1 bulk") {
				if row.AssignedToID != nil && assigned == nil {
					assigned = row
				}
				if row.AssignedToID == nil && unassigned == nil {
					unassigned = row
				}
			}
		}
		require.NotNil(t, assigned)
		require.NotNil(t, unassigned)
		for _, row := range []*models.Customer{assigned, unassigned} {
			assignee := ""
			if row.AssignedToID != nil {
				assignee = strconv.FormatUint(uint64(*row.AssignedToID), 10)
			}
			assert.Equal(t, []string{
				strconv.FormatUint(uint64(row.ID), 10), row.FirstName, row.LastName, row.Email, row.Phone, row.Company,
				row.Address, row.Notes, assignee, row.CreatedAt.Format(time.RFC3339), row.UpdatedAt.Format(time.RFC3339), row.Website,
			}, findRecord(t, records, row.ID))
		}
		assert.Equal(t, strconv.FormatUint(uint64(h.salesID), 10), findRecord(t, records, assigned.ID)[8])
	})

	t.Run("commas_quotes_and_newlines_are_quoted", func(t *testing.T) {
		records, body := h.exportCSV(t, "")

		comma := findRecord(t, records, h.specials["comma"])
		assert.Equal(t, "ada, jr", comma[1])
		assert.Equal(t, "acme, inc", comma[5])
		assert.Equal(t, "1 comma road, east", comma[6])
		assert.Equal(t, "likes, commas", comma[7])
		assert.Contains(t, body, `"ada, jr"`)
		assert.Contains(t, body, `"acme, inc"`)

		quote := findRecord(t, records, h.specials["quote"])
		assert.Equal(t, `o"brien`, quote[2])
		assert.Equal(t, `2 "quote" road`, quote[6])
		assert.Equal(t, `said "hello"`, quote[7])
		assert.Contains(t, body, `"o""brien"`)
		assert.Contains(t, body, `"said ""hello"""`)

		newline := findRecord(t, records, h.specials["newline"])
		assert.Equal(t, "3 main st\nsuite 2", newline[6])
		assert.Equal(t, "line one\nline two", newline[7])
		assert.Contains(t, body, "\"line one\nline two\"")
	})

	t.Run("formula_like_cells_are_neutralised_and_numbers_are_not", func(t *testing.T) {
		records, _ := h.exportCSV(t, "")

		formula := findRecord(t, records, h.specials["formula"])
		assert.Equal(t, "'=cmd|' /c calc'!a0", formula[4])
		assert.Equal(t, "'@cmd", formula[6])
		assert.Equal(t, "'=1+1", formula[7])
		assert.Equal(t, "'+sum(a1)", formula[11])
		assert.Equal(t, "excel", formula[5], "an ordinary cell is left alone")

		phone := findRecord(t, records, h.specials["phone"])
		assert.Equal(t, "+1 (555) 010-0100", phone[4], "a dialling code is not a formula")
		assert.Equal(t, "-12.5", phone[7], "a negative figure is not a formula")
		assert.Equal(t, "'-not a number", phone[6])
		assert.Equal(t, "'\ttabbed", phone[11])
	})

	t.Run("erased_rows_are_absent", func(t *testing.T) {
		records, body := h.exportCSV(t, "?sort_by=created_at&sort_order=desc")
		for _, record := range records {
			_, ok := live[recordIDs(t, [][]string{record})[0]]
			require.True(t, ok, "row %s is not a live customer", record[0])
		}
		var erased []models.Customer
		require.NoError(t, h.db.Unscoped().Where("deleted_at IS NOT NULL").Find(&erased).Error)
		require.Len(t, erased, 22)
		for _, row := range erased {
			assert.NotContains(t, body, "\n"+strconv.FormatUint(uint64(row.ID), 10)+",")
			assert.NotContains(t, body, row.Email)
		}
	})

	t.Run("output_is_repeatable", func(t *testing.T) {
		_, first := h.exportCSV(t, "?search=alpha&sort_by=company&sort_order=desc")
		_, second := h.exportCSV(t, "?search=alpha&sort_by=company&sort_order=desc")
		assert.Equal(t, first, second)
	})

	t.Run("export_is_admin_only", func(t *testing.T) {
		for _, role := range []string{"sales", "support", "customer"} {
			rec := h.request(t, http.MethodGet, "/api/v1/customers/export", h.tokens[role])
			assert.Equal(t, http.StatusForbidden, rec.Code, role)
			assert.NotContains(t, rec.Header().Get("Content-Type"), "text/csv", role)
			assert.Empty(t, rec.Header().Get("Content-Disposition"), role)
			var response utils.APIResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), role)
			assert.False(t, response.Success, role)
		}
		for _, token := range []string{"", "not-a-token"} {
			rec := h.request(t, http.MethodGet, "/api/v1/customers/export", token)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.NotContains(t, rec.Header().Get("Content-Type"), "text/csv")
		}
	})

	// Last, because it destroys the table: a failure before the first byte is
	// the JSON error envelope, never a 200 with a partial or empty file.
	t.Run("failure_before_the_response_is_a_json_error", func(t *testing.T) {
		require.NoError(t, h.db.Migrator().DropTable(&models.Customer{}))
		rec := h.request(t, http.MethodGet, "/api/v1/customers/export?sort_by=email", h.tokens["admin"])
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Header().Get("Content-Type"), "text/csv")
		assert.Empty(t, rec.Header().Get("Content-Disposition"))
		var response utils.APIResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		assert.False(t, response.Success)
	})
}
