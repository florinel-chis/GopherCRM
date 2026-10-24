package repository

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
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

// Regression roots for #105: ListAllForExport used to combine the requested
// ORDER BY with GORM's FindInBatches, whose continuation cursor is `id > last
// id of the previous batch`. Under any ordering other than id that cursor
// skipped every customer whose id was below the previous batch's last id, so a
// sorted export longer than one batch silently lost rows.
//
// The fixture is built to be adversarial to exactly that, and to any other
// batch-boundary shortcut:
//
//   - more rows than two of the old 500-row batches (1203 inserted, 1179 live);
//   - every sort column runs OPPOSITE to id order, so a primary-key cursor
//     discards most of the table;
//   - every column except the unique email has runs of duplicate values, and in
//     the unfiltered ascending order both old batch boundaries (positions
//     499/500 and 999/1000) fall inside such a run, so an implementation that
//     breaks ties arbitrarily between batches repeats or drops a row there;
//   - a legacy-shaped NULL company (the MySQL migration declares the column
//     nullable) and an empty-string company, which must order deterministically
//     too;
//   - erased (soft-deleted) rows, which must never appear;
//   - a search filter that still matches more than 500 rows.
//
// Expected sequences are computed in Go from the rows as stored: the column in
// the requested direction, then id in the same direction, NULL before every
// value. The fixture values are lowercase ASCII of equal length, so Go's byte
// order agrees with MySQL's case-insensitive collation and SQLite's BINARY
// collation, and the engines are expected to produce the identical sequence.
const (
	exportTraversalRows    = 1203
	exportTraversalErased  = 24 // the lowest ids are erased, keeping ascending positions aligned
	exportTraversalOldSize = 500
)

var exportTraversalBase = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// exportTraversalColumns is the export's sort allowlist, as the handler exposes
// it (customerSortColumns in internal/handler/customer_handler.go). The handler
// package cannot be imported from here without an import cycle, so the list is
// repeated; a column added there must be added here.
var exportTraversalColumns = []struct {
	name string
	ties bool // the fixture gives this column duplicate values
}{
	{"created_at", true},
	{"updated_at", true},
	{"first_name", true},
	{"last_name", true},
	{"email", false},
	{"company", true},
}

type exportTraversalFixture struct {
	live        []models.Customer
	erasedIDs   []uint
	nullCompany map[uint]bool
}

func TestCustomerExportTraversalSQLite(t *testing.T) {
	runCustomerExportTraversal(t, openCustomerExportTraversalDB(t, false))
}

func TestCustomerExportTraversalMySQL(t *testing.T) {
	runCustomerExportTraversal(t, openCustomerExportTraversalDB(t, true))
}

// openCustomerExportTraversalDB opens a private SQLite database, or a uniquely
// named disposable schema on the TEST_MYSQL_DSN server. MySQL is opt-in: the
// root skips without the variable and fails, rather than skipping, when the
// variable names a server that cannot be reached.
func openCustomerExportTraversalDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	if !useMySQL {
		db, err := gorm.Open(sqlite.Open("file:customer_export_traversal?mode=memory&cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{
			Logger: logger.Discard,
		})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		// One connection, as the production SQLite pool has.
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Customer{}))
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
	name := fmt.Sprintf("gocrm_customer_export_%d", time.Now().UnixNano())
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
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Customer{}))
	return db
}

