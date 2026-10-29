package repository

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// Regression roots for #110: every paginated list and search over leads,
// customers, tasks (including the label join), tickets, users and companies
// must walk a total order, so that consecutive pages of an unchanged table
// neither repeat nor skip a row however many rows share the sort value.
//
// The fixture is deliberately hostile to an order without a unique
// tie-breaker: every row of an entity carries the SAME created_at, and each
// other sort column holds three or four values spread across the rows so that
// equal values never sit next to each other in id order. A page boundary
// therefore always falls inside a run of ties.
//
// The contract under test, computed in Go from the rows as inserted:
//   - no sort requested: id ascending (companies: created_at then id, both
//     descending, as the company list always documented "newest first");
//   - a sort requested: that column in the requested direction, then id in the
//     same direction.
//
// Every page is fetched with a separate statement, the concatenation must equal
// the expected sequence exactly (so no duplicate and no missing id), and the
// matching count method must agree with it.
const paginationPageSize = 3

var paginationCreatedAt = time.Date(2026, time.March, 3, 10, 0, 0, 0, time.UTC)

func TestPaginationDeterminismSQLite(t *testing.T) {
	runPaginationDeterminism(t, openPaginationDB(t, false))
}

func TestPaginationDeterminismMySQL(t *testing.T) {
	runPaginationDeterminism(t, openPaginationDB(t, true))
}

var paginationModels = []interface{}{
	&models.User{}, &models.Company{}, &models.Lead{}, &models.Customer{}, &models.Deal{},
	&models.Label{}, &models.Task{}, &models.Ticket{}, &models.TicketComment{},
}

// openPaginationDB opens a private SQLite database, or a uniquely named
// disposable schema on the TEST_MYSQL_DSN server. The MySQL root skips without
// the variable and fails when the variable names an unreachable server.
func openPaginationDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	return openDisposableDB(t, "pagination_determinism", useMySQL, paginationModels...)
}

// openDisposableDB opens a private named in-memory SQLite database, or a
// uniquely named disposable schema on the TEST_MYSQL_DSN server, and
// AutoMigrates the given models into it.
func openDisposableDB(t *testing.T, name string, useMySQL bool, migrate ...interface{}) *gorm.DB {
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
		require.NoError(t, db.AutoMigrate(migrate...))
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
	require.NoError(t, db.AutoMigrate(migrate...))
	return db
}

// pageSort is one requested ordering. An empty by is "no sort requested".
type pageSort struct {
	by    string
	order string
}

func (s pageSort) desc() bool { return s.order == "desc" }

func (s pageSort) String() string {
	if s.by == "" {
		return "default"
	}
	return s.by + "_" + s.order
}

// tiedSorts are the orderings every path is checked under: the default, the
// all-equal created_at in both directions and one tied business column in both
// directions.
func tiedSorts(column string) []pageSort {
	return []pageSort{
		{"", ""},
		{"created_at", "asc"},
		{"created_at", "desc"},
		{column, "asc"},
		{column, "desc"},
	}
}

// expectedIDs filters rows and orders them under the contract described at the
// top of the file. key returns the sort column's value as stored; the fixture
// uses plain lowercase ASCII, so Go's byte order agrees with MySQL's
// case-insensitive collation and SQLite's BINARY collation.
func expectedIDs[T any](rows []T, id func(T) uint, keep func(T) bool, key func(T) string, desc bool) []uint {
	type keyed struct {
		id  uint
		key string
	}
	var picked []keyed
	for _, row := range rows {
		if keep != nil && !keep(row) {
			continue
		}
		k := ""
		if key != nil {
			k = key(row)
		}
		picked = append(picked, keyed{id(row), k})
	}
	sort.Slice(picked, func(i, j int) bool {
		a, b := picked[i], picked[j]
		if desc {
			a, b = b, a
		}
		if a.key != b.key {
			return a.key < b.key
		}
		return a.id < b.id
	})
	out := make([]uint, 0, len(picked))
	for _, p := range picked {
		out = append(out, p.id)
	}
	return out
}

