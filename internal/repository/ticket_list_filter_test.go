package repository

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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

// #218: ListByFilter and CountByFilter share whereTicketListFilter. These roots
// seed tickets covering every status, priority and assignee (two agents and
// unassigned), tied titles and timestamps, a search term spread over the three
// searched columns and soft-deleted rows, then check every filter against an
// in-memory model: the exact page sequence in both directions under every
// sort, the count, and that nothing outside the filter leaks through. They
// run on a file-backed SQLite database with one connection and, when
// TEST_MYSQL_DSN is set, on a disposable MySQL schema; the MySQL root skips
// without the variable and fails when it is unreachable.

func TestTicketListFilterRepositorySQLite(t *testing.T) {
	runTicketListFilterRepository(t, openTicketListFilterDB(t, false))
}

func TestTicketListFilterRepositoryMySQL(t *testing.T) {
	runTicketListFilterRepository(t, openTicketListFilterDB(t, true))
}

func openTicketListFilterDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{Logger: logger.Discard}
	var db *gorm.DB
	if !useMySQL {
		var err error
		dsn := filepath.Join(t.TempDir(), "ticket_list_filter.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
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
		name := fmt.Sprintf("gocrm_ticket_list_filter_%d", time.Now().UnixNano())
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
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Customer{}, &models.Ticket{}, &models.TicketComment{}))
	return db
}

// filterFixture is the in-memory copy of one seeded ticket.
type filterFixture struct {
	id          uint
	status      string
	priority    string
	assignee    *uint
	title       string
	description string
	resolution  string
	createdAt   time.Time
	updatedAt   time.Time
	deleted     bool
}

const ticketFilterNeedle = "needle"

func (f filterFixture) matches(filter TicketListFilter) bool {
	if f.deleted {
		return false
	}
	if filter.AssigneeID != nil && (f.assignee == nil || *f.assignee != *filter.AssigneeID) {
		return false
	}
	if filter.Status != "" && f.status != filter.Status {
		return false
	}
	if filter.Priority != "" && f.priority != filter.Priority {
		return false
	}
	if filter.Search != "" && !strings.Contains(f.title, filter.Search) &&
		!strings.Contains(f.description, filter.Search) && !strings.Contains(f.resolution, filter.Search) {
		return false
	}
	return true
}

// compareBy compares two fixtures on one sort column, 0 when tied.
func compareBy(a, b filterFixture, column string) int {
	switch column {
	case "title":
		return strings.Compare(a.title, b.title)
	case "status":
		return strings.Compare(a.status, b.status)
	case "priority":
		return strings.Compare(a.priority, b.priority)
	case "created_at":
		return a.createdAt.Compare(b.createdAt)
	case "updated_at":
		return a.updatedAt.Compare(b.updatedAt)
	}
	return 0
}