// seedExportTraversal inserts the adversarial fixture and returns the live rows
// as the database stores them.
//
// Row i (inserted i-th, so with the i-th smallest id) carries rank = rows-1-i,
// and every sort value grows with rank, i.e. shrinks as id grows. Values are
// grouped so that consecutive ranks share them: first_name in sevens, last_name
// in elevens, company in thirteens, created_at in nines, updated_at in threes.
// The 24 highest ranks (the 24 lowest ids) are erased through the repository,
// which keeps the ascending positions of every other row where arithmetic puts
// them. Two whole company groups near the top of the order are replaced by NULL
// and by the empty string; both sort to the front, shifting the rest by whole
// groups, so the boundary ties below still hold.
func seedExportTraversal(t *testing.T, db *gorm.DB) exportTraversalFixture {
	t.Helper()

	rows := make([]models.Customer, 0, exportTraversalRows)
	for i := 0; i < exportTraversalRows; i++ {
		rank := exportTraversalRows - 1 - i
		notes := "beta-notes"
		if i%3 != 0 {
			notes = "alpha-notes"
		}
		company := fmt.Sprintf("co%04d", rank/13)
		if rank/13 == 85 {
			company = ""
		}
		rows = append(rows, models.Customer{
			BaseModel: models.BaseModel{
				CreatedAt: exportTraversalBase.Add(time.Duration(rank/9) * time.Second),
				UpdatedAt: exportTraversalBase.Add(time.Duration(rank/3) * time.Second),
			},
			FirstName: fmt.Sprintf("fn%04d", rank/7),
			LastName:  fmt.Sprintf("ln%04d", rank/11),
			Email:     fmt.Sprintf("u%04d@export.test", rank),
			Phone:     fmt.Sprintf("+1 555 %04d", i),
			Company:   company,
			Notes:     notes,
		})
	}
	require.NoError(t, db.CreateInBatches(&rows, 200).Error)
	for i := 1; i < len(rows); i++ {
		require.Greater(t, rows[i].ID, rows[i-1].ID, "ids must grow in insertion order for the fixture to run against them")
	}

	// A legacy-shaped NULL in the nullable company column. UpdateColumn leaves
	// updated_at alone, which the fixture relies on.
	var nullIDs []uint
	for i := range rows {
		if rank := exportTraversalRows - 1 - i; rank/13 == 84 {
			nullIDs = append(nullIDs, rows[i].ID)
		}
	}
	require.Len(t, nullIDs, 13)
	require.NoError(t, db.Model(&models.Customer{}).Where("id IN ?", nullIDs).UpdateColumn("company", nil).Error)

	repo := NewCustomerRepository(db)
	erased := make([]uint, 0, exportTraversalErased)
	for i := 0; i < exportTraversalErased; i++ {
		require.NoError(t, repo.Delete(rows[i].ID))
		erased = append(erased, rows[i].ID)
	}

	var live []models.Customer
	require.NoError(t, db.Order("id").Find(&live).Error)
	require.Len(t, live, exportTraversalRows-exportTraversalErased)

	var storedNull []uint
	require.NoError(t, db.Model(&models.Customer{}).Where("company IS NULL").Pluck("id", &storedNull).Error)
	require.Len(t, storedNull, 13, "the NULL company rows must be live")
	nullCompany := make(map[uint]bool, len(storedNull))
	for _, id := range storedNull {
		nullCompany[id] = true
	}

	// The explicit timestamps must have been stored, or the time columns would
	// degenerate into one big tie and stop disagreeing with id order.
	distinctCreated := map[int64]bool{}
	for _, row := range live {
		distinctCreated[row.CreatedAt.Unix()] = true
	}
	require.Greater(t, len(distinctCreated), 100, "created_at fixture values were not stored as set")

	return exportTraversalFixture{live: live, erasedIDs: erased, nullCompany: nullCompany}
}

// compareExportColumn orders two rows by one sort column the way the database
// does: NULL first, then the value. The caller breaks ties by id.
func compareExportColumn(column string, a, b *models.Customer, nullCompany map[uint]bool) int {
	switch column {
	case "created_at":
		return compareExportTimes(a.CreatedAt, b.CreatedAt)
	case "updated_at":
		return compareExportTimes(a.UpdatedAt, b.UpdatedAt)
	case "first_name":
		return strings.Compare(a.FirstName, b.FirstName)
	case "last_name":
		return strings.Compare(a.LastName, b.LastName)
	case "email":
		return strings.Compare(a.Email, b.Email)
	case "company":
		aNull, bNull := nullCompany[a.ID], nullCompany[b.ID]
		switch {
		case aNull && bNull:
			return 0
		case aNull:
			return -1
		case bNull:
			return 1
		}
		return strings.Compare(a.Company, b.Company)
	}
	panic("unknown export sort column " + column)
}

func compareExportTimes(a, b time.Time) int {
	switch {
	case a.Before(b):
		return -1
	case a.After(b):
		return 1
	}
	return 0
}