// assertPagesWalkTotalOrder fetches every page of one path and checks it
// against the expected sequence and the path's count.
func assertPagesWalkTotalOrder(t *testing.T, name string, expected []uint, fetch func(offset, limit int) ([]uint, error), count func() (int64, error)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		require.Greater(t, len(expected), paginationPageSize, "the fixture must span several pages for this path")
		var got []uint
		seen := map[uint]bool{}
		for offset := 0; offset < len(expected)+paginationPageSize; offset += paginationPageSize {
			ids, err := fetch(offset, paginationPageSize)
			require.NoError(t, err)
			require.LessOrEqual(t, len(ids), paginationPageSize)
			for _, id := range ids {
				assert.False(t, seen[id], "id %d appears on more than one page", id)
				seen[id] = true
			}
			got = append(got, ids...)
		}
		assert.Equal(t, expected, got, "the pages must concatenate to the total order")
		if count != nil {
			n, err := count()
			require.NoError(t, err)
			assert.EqualValues(t, len(expected), n, "count must match the rows the pages return")
		}
	})
}

func idsOf[T any](rows []T, id func(T) uint) []uint {
	out := make([]uint, 0, len(rows))
	for _, row := range rows {
		out = append(out, id(row))
	}
	return out
}

// spread picks values[i*step % len] so that equal values are never adjacent in
// insertion (id) order.
func spread[T any](values []T, i int) T {
	return values[(i*5+i/len(values))%len(values)]
}

type paginationFixture struct {
	users     []models.User
	leads     []models.Lead
	customers []models.Customer
	companies []models.Company
	tasks     []models.Task
	tickets   []models.Ticket
	label     models.Label
	otherTag  models.Label
	taskLabel map[uint]bool
}

func seedPagination(t *testing.T, db *gorm.DB) paginationFixture {
	t.Helper()
	const rows = 16
	f := paginationFixture{taskLabel: map[uint]bool{}}
	base := models.BaseModel{CreatedAt: paginationCreatedAt, UpdatedAt: paginationCreatedAt}

	roles := []models.UserRole{models.RoleSales, models.RoleSupport, models.RoleAdmin}
	for i := 0; i < rows; i++ {
		last := "pager"
		if i%4 == 0 {
			last = "other"
		}
		u := models.User{BaseModel: base, Email: fmt.Sprintf("user%02d@pages.test", i), Password: "x",
			FirstName: "first", LastName: last, Role: spread(roles, i), IsActive: true}
		require.NoError(t, db.Create(&u).Error)
		f.users = append(f.users, u)
	}
	ownerA, ownerB := f.users[0].ID, f.users[1].ID

	industries := []string{"retail", "energy", "health"}
	for i := 0; i < rows; i++ {
		c := models.Company{BaseModel: base, Name: fmt.Sprintf("company %02d", i),
			Domain: fmt.Sprintf("c%02d.pages.test", i), Industry: spread(industries, i), City: "berlin"}
		if i%4 == 0 {
			c.City = "lisbon"
		}
		require.NoError(t, db.Create(&c).Error)
		f.companies = append(f.companies, c)
	}
	linked := f.companies[0].ID

	statuses := []models.LeadStatus{models.LeadStatusNew, models.LeadStatusContacted, models.LeadStatusQualified}
	for i := 0; i < rows; i++ {
		notes := "pager"
		if i%4 == 1 {
			notes = "quiet"
		}
		class := models.LeadClassificationLead
		if i%3 == 2 {
			class = models.LeadClassificationSpam
		}
		owner := ownerA
		if i%3 == 1 {
			owner = ownerB
		}
		l := models.Lead{BaseModel: base, FirstName: "lead", LastName: "row", Email: fmt.Sprintf("lead%02d@pages.test", i),
			Company: "acme", Status: spread(statuses, i), Classification: class, Notes: notes, OwnerID: owner}
		if i%4 != 3 {
			l.CompanyID = &linked
		}
		require.NoError(t, db.Create(&l).Error)
		f.leads = append(f.leads, l)
	}

	companies := []string{"gamma", "alpha", "delta"}
	for i := 0; i < rows; i++ {
		notes := "pager"
		if i%4 == 2 {
			notes = "quiet"
		}
		c := models.Customer{BaseModel: base, FirstName: "cust", LastName: "row", Email: fmt.Sprintf("cust%02d@pages.test", i),
			Company: spread(companies, i), Notes: notes}
		if i%4 != 1 {
			c.CompanyID = &linked
		}
		require.NoError(t, db.Create(&c).Error)
		f.customers = append(f.customers, c)
	}

	f.label = models.Label{BaseModel: base, Name: "pages", Color: "#112233"}
	require.NoError(t, db.Create(&f.label).Error)
	f.otherTag = models.Label{BaseModel: base, Name: "other", Color: "#445566"}
	require.NoError(t, db.Create(&f.otherTag).Error)

	taskPriorities := []models.TaskPriority{models.TaskPriorityLow, models.TaskPriorityMedium, models.TaskPriorityHigh}
	for i := 0; i < rows; i++ {
		desc := "pager"
		if i%4 == 3 {
			desc = "quiet"
		}
		assignee := ownerA
		if i%3 == 0 {
			assignee = ownerB
		}
		task := models.Task{BaseModel: base, Title: fmt.Sprintf("task %02d", i), Description: desc,
			Priority: spread(taskPriorities, i), AssignedToID: assignee}
		require.NoError(t, db.Omit("Labels").Create(&task).Error)
		// Most tasks carry the label under test; some carry both labels so the
		// join produces rows that must not duplicate the task, and some carry
		// only the other label.
		switch i % 5 {
		case 0:
			require.NoError(t, db.Model(&task).Association("Labels").Append(&f.label, &f.otherTag))
			f.taskLabel[task.ID] = true
		case 4:
			require.NoError(t, db.Model(&task).Association("Labels").Append(&f.otherTag))
		default:
			require.NoError(t, db.Model(&task).Association("Labels").Append(&f.label))
			f.taskLabel[task.ID] = true
		}
		f.tasks = append(f.tasks, task)
	}

	ticketPriorities := []models.TicketPriority{models.TicketPriorityLow, models.TicketPriorityMedium, models.TicketPriorityHigh, models.TicketPriorityUrgent}
	supportA, supportB := f.users[2].ID, f.users[3].ID
	for i := 0; i < rows*2; i++ {
		desc := "pager"
		if i%4 == 0 {
			desc = "quiet"
		}
		ticket := models.Ticket{BaseModel: base, Title: fmt.Sprintf("ticket %02d", i), Description: desc,
			Priority: spread(ticketPriorities, i), CustomerID: f.customers[i%2].ID}
		switch i % 3 {
		case 0:
			ticket.AssignedToID = &supportA
		case 1:
			ticket.AssignedToID = &supportB
		}
		require.NoError(t, db.Create(&ticket).Error)
		f.tickets = append(f.tickets, ticket)
	}
	return f
}