// expectedFilterOrder is orderPage's total order over the matching fixtures:
// id ascending without a sort column, otherwise the column and then id, both
// in the requested direction.
func expectedFilterOrder(all []filterFixture, filter TicketListFilter, sortBy, sortOrder string) []uint {
	rows := []filterFixture{}
	for _, f := range all {
		if f.matches(filter) {
			rows = append(rows, f)
		}
	}
	desc := sortBy != "" && sortOrder == "desc"
	sort.SliceStable(rows, func(i, j int) bool {
		c := compareBy(rows[i], rows[j], sortBy)
		if c == 0 {
			if rows[i].id < rows[j].id {
				c = -1
			} else if rows[i].id > rows[j].id {
				c = 1
			}
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
	ids := make([]uint, 0, len(rows))
	for _, f := range rows {
		ids = append(ids, f.id)
	}
	return ids
}

func describeFilter(filter TicketListFilter) string {
	assignee := "any"
	if filter.AssigneeID != nil {
		assignee = fmt.Sprint(*filter.AssigneeID)
	}
	return fmt.Sprintf("assignee=%s status=%q priority=%q search=%q", assignee, filter.Status, filter.Priority, filter.Search)
}

func runTicketListFilterRepository(t *testing.T, db *gorm.DB) {
	repo := NewTicketRepository(db)
	stamp := time.Now().UnixNano()

	newUser := func(name string, role models.UserRole) *models.User {
		u := &models.User{Email: fmt.Sprintf("%s-%d@ticket-filter.test", name, stamp), Password: "x",
			FirstName: "F", LastName: name, Role: role, IsActive: true}
		require.NoError(t, db.Create(u).Error)
		return u
	}
	sue := newUser("sue", models.RoleSupport)
	sam := newUser("sam", models.RoleSupport)
	customer := &models.Customer{FirstName: "C", LastName: "Customer", Email: fmt.Sprintf("customer-%d@ticket-filter.test", stamp)}
	require.NoError(t, db.Create(customer).Error)

	statuses := []models.TicketStatus{models.TicketStatusOpen, models.TicketStatusInProgress, models.TicketStatusResolved, models.TicketStatusClosed}
	priorities := []models.TicketPriority{models.TicketPriorityLow, models.TicketPriorityMedium, models.TicketPriorityHigh, models.TicketPriorityUrgent}
	// Distinct first letters keep the order identical under SQLite's binary
	// and MySQL's case-insensitive collation.
	titles := []string{"alpha", "bravo", ticketFilterNeedle}
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

	var all []filterFixture
	for i := 0; i < 48; i++ {
		f := filterFixture{
			status:      string(statuses[i%4]),
			priority:    string(priorities[(i/4)%4]),
			title:       titles[(i/2)%3],
			description: "routine request",
			resolution:  "",
			createdAt:   base.Add(time.Duration(i%4) * time.Minute),
			updatedAt:   base.Add(time.Duration(i%2) * time.Minute),
		}
		switch i % 3 {
		case 0:
			f.assignee = &sue.ID
		case 1:
			f.assignee = &sam.ID
		}
		if i%5 == 0 {
			f.description = "a " + ticketFilterNeedle + " in the description"
		}
		if i%7 == 0 {
			f.resolution = "found the " + ticketFilterNeedle
		}
		ticket := &models.Ticket{Title: f.title, Description: f.description, Resolution: f.resolution,
			Status: models.TicketStatus(f.status), Priority: models.TicketPriority(f.priority),
			CustomerID: customer.ID, AssignedToID: f.assignee}
		require.NoError(t, db.Create(ticket).Error)
		require.NoError(t, db.Model(ticket).UpdateColumns(map[string]interface{}{
			"created_at": f.createdAt, "updated_at": f.updatedAt}).Error)
		f.id = ticket.ID
		if i%11 == 0 {
			require.NoError(t, db.Delete(ticket).Error)
			f.deleted = true
		}
		all = append(all, f)
	}
	byID := map[uint]filterFixture{}
	for _, f := range all {
		byID[f.id] = f
	}

	zero := uint(0)
	filters := []TicketListFilter{{}}
	for _, s := range statuses {
		filters = append(filters, TicketListFilter{Status: string(s)})
	}
	for _, p := range priorities {
		filters = append(filters, TicketListFilter{Priority: string(p)})
	}
	filters = append(filters,
		TicketListFilter{Status: "open", Priority: "low"},
		TicketListFilter{Status: "closed", Priority: "urgent"},
		TicketListFilter{Status: "in_progress", Priority: "high", Search: ticketFilterNeedle},
		TicketListFilter{Status: "resolved", Search: ticketFilterNeedle},
		TicketListFilter{Priority: "medium", Search: ticketFilterNeedle},
		TicketListFilter{Search: ticketFilterNeedle},
		TicketListFilter{AssigneeID: &sue.ID},
		TicketListFilter{AssigneeID: &sue.ID, Status: "open"},
		TicketListFilter{AssigneeID: &sam.ID, Priority: "urgent"},
		TicketListFilter{AssigneeID: &sue.ID, Status: "open", Priority: "low", Search: ticketFilterNeedle},
		TicketListFilter{AssigneeID: &sam.ID, Search: ticketFilterNeedle},
		TicketListFilter{AssigneeID: &zero},
		TicketListFilter{AssigneeID: &zero, Status: "open", Search: ticketFilterNeedle},
		// A search term matching nothing.
		TicketListFilter{Search: "zz-no-such-ticket"},
	)

	sorts := []struct{ by, order string }{
		{"", "asc"}, {"", "desc"},
		{"title", "asc"}, {"title", "desc"},
		{"status", "asc"}, {"status", "desc"},
		{"priority", "asc"}, {"priority", "desc"},
		{"created_at", "asc"}, {"created_at", "desc"},
		{"updated_at", "asc"}, {"updated_at", "desc"},
	}

	t.Run("pages walk the filtered total order and the count matches", func(t *testing.T) {
		for _, filter := range filters {
			count, err := repo.CountByFilter(filter)
			require.NoError(t, err)
			expectedAll := expectedFilterOrder(all, filter, "", "asc")
			assert.Equal(t, int64(len(expectedAll)), count, describeFilter(filter))

			for _, s := range sorts {
				want := expectedFilterOrder(all, filter, s.by, s.order)
				context := fmt.Sprintf("%s sort=%s/%s", describeFilter(filter), s.by, s.order)
				for _, limit := range []int{3, 7} {
					got := []uint{}
					for offset := 0; offset < len(want)+limit; offset += limit {
						rows, err := repo.ListByFilter(filter, offset, limit, s.by, s.order)
						require.NoError(t, err, context)
						assert.LessOrEqual(t, len(rows), limit, context)
						for _, row := range rows {
							got = append(got, row.ID)
						}
						if offset >= len(want) {
							assert.Empty(t, rows, "%s: a page past the end is empty", context)
						}
					}
					assert.Equal(t, want, got, "%s limit=%d: no repeats, no gaps", context, limit)
				}
			}
		}
	})

	t.Run("nothing outside the filter leaks through the search OR", func(t *testing.T) {
		for _, filter := range filters {
			rows, err := repo.ListByFilter(filter, 0, 1000, "", "")
			require.NoError(t, err)
			for _, row := range rows {
				f, ok := byID[row.ID]
				require.True(t, ok)
				context := fmt.Sprintf("%s: ticket %d", describeFilter(filter), row.ID)
				assert.False(t, f.deleted, "%s is soft-deleted", context)
				if filter.Status != "" {
					assert.Equal(t, filter.Status, string(row.Status), context)
				}
				if filter.Priority != "" {
					assert.Equal(t, filter.Priority, string(row.Priority), context)
				}
				if filter.AssigneeID != nil {
					require.NotNil(t, row.AssignedToID, context)
					assert.Equal(t, *filter.AssigneeID, *row.AssignedToID, context)
				}
			}
		}
		// The search alone matches rows of every status, priority and assignee,
		// so the narrower results above are the predicate's doing.
		rows, err := repo.ListByFilter(TicketListFilter{Search: ticketFilterNeedle}, 0, 1000, "", "")
		require.NoError(t, err)
		seenStatus, seenPriority, seenAssignee := map[models.TicketStatus]bool{}, map[models.TicketPriority]bool{}, map[string]bool{}
		for _, row := range rows {
			seenStatus[row.Status] = true
			seenPriority[row.Priority] = true
			if row.AssignedToID == nil {
				seenAssignee["none"] = true
			} else {
				seenAssignee[fmt.Sprint(*row.AssignedToID)] = true
			}
		}
		assert.Len(t, seenStatus, 4)
		assert.Len(t, seenPriority, 4)
		assert.Len(t, seenAssignee, 3)
	})

	t.Run("an assignee id of zero matches no ticket", func(t *testing.T) {
		for _, filter := range []TicketListFilter{{AssigneeID: &zero}, {AssigneeID: &zero, Status: "open"}, {AssigneeID: &zero, Search: ticketFilterNeedle}} {
			rows, err := repo.ListByFilter(filter, 0, 100, "", "")
			require.NoError(t, err)
			assert.Empty(t, rows)
			count, err := repo.CountByFilter(filter)
			require.NoError(t, err)
			assert.Equal(t, int64(0), count)
		}
	})

	t.Run("soft-deleted tickets are never counted or listed", func(t *testing.T) {
		deleted := 0
		for _, f := range all {
			if f.deleted {
				deleted++
				filter := TicketListFilter{Status: f.status, Priority: f.priority, AssigneeID: f.assignee, Search: f.title}
				rows, err := repo.ListByFilter(filter, 0, 1000, "", "")
				require.NoError(t, err)
				for _, row := range rows {
					assert.NotEqual(t, f.id, row.ID)
				}
				count, err := repo.CountByFilter(filter)
				require.NoError(t, err)
				assert.Equal(t, int64(len(rows)), count)
			}
		}
		require.Greater(t, deleted, 0, "the fixture must hold soft-deleted rows")
	})

	t.Run("an empty filter returns what Count and List return", func(t *testing.T) {
		count, err := repo.Count()
		require.NoError(t, err)
		filtered, err := repo.CountByFilter(TicketListFilter{})
		require.NoError(t, err)
		assert.Equal(t, count, filtered)
		for _, limit := range []int{5, 20} {
			for offset := 0; offset <= int(count); offset += limit {
				plain, err := repo.List(offset, limit)
				require.NoError(t, err)
				rows, err := repo.ListByFilter(TicketListFilter{}, offset, limit, "", "", "Customer", "AssignedTo")
				require.NoError(t, err)
				require.Equal(t, len(plain), len(rows))
				for i := range plain {
					assert.Equal(t, plain[i].ID, rows[i].ID)
				}
			}
		}
	})

	t.Run("preloads are applied", func(t *testing.T) {
		rows, err := repo.ListByFilter(TicketListFilter{AssigneeID: &sue.ID}, 0, 100, "", "", "Customer", "AssignedTo")
		require.NoError(t, err)
		require.NotEmpty(t, rows)
		for _, row := range rows {
			assert.Equal(t, customer.ID, row.Customer.ID)
			require.NotNil(t, row.AssignedTo)
			assert.Equal(t, sue.ID, row.AssignedTo.ID)
		}
	})

	t.Run("an unknown sort column is refused", func(t *testing.T) {
		_, err := repo.ListByFilter(TicketListFilter{Status: "open"}, 0, 10, "title; DROP TABLE tickets", "asc")
		assert.Error(t, err)
	})
}