// expectedExportOrder sorts the rows by the column and then by id, both in the
// requested direction; an empty column is the default order, id ascending.
func expectedExportOrder(rows []models.Customer, column string, desc bool, nullCompany map[uint]bool) []models.Customer {
	sorted := append([]models.Customer(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		c := 0
		if column != "" {
			c = compareExportColumn(column, &sorted[i], &sorted[j], nullCompany)
		}
		if c == 0 {
			switch {
			case sorted[i].ID < sorted[j].ID:
				c = -1
			case sorted[i].ID > sorted[j].ID:
				c = 1
			}
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
	return sorted
}

// matchesExportSearch mirrors the repository's LIKE filter for the fixture's
// lowercase ASCII values (a NULL company matches nothing, as in SQL).
func matchesExportSearch(c *models.Customer, term string) bool {
	for _, field := range []string{c.FirstName, c.LastName, c.Email, c.Company, c.Phone, c.Notes} {
		if strings.Contains(field, term) {
			return true
		}
	}
	return false
}

func filterExportRows(rows []models.Customer, term string) []models.Customer {
	if term == "" {
		return rows
	}
	filtered := make([]models.Customer, 0, len(rows))
	for _, row := range rows {
		if matchesExportSearch(&row, term) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func exportIDs(rows []models.Customer) []uint {
	ids := make([]uint, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids
}

func assertExportSequence(t *testing.T, expected []models.Customer, got []models.Customer) {
	t.Helper()
	require.Len(t, got, len(expected), "every matching row exactly once")
	seen := make(map[uint]bool, len(got))
	for _, row := range got {
		require.False(t, seen[row.ID], "customer %d exported twice", row.ID)
		seen[row.ID] = true
	}
	require.Equal(t, exportIDs(expected), exportIDs(got), "exported ids out of the requested order")
}

func runCustomerExportTraversal(t *testing.T, db *gorm.DB) {
	fixture := seedExportTraversal(t, db)
	repo := NewCustomerRepository(db)
	const search = "alpha"

	t.Run("fixture_is_adversarial", func(t *testing.T) {
		filtered := filterExportRows(fixture.live, search)
		require.Greater(t, len(fixture.live), 2*exportTraversalOldSize, "the table must span more than two old batches")
		require.Greater(t, len(filtered), exportTraversalOldSize, "the search must still match more than one old batch")

		for _, column := range exportTraversalColumns {
			expected := expectedExportOrder(fixture.live, column.name, false, fixture.nullCompany)
			ids := exportIDs(expected)
			assert.False(t, sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] < ids[j] }),
				"%s ascending must disagree with id order", column.name)
			if !column.ties {
				continue
			}
			distinct := map[string]bool{}
			for i := range expected {
				distinct[fmt.Sprint(exportColumnValue(column.name, &expected[i], fixture.nullCompany))] = true
			}
			assert.Less(t, len(distinct), len(expected), "%s must have duplicate values", column.name)
			for _, boundary := range []int{exportTraversalOldSize, 2 * exportTraversalOldSize} {
				require.Less(t, boundary, len(expected))
				assert.Zero(t, compareExportColumn(column.name, &expected[boundary-1], &expected[boundary], fixture.nullCompany),
					"%s: positions %d and %d must tie across the old batch boundary", column.name, boundary-1, boundary)
			}
		}
	})

	t.Run("default_order_is_id_ascending", func(t *testing.T) {
		got, err := repo.ListAllForExport("", "", "")
		require.NoError(t, err)
		assertExportSequence(t, expectedExportOrder(fixture.live, "", false, fixture.nullCompany), got)

		got, err = repo.ListAllForExport(search, "", "")
		require.NoError(t, err)
		assertExportSequence(t, expectedExportOrder(filterExportRows(fixture.live, search), "", false, fixture.nullCompany), got)
	})

	for _, column := range exportTraversalColumns {
		for _, direction := range []string{"asc", "desc"} {
			desc := direction == "desc"
			t.Run(column.name+"_"+direction, func(t *testing.T) {
				got, err := repo.ListAllForExport("", column.name, direction)
				require.NoError(t, err)
				assertExportSequence(t, expectedExportOrder(fixture.live, column.name, desc, fixture.nullCompany), got)
			})
			t.Run(column.name+"_"+direction+"_search", func(t *testing.T) {
				got, err := repo.ListAllForExport(search, column.name, direction)
				require.NoError(t, err)
				assertExportSequence(t, expectedExportOrder(filterExportRows(fixture.live, search), column.name, desc, fixture.nullCompany), got)
			})
		}
		t.Run(column.name+"_desc_reverses_asc", func(t *testing.T) {
			asc, err := repo.ListAllForExport("", column.name, "asc")
			require.NoError(t, err)
			desc, err := repo.ListAllForExport("", column.name, "desc")
			require.NoError(t, err)
			require.Len(t, desc, len(asc))
			for i := range asc {
				require.Equal(t, asc[i].ID, desc[len(desc)-1-i].ID, "descending must be the exact reverse of ascending")
			}
		})
	}

	t.Run("erased_rows_are_excluded", func(t *testing.T) {
		got, err := repo.ListAllForExport("", "email", "asc")
		require.NoError(t, err)
		exported := make(map[uint]bool, len(got))
		for _, row := range got {
			exported[row.ID] = true
		}
		require.Len(t, fixture.erasedIDs, exportTraversalErased)
		for _, id := range fixture.erasedIDs {
			assert.False(t, exported[id], "erased customer %d reappeared in the export", id)
		}
	})

	t.Run("output_is_repeatable", func(t *testing.T) {
		first, err := repo.ListAllForExport(search, "company", "desc")
		require.NoError(t, err)
		second, err := repo.ListAllForExport(search, "company", "desc")
		require.NoError(t, err)
		require.Equal(t, exportIDs(first), exportIDs(second))
	})
}

// exportColumnValue is the fixture's view of one sort value, for counting
// distinct values; NULL is reported distinctly from the empty string.
func exportColumnValue(column string, c *models.Customer, nullCompany map[uint]bool) interface{} {
	switch column {
	case "created_at":
		return c.CreatedAt.UnixNano()
	case "updated_at":
		return c.UpdatedAt.UnixNano()
	case "first_name":
		return c.FirstName
	case "last_name":
		return c.LastName
	case "email":
		return c.Email
	case "company":
		if nullCompany[c.ID] {
			return nil
		}
		return c.Company
	}
	panic("unknown export sort column " + column)
}