func runPaginationDeterminism(t *testing.T, db *gorm.DB) {
	f := seedPagination(t, db)

	t.Run("leads", func(t *testing.T) { checkLeadPagination(t, db, f) })
	t.Run("customers", func(t *testing.T) { checkCustomerPagination(t, db, f) })
	t.Run("tasks", func(t *testing.T) { checkTaskPagination(t, db, f) })
	t.Run("tickets", func(t *testing.T) { checkTicketPagination(t, db, f) })
	t.Run("users", func(t *testing.T) { checkUserPagination(t, db, f) })
	t.Run("companies", func(t *testing.T) { checkCompanyPagination(t, db, f) })
}

func leadID(l models.Lead) uint { return l.ID }

func leadKey(column string) func(models.Lead) string {
	switch column {
	case "status":
		return func(l models.Lead) string { return string(l.Status) }
	default: // created_at is identical on every row; the default sorts by id alone
		return nil
	}
}

func checkLeadPagination(t *testing.T, db *gorm.DB, f paginationFixture) {
	repo := NewLeadRepository(db)
	all := func(models.Lead) bool { return true }
	matches := func(l models.Lead) bool { return l.Notes == "pager" }

	assertPagesWalkTotalOrder(t, "ListWithPreloads", expectedIDs(f.leads, leadID, all, nil, false),
		func(o, l int) ([]uint, error) {
			rows, err := repo.ListWithPreloads(o, l, "Owner")
			return idsOf(rows, leadID), err
		}, repo.Count)

	for _, s := range tiedSorts("status") {
		s := s
		assertPagesWalkTotalOrder(t, "ListSortedWithPreloads/"+s.String(), expectedIDs(f.leads, leadID, all, leadKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.ListSortedWithPreloads(o, l, s.by, s.order, "Owner")
				return idsOf(rows, leadID), err
			}, repo.Count)
		assertPagesWalkTotalOrder(t, "Search/"+s.String(), expectedIDs(f.leads, leadID, matches, leadKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.Search("pager", o, l, s.by, s.order, "Owner")
				return idsOf(rows, leadID), err
			}, func() (int64, error) { return repo.CountSearch("pager") })
	}

	owner := f.users[0].ID
	assertPagesWalkTotalOrder(t, "GetByOwnerID", expectedIDs(f.leads, leadID, func(l models.Lead) bool { return l.OwnerID == owner }, nil, false),
		func(o, l int) ([]uint, error) {
			rows, err := repo.GetByOwnerID(owner, o, l)
			return idsOf(rows, leadID), err
		}, func() (int64, error) { return repo.CountByOwnerID(owner) })

	assertPagesWalkTotalOrder(t, "GetByClassification", expectedIDs(f.leads, leadID, func(l models.Lead) bool { return l.Classification == models.LeadClassificationLead }, nil, false),
		func(o, l int) ([]uint, error) {
			rows, err := repo.GetByClassification(models.LeadClassificationLead, o, l)
			return idsOf(rows, leadID), err
		}, func() (int64, error) { return repo.CountByClassification(models.LeadClassificationLead) })
}

func customerID(c models.Customer) uint { return c.ID }

func customerKey(column string) func(models.Customer) string {
	if column == "company" {
		return func(c models.Customer) string { return c.Company }
	}
	return nil
}

func checkCustomerPagination(t *testing.T, db *gorm.DB, f paginationFixture) {
	repo := NewCustomerRepository(db)
	all := func(models.Customer) bool { return true }
	matches := func(c models.Customer) bool { return c.Notes == "pager" }

	assertPagesWalkTotalOrder(t, "List", expectedIDs(f.customers, customerID, all, nil, false),
		func(o, l int) ([]uint, error) {
			rows, err := repo.List(o, l)
			return idsOf(rows, customerID), err
		}, repo.Count)

	for _, s := range tiedSorts("company") {
		s := s
		assertPagesWalkTotalOrder(t, "ListSortedWithPreloads/"+s.String(), expectedIDs(f.customers, customerID, all, customerKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.ListSortedWithPreloads(o, l, s.by, s.order, "User")
				return idsOf(rows, customerID), err
			}, repo.Count)
		assertPagesWalkTotalOrder(t, "Search/"+s.String(), expectedIDs(f.customers, customerID, matches, customerKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.Search("pager", o, l, s.by, s.order)
				return idsOf(rows, customerID), err
			}, func() (int64, error) { return repo.CountSearch("pager") })
	}
}

func taskID(task models.Task) uint { return task.ID }

func taskKey(column string) func(models.Task) string {
	if column == "priority" {
		return func(task models.Task) string { return string(task.Priority) }
	}
	return nil
}

func checkTaskPagination(t *testing.T, db *gorm.DB, f paginationFixture) {
	repo := NewTaskRepository(db)
	all := func(models.Task) bool { return true }
	matches := func(task models.Task) bool { return task.Description == "pager" }

	assertPagesWalkTotalOrder(t, "List", expectedIDs(f.tasks, taskID, all, nil, false),
		func(o, l int) ([]uint, error) {
			rows, err := repo.List(o, l)
			return idsOf(rows, taskID), err
		}, repo.Count)

	assignee := f.users[0].ID
	assertPagesWalkTotalOrder(t, "GetByAssignedToID", expectedIDs(f.tasks, taskID, func(task models.Task) bool { return task.AssignedToID == assignee }, nil, false),
		func(o, l int) ([]uint, error) {
			rows, err := repo.GetByAssignedToID(assignee, o, l)
			return idsOf(rows, taskID), err
		}, func() (int64, error) { return repo.CountByAssignedToID(assignee) })

	labelled := func(task models.Task) bool { return f.taskLabel[task.ID] }
	labelledMine := func(task models.Task) bool { return f.taskLabel[task.ID] && task.AssignedToID == assignee }
	for _, s := range tiedSorts("priority") {
		s := s
		assertPagesWalkTotalOrder(t, "ListSortedWithPreloads/"+s.String(), expectedIDs(f.tasks, taskID, all, taskKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.ListSortedWithPreloads(o, l, s.by, s.order, "Labels", "Lead")
				return idsOf(rows, taskID), err
			}, repo.Count)
		assertPagesWalkTotalOrder(t, "Search/"+s.String(), expectedIDs(f.tasks, taskID, matches, taskKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.Search("pager", o, l, s.by, s.order, "Labels")
				return idsOf(rows, taskID), err
			}, func() (int64, error) { return repo.CountSearch("pager") })
		assertPagesWalkTotalOrder(t, "ListByLabel/"+s.String(), expectedIDs(f.tasks, taskID, labelled, taskKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.ListByLabel(f.label.ID, nil, o, l, s.by, s.order, "Labels", "Lead")
				return idsOf(rows, taskID), err
			}, func() (int64, error) { return repo.CountByLabel(f.label.ID, nil) })
		assertPagesWalkTotalOrder(t, "ListByLabelAssignee/"+s.String(), expectedIDs(f.tasks, taskID, labelledMine, taskKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.ListByLabel(f.label.ID, &assignee, o, l, s.by, s.order, "Labels")
				return idsOf(rows, taskID), err
			}, func() (int64, error) { return repo.CountByLabel(f.label.ID, &assignee) })
	}
}

func ticketID(ticket models.Ticket) uint { return ticket.ID }

func ticketKey(column string) func(models.Ticket) string {
	if column == "priority" {
		return func(ticket models.Ticket) string { return string(ticket.Priority) }
	}
	return nil
}

func checkTicketPagination(t *testing.T, db *gorm.DB, f paginationFixture) {
	repo := NewTicketRepository(db)
	all := func(models.Ticket) bool { return true }
	matches := func(ticket models.Ticket) bool { return ticket.Description == "pager" }

	assertPagesWalkTotalOrder(t, "List", expectedIDs(f.tickets, ticketID, all, nil, false),
		func(o, l int) ([]uint, error) {
			rows, err := repo.List(o, l)
			return idsOf(rows, ticketID), err
		}, repo.Count)

	customer := f.customers[0].ID
	assertPagesWalkTotalOrder(t, "GetByCustomerID", expectedIDs(f.tickets, ticketID, func(ticket models.Ticket) bool { return ticket.CustomerID == customer }, nil, false),
		func(o, l int) ([]uint, error) {
			rows, err := repo.GetByCustomerID(customer, o, l)
			return idsOf(rows, ticketID), err
		}, func() (int64, error) { return repo.CountByCustomerID(customer) })

	support := f.users[2].ID
	mine := func(ticket models.Ticket) bool { return ticket.AssignedToID != nil && *ticket.AssignedToID == support }
	assertPagesWalkTotalOrder(t, "GetByAssignedToID", expectedIDs(f.tickets, ticketID, mine, nil, false),
		func(o, l int) ([]uint, error) {
			rows, err := repo.GetByAssignedToID(support, o, l)
			return idsOf(rows, ticketID), err
		}, func() (int64, error) { return repo.CountByAssignedToID(support) })

	scope := TicketAssigneeScope{AssignedToID: support}
	narrowCustomer := f.customers[1].ID
	narrow := TicketAssigneeScope{AssignedToID: support, CustomerID: &narrowCustomer, Search: "pager"}
	mineNarrow := func(ticket models.Ticket) bool {
		return mine(ticket) && ticket.CustomerID == narrowCustomer && ticket.Description == "pager"
	}
	for _, s := range tiedSorts("priority") {
		s := s
		assertPagesWalkTotalOrder(t, "ListSortedWithPreloads/"+s.String(), expectedIDs(f.tickets, ticketID, all, ticketKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.ListSortedWithPreloads(o, l, s.by, s.order, "Customer", "AssignedTo")
				return idsOf(rows, ticketID), err
			}, repo.Count)
		assertPagesWalkTotalOrder(t, "Search/"+s.String(), expectedIDs(f.tickets, ticketID, matches, ticketKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.Search("pager", o, l, s.by, s.order)
				return idsOf(rows, ticketID), err
			}, func() (int64, error) { return repo.CountSearch("pager") })
		assertPagesWalkTotalOrder(t, "ListByAssigneeScope/"+s.String(), expectedIDs(f.tickets, ticketID, mine, ticketKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.ListByAssigneeScope(scope, o, l, s.by, s.order, "Customer")
				return idsOf(rows, ticketID), err
			}, func() (int64, error) { return repo.CountByAssigneeScope(scope) })
		assertPagesWalkTotalOrder(t, "ListByAssigneeScopeNarrow/"+s.String(), expectedIDs(f.tickets, ticketID, mineNarrow, ticketKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.ListByAssigneeScope(narrow, o, l, s.by, s.order)
				return idsOf(rows, ticketID), err
			}, func() (int64, error) { return repo.CountByAssigneeScope(narrow) })
	}
}

func userID(u models.User) uint { return u.ID }

func userKey(column string) func(models.User) string {
	if column == "role" {
		return func(u models.User) string { return string(u.Role) }
	}
	return nil
}

func checkUserPagination(t *testing.T, db *gorm.DB, f paginationFixture) {
	repo := NewUserRepository(db)
	all := func(models.User) bool { return true }
	matches := func(u models.User) bool { return u.LastName == "pager" }

	assertPagesWalkTotalOrder(t, "List", expectedIDs(f.users, userID, all, nil, false),
		func(o, l int) ([]uint, error) {
			rows, err := repo.List(o, l)
			return idsOf(rows, userID), err
		}, repo.Count)

	for _, s := range tiedSorts("role") {
		s := s
		assertPagesWalkTotalOrder(t, "ListSorted/"+s.String(), expectedIDs(f.users, userID, all, userKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.ListSorted(o, l, s.by, s.order)
				return idsOf(rows, userID), err
			}, repo.Count)
		assertPagesWalkTotalOrder(t, "Search/"+s.String(), expectedIDs(f.users, userID, matches, userKey(s.by), s.desc()),
			func(o, l int) ([]uint, error) {
				rows, err := repo.Search("pager", o, l, s.by, s.order)
				return idsOf(rows, userID), err
			}, func() (int64, error) { return repo.CountSearch("pager") })
	}
}

func companyID(c models.Company) uint { return c.ID }

func companyKey(column string) func(models.Company) string {
	if column == "industry" {
		return func(c models.Company) string { return c.Industry }
	}
	return nil
}

func checkCompanyPagination(t *testing.T, db *gorm.DB, f paginationFixture) {
	repo := NewCompanyRepository(db)
	all := func(models.Company) bool { return true }
	berlin := func(c models.Company) bool { return c.City == "berlin" }

	for _, s := range tiedSorts("industry") {
		s := s
		desc := s.desc()
		if s.by == "" {
			desc = true // the company list defaults to newest first
		}
		for _, search := range []string{"", "berlin"} {
			search := search
			keep := all
			name := "List/" + s.String()
			if search != "" {
				keep = berlin
				name = "ListSearch/" + s.String()
			}
			var total int64
			assertPagesWalkTotalOrder(t, name, expectedIDs(f.companies, companyID, keep, companyKey(s.by), desc),
				func(o, l int) ([]uint, error) {
					rows, n, err := repo.List(o, l, search, s.by, s.order)
					total = n
					return idsOf(rows, companyID), err
				}, func() (int64, error) { return total, nil })
		}
	}

	linked := f.companies[0].ID
	var total int64
	assertPagesWalkTotalOrder(t, "ListCustomers",
		expectedIDs(f.customers, customerID, func(c models.Customer) bool { return c.CompanyID != nil && *c.CompanyID == linked }, nil, true),
		func(o, l int) ([]uint, error) {
			rows, n, err := repo.ListCustomers(linked, o, l)
			total = n
			return idsOf(rows, customerID), err
		}, func() (int64, error) { return total, nil })

	onCompany := func(l models.Lead) bool { return l.CompanyID != nil && *l.CompanyID == linked }
	assertPagesWalkTotalOrder(t, "ListLeads",
		expectedIDs(f.leads, leadID, onCompany, nil, true),
		func(o, l int) ([]uint, error) {
			rows, n, err := repo.ListLeads(linked, nil, o, l)
			total = n
			return idsOf(rows, leadID), err
		}, func() (int64, error) { return total, nil })

	owner := f.users[0].ID
	assertPagesWalkTotalOrder(t, "ListLeadsOwner",
		expectedIDs(f.leads, leadID, func(l models.Lead) bool { return onCompany(l) && l.OwnerID == owner }, nil, true),
		func(o, l int) ([]uint, error) {
			rows, n, err := repo.ListLeads(linked, &owner, o, l)
			total = n
			return idsOf(rows, leadID), err
		}, func() (int64, error) { return total, nil })
}
