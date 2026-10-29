package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The identity erasure graph (#108).
//
// A person can be stored three times over: as a lead, as the customer that
// lead was converted into (leads.customer_id), and as the customer-role login
// of that customer (customers.user_id). Deleting any one of them must erase all
// of them, in one unit, and must never follow a link into a staff account.
//
// These tests run on the application's real schema, created by the same
// migration cmd/main.go runs, with foreign keys enforced: on SQLite through the
// foreign_keys pragma, on MySQL/MariaDB through the constraints AutoMigrate
// creates. The MySQL root uses a uniquely named disposable schema on the
// TEST_MYSQL_DSN server; it skips without the variable and fails when the
// variable names a server that cannot be reached.

func TestIdentityErasureSQLite(t *testing.T) {
	runIdentityErasure(t, false)
}

func TestIdentityErasureMySQL(t *testing.T) {
	if os.Getenv("TEST_MYSQL_DSN") == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	runIdentityErasure(t, true)
}

// openIdentityErasureDB returns a fresh database with the full production
// schema and foreign keys on.
func openIdentityErasureDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{Logger: logger.Discard}
	var db *gorm.DB
	if !useMySQL {
		path := filepath.Join(t.TempDir(), "identity.db")
		var err error
		db, err = gorm.Open(sqlite.Open("file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		// One connection, as the production SQLite pool has.
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	} else {
		dsnCfg, err := mysqldriver.ParseDSN(os.Getenv("TEST_MYSQL_DSN"))
		require.NoError(t, err)
		dsnCfg.DBName = ""
		dsnCfg.ParseTime = true
		dsnCfg.Timeout, dsnCfg.ReadTimeout, dsnCfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
		server, err := sql.Open("mysql", dsnCfg.FormatDSN())
		require.NoError(t, err)
		name := fmt.Sprintf("gocrm_identity_erasure_%d", time.Now().UnixNano())
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
		sqlDB.SetMaxOpenConns(4)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	// models.MigrateDatabase works off the package-level handle; it is swapped
	// for the call and restored straight away. No test in this package runs in
	// parallel.
	previous := models.DB
	models.DB = db
	err := models.MigrateDatabase()
	models.DB = previous
	require.NoError(t, err)

	if useMySQL {
		// The fixture is only production-equivalent if the constraints exist.
		var constraints int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA = DATABASE()").Scan(&constraints).Error)
		require.Positive(t, constraints, "the MySQL fixture must carry the schema's foreign keys")
	}
	return db
}

// identityGraph is one person stored as a login, two customers (one of them
// soft-deleted before erasure existed) and three converted leads (one
// soft-deleted), plus everything that duplicates them: a form submission and
// its confirmation token, credentials, notifications, ticket comments (one
// soft-deleted) and an active automation run. Staff accounts own, manage and
// are assigned to all of it and must survive untouched.
type identityGraph struct {
	tag                        string
	admin, sales, support      *models.User
	login                      *models.User
	customerLive, customerGone *models.Customer
	leadLive, leadGone, leadB  *models.Lead
	ticket                     *models.Ticket
	commentLive, commentGone   *models.TicketComment
	staffComment               *models.TicketComment
	submission                 *models.FormSubmission
	run                        *models.AutomationRun
	task                       *models.Task
	deal                       *models.Deal
	needles                    []string
}

var identityTagSeq struct {
	sync.Mutex
	n int
}

func nextIdentityTag() string {
	identityTagSeq.Lock()
	defer identityTagSeq.Unlock()
	identityTagSeq.n++
	return fmt.Sprintf("%d%03d", time.Now().UnixNano()%1_000_000, identityTagSeq.n)
}

func seedIdentityStaff(t *testing.T, db *gorm.DB, tag string, role models.UserRole) *models.User {
	t.Helper()
	user := &models.User{
		Email: fmt.Sprintf("%s-%s@staff.test", role, tag), Password: "$2a$10$staffhash", FirstName: "Staff",
		LastName: string(role), Role: role, IsActive: true,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func seedIdentityGraph(t *testing.T, db *gorm.DB) *identityGraph {
	t.Helper()
	tag := nextIdentityTag()
	g := &identityGraph{tag: tag}
	g.admin = seedIdentityStaff(t, db, tag, models.RoleAdmin)
	g.sales = seedIdentityStaff(t, db, tag, models.RoleSales)
	g.support = seedIdentityStaff(t, db, tag, models.RoleSupport)

	first, last := "Ottilie"+tag, "Brennagh"+tag
	phone := "+44-7700-" + tag
	company := "Wrenworks" + tag
	address := "12 Wren Lane " + tag
	notes := "Prefers calls after six " + tag
	loginEmail := "ottilie.login." + tag + "@person.test"
	liveEmail := "ottilie.customer." + tag + "@person.test"
	goneEmail := "ottilie.old." + tag + "@person.test"
	leadEmail := "ottilie.lead." + tag + "@person.test"
	leadGoneEmail := "ottilie.lead.old." + tag + "@person.test"
	leadBEmail := "ottilie.lead.b." + tag + "@person.test"
	externalID := "crm-ext-" + tag
	website := "https://ottilie" + tag + ".example"
	ip := "198.51.100." + tag[len(tag)-2:]
	userAgent := "OttilieBrowser/" + tag
	referrer := "https://landing.example/" + tag
	g.needles = []string{first, last, phone, company, address, notes, loginEmail, liveEmail, goneEmail,
		leadEmail, leadGoneEmail, leadBEmail, externalID, website, userAgent, referrer}

	now := time.Now()
	g.login = &models.User{Email: loginEmail, Password: "$2a$10$personhash" + tag, FirstName: first, LastName: last,
		Role: models.RoleCustomer, IsActive: true, LastLoginAt: &now, FailedLoginAttempts: 2}
	require.NoError(t, db.Create(g.login).Error)

	newCustomer := func(email string) *models.Customer {
		c := &models.Customer{FirstName: first, LastName: last, Email: email, Phone: phone, Company: company,
			Website: website, Position: "Buyer", Address: address, City: "Wrenham", State: "Wrenshire",
			Country: "UK", PostalCode: "WR1 " + tag[:3], Notes: notes, UserID: &g.login.ID, AssignedToID: &g.sales.ID}
		require.NoError(t, db.Create(c).Error)
		return c
	}
	g.customerLive = newCustomer(liveEmail)
	g.customerGone = newCustomer(goneEmail)

	newLead := func(email string, customerID uint) *models.Lead {
		l := &models.Lead{FirstName: first, LastName: last, Email: email, Phone: phone, Company: company,
			Position: "Buyer", Notes: notes, ExternalID: externalID, Source: "website",
			Status: models.LeadStatusConverted, OwnerID: g.sales.ID, CustomerID: &customerID}
		require.NoError(t, db.Create(l).Error)
		return l
	}
	g.leadLive = newLead(leadEmail, g.customerLive.ID)
	g.leadGone = newLead(leadGoneEmail, g.customerLive.ID)
	g.leadB = newLead(leadBEmail, g.customerGone.ID)

	g.ticket = &models.Ticket{Title: "Printer jam", Description: "Tray two", CustomerID: g.customerLive.ID, AssignedToID: &g.support.ID}
	require.NoError(t, db.Create(g.ticket).Error)
	g.commentLive = &models.TicketComment{TicketID: g.ticket.ID, AuthorUserID: g.login.ID, Body: "Call me on " + phone}
	g.commentGone = &models.TicketComment{TicketID: g.ticket.ID, AuthorUserID: g.login.ID, Body: "Signed, " + first + " " + last}
	g.staffComment = &models.TicketComment{TicketID: g.ticket.ID, AuthorUserID: g.support.ID, Body: "Replaced the roller", IsInternal: true}
	for _, c := range []*models.TicketComment{g.commentLive, g.commentGone, g.staffComment} {
		require.NoError(t, db.Create(c).Error)
	}

	form := &models.Form{Name: "Contact " + tag, PublicID: "identity-" + tag, Status: models.FormStatusPublished,
		Fields:     []models.FormFieldDef{{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true}},
		CreateLead: true, DefaultOwnerID: g.sales.ID, CreatedByID: g.admin.ID}
	require.NoError(t, db.Create(form).Error)
	g.submission = &models.FormSubmission{FormID: form.ID, Data: map[string]string{"email": leadEmail, "name": first + " " + last},
		Email: leadEmail, Status: models.FormSubmissionConfirmed, LeadID: &g.leadLive.ID, IPAddress: ip, UserAgent: userAgent, Referrer: referrer}
	require.NoError(t, db.Create(g.submission).Error)
	require.NoError(t, db.Create(&models.FormConfirmationToken{SubmissionID: g.submission.ID, TokenHash: "ct" + strings.Repeat(tag, 4), ExpiresAt: now.Add(time.Hour)}).Error)

	expires := now.Add(time.Hour)
	require.NoError(t, db.Create(&models.APIKey{Name: "cli", KeyHash: "kh" + tag, Prefix: "gcrm_ab", UserID: g.login.ID}).Error)
	require.NoError(t, db.Create(&models.RefreshToken{UserID: g.login.ID, TokenHash: "rt" + tag, ExpiresAt: expires}).Error)
	require.NoError(t, db.Create(&models.PasswordResetToken{UserID: g.login.ID, TokenHash: "pr" + tag, ExpiresAt: expires}).Error)

	require.NoError(t, db.Create(&models.Notification{UserID: g.login.ID, Title: "Your ticket was updated", EntityType: models.AutomationEntityLead, EntityID: 0}).Error)
	require.NoError(t, db.Create(&models.Notification{UserID: g.sales.ID, Title: "Call " + first + " " + last, EntityType: models.AutomationEntityLead, EntityID: g.leadLive.ID}).Error)
	rule := &models.AutomationRule{Name: "Follow up " + tag, EntityType: models.AutomationEntityLead, TriggerType: "lead_created", Enabled: true, Version: 1}
	require.NoError(t, NewAutomationRepository(db).CreateRule(rule))
	g.run = &models.AutomationRun{RuleID: rule.ID, RuleVersion: 1, EntityType: models.AutomationEntityLead, EntityID: g.leadLive.ID,
		DedupeKey: "identity:" + tag, Status: models.AutomationRunPending, NextPosition: 1}
	inserted, err := NewAutomationRepository(db).CreateRunIgnoringDuplicate(g.run)
	require.NoError(t, err)
	require.True(t, inserted)

	g.task = &models.Task{Title: "Send brochure", AssignedToID: g.sales.ID, LeadID: &g.leadLive.ID, CustomerID: &g.customerLive.ID}
	require.NoError(t, db.Create(g.task).Error)
	g.deal = &models.Deal{Title: "Printer fleet", Currency: "EUR", Probability: 10, OwnerID: g.sales.ID,
		CustomerID: &g.customerLive.ID, LeadID: &g.leadLive.ID}
	require.NoError(t, db.Create(g.deal).Error)

	// The rows soft-deleted before deletion became an erasure: hidden from
	// every ordinary query, still holding the person.
	require.NoError(t, db.Delete(&models.Customer{}, g.customerGone.ID).Error)
	require.NoError(t, db.Delete(&models.Lead{}, g.leadGone.ID).Error)
	require.NoError(t, db.Delete(&models.TicketComment{}, g.commentGone.ID).Error)

	// The sweep must see every copy before the erasure, or its silence after
	// it proves nothing.
	holding := identityTablesHolding(t, db, g.needles)
	for _, table := range []string{"users", "customers", "leads", "ticket_comments", "form_submissions", "notifications"} {
		require.Contains(t, holding, table, "the seeded person must be visible in %s before the erasure", table)
	}
	return g
}

// identitySweep reads every row of every table, soft-deleted rows included,
// and returns "table.column" for each place a needle still appears.
func identitySweep(t *testing.T, db *gorm.DB, needles []string) []string {
	t.Helper()
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	var hits []string
	for _, table := range tables {
		if strings.HasPrefix(table, "sqlite_") {
			continue
		}
		var rows []map[string]interface{}
		require.NoError(t, db.Table(table).Find(&rows).Error, "reading %s", table)
		for _, row := range rows {
			for column, value := range row {
				if value == nil {
					continue
				}
				text := fmt.Sprintf("%v", value)
				if b, ok := value.([]byte); ok {
					text = string(b)
				}
				for _, needle := range needles {
					if strings.Contains(strings.ToLower(text), strings.ToLower(needle)) {
						hits = append(hits, fmt.Sprintf("%s.%s contains %q", table, column, needle))
					}
				}
			}
		}
	}
	sort.Strings(hits)
	return hits
}

func identityTablesHolding(t *testing.T, db *gorm.DB, needles []string) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, hit := range identitySweep(t, db, needles) {
		seen[strings.SplitN(hit, ".", 2)[0]] = true
	}
	var tables []string
	for table := range seen {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}

// snapshotIdentityDB renders every row of every table, except the excluded
// ones, in a stable textual form, so "nothing changed" is one comparison.
func snapshotIdentityDB(t *testing.T, db *gorm.DB, exclude ...string) map[string][]string {
	t.Helper()
	skip := map[string]bool{}
	for _, table := range exclude {
		skip[table] = true
	}
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	snapshot := map[string][]string{}
	for _, table := range tables {
		if strings.HasPrefix(table, "sqlite_") || skip[table] {
			continue
		}
		var rows []map[string]interface{}
		require.NoError(t, db.Table(table).Find(&rows).Error, "reading %s", table)
		rendered := make([]string, 0, len(rows))
		for _, row := range rows {
			columns := make([]string, 0, len(row))
			for column := range row {
				columns = append(columns, column)
			}
			sort.Strings(columns)
			var b strings.Builder
			for _, column := range columns {
				value := row[column]
				if raw, ok := value.([]byte); ok {
					value = string(raw)
				}
				fmt.Fprintf(&b, "%s=%v;", column, value)
			}
			rendered = append(rendered, b.String())
		}
		sort.Strings(rendered)
		snapshot[table] = rendered
	}
	return snapshot
}

func countRows(t *testing.T, db *gorm.DB, model interface{}, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Unscoped().Model(model).Where(where, args...).Count(&n).Error)
	return n
}

// assertGraphErased checks every seeded scrub target of the person, then the
// whole-schema sweep, then that staff and business links are intact.
func assertGraphErased(t *testing.T, db *gorm.DB, g *identityGraph) {
	t.Helper()

	var login models.User
	require.NoError(t, db.Unscoped().First(&login, g.login.ID).Error)
	assert.True(t, login.DeletedAt.Valid, "the login is soft-deleted")
	assert.True(t, strings.HasSuffix(login.Email, "@"+anonymizedEmailDomain), "the login's address is a placeholder")
	assert.Empty(t, login.FirstName)
	assert.Empty(t, login.LastName)
	assert.Equal(t, unusablePasswordHash, login.Password)
	assert.False(t, login.IsActive)
	assert.Equal(t, models.RoleCustomer, login.Role, "the role is not personal data and stays")

	for _, id := range []uint{g.customerLive.ID, g.customerGone.ID} {
		var c models.Customer
		require.NoError(t, db.Unscoped().First(&c, id).Error)
		assert.True(t, c.DeletedAt.Valid, "customer %d is soft-deleted", id)
		assert.True(t, strings.HasSuffix(c.Email, "@"+anonymizedEmailDomain), "customer %d", id)
		assert.Empty(t, c.FirstName, "customer %d", id)
		assert.Empty(t, c.Notes, "customer %d", id)
		require.NotNil(t, c.UserID, "the identity link is an id, not personal data, and stays")
		assert.Equal(t, g.login.ID, *c.UserID)
		require.NotNil(t, c.AssignedToID)
		assert.Equal(t, g.sales.ID, *c.AssignedToID, "the account manager link stays")
	}
	for _, id := range []uint{g.leadLive.ID, g.leadGone.ID, g.leadB.ID} {
		var l models.Lead
		require.NoError(t, db.Unscoped().First(&l, id).Error)
		assert.True(t, l.DeletedAt.Valid, "lead %d is soft-deleted", id)
		assert.True(t, strings.HasSuffix(l.Email, "@"+anonymizedEmailDomain), "lead %d", id)
		assert.Empty(t, l.FirstName, "lead %d", id)
		assert.Empty(t, l.ExternalID, "lead %d", id)
		assert.Equal(t, g.sales.ID, l.OwnerID, "the lead owner stays")
	}

	for _, model := range []interface{}{&models.APIKey{}, &models.RefreshToken{}, &models.PasswordResetToken{}, &models.Notification{}} {
		assert.Zero(t, countRows(t, db, model, "user_id = ?", g.login.ID), "%T of the login must be gone", model)
	}
	assert.Zero(t, countRows(t, db, &models.Notification{}, "entity_type = ? AND entity_id = ?", models.AutomationEntityLead, g.leadLive.ID))
	assert.Zero(t, countRows(t, db, &models.FormConfirmationToken{}, "submission_id = ?", g.submission.ID))
	var submission models.FormSubmission
	require.NoError(t, db.Unscoped().First(&submission, g.submission.ID).Error)
	assert.Empty(t, submission.IPAddress)
	assert.Empty(t, submission.UserAgent)
	assert.Empty(t, submission.Referrer)

	var run models.AutomationRun
	require.NoError(t, db.First(&run, g.run.ID).Error)
	assert.Equal(t, models.AutomationRunCancelled, run.Status)

	assertCommentRedacted(t, db, g.commentLive.ID, false)
	assertCommentRedacted(t, db, g.commentGone.ID, true)
	var staffComment models.TicketComment
	require.NoError(t, db.First(&staffComment, g.staffComment.ID).Error)
	assert.Equal(t, "Replaced the roller", staffComment.Body, "a staff comment is not the person's")

	assert.Empty(t, identitySweep(t, db, g.needles), "personal data survived the erasure")
	assertStaffIntact(t, db, g)
}

func assertCommentRedacted(t *testing.T, db *gorm.DB, id uint, wasDeleted bool) {
	t.Helper()
	var c models.TicketComment
	require.NoError(t, db.Unscoped().First(&c, id).Error)
	assert.Equal(t, "[deleted]", c.Body, "comment %d is redacted", id)
	assert.Equal(t, wasDeleted, c.DeletedAt.Valid, "comment %d keeps its own deletion state", id)
}

func assertStaffIntact(t *testing.T, db *gorm.DB, g *identityGraph) {
	t.Helper()
	for _, staff := range []*models.User{g.admin, g.sales, g.support} {
		var u models.User
		require.NoError(t, db.First(&u, staff.ID).Error, "staff %s is still live", staff.Role)
		assert.Equal(t, staff.Email, u.Email)
		assert.Equal(t, "Staff", u.FirstName)
		assert.True(t, u.IsActive)
	}
	var ticket models.Ticket
	require.NoError(t, db.First(&ticket, g.ticket.ID).Error, "the ticket is a business record")
	require.NotNil(t, ticket.AssignedToID)
	assert.Equal(t, g.support.ID, *ticket.AssignedToID)
	var task models.Task
	require.NoError(t, db.First(&task, g.task.ID).Error)
	assert.Equal(t, g.sales.ID, task.AssignedToID)
	var deal models.Deal
	require.NoError(t, db.First(&deal, g.deal.ID).Error)
	assert.Equal(t, g.sales.ID, deal.OwnerID)
	require.NotNil(t, deal.CustomerID)
	assert.Equal(t, g.customerLive.ID, *deal.CustomerID)
}

// identityRoot erases the person starting from one node, through the plain
// repository constructor or through its WithTx variant inside a transaction.
type identityRoot struct {
	name  string
	erase func(db *gorm.DB, g *identityGraph) error
}

func identityRoots() []identityRoot {
	return []identityRoot{
		{"user", func(db *gorm.DB, g *identityGraph) error { return NewUserRepository(db).Delete(g.login.ID) }},
		{"customer", func(db *gorm.DB, g *identityGraph) error { return NewCustomerRepository(db).Delete(g.customerLive.ID) }},
		{"lead", func(db *gorm.DB, g *identityGraph) error { return NewLeadRepository(db).Delete(g.leadLive.ID) }},
		{"user_with_tx", func(db *gorm.DB, g *identityGraph) error {
			return db.Transaction(func(tx *gorm.DB) error { return NewUserRepository(db).WithTx(tx).Delete(g.login.ID) })
		}},
		{"customer_with_tx", func(db *gorm.DB, g *identityGraph) error {
			return db.Transaction(func(tx *gorm.DB) error { return NewCustomerRepository(db).WithTx(tx).Delete(g.customerLive.ID) })
		}},
		{"lead_with_tx", func(db *gorm.DB, g *identityGraph) error {
			return db.Transaction(func(tx *gorm.DB) error { return NewLeadRepository(db).WithTx(tx).Delete(g.leadLive.ID) })
		}},
	}
}

// failSoftDeletesOn injects a sentinel into every DELETE statement against
// table and counts the statements it intercepted.
func failSoftDeletesOn(t *testing.T, db *gorm.DB, table string) (sentinel error, calls func() int) {
	t.Helper()
	sentinel = fmt.Errorf("injected failure soft-deleting %s", table)
	var mu sync.Mutex
	count := 0
	name := "identity_erasure_test:fail_" + table
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != table {
			return
		}
		mu.Lock()
		count++
		mu.Unlock()
		_ = tx.AddError(sentinel)
	}))
	t.Cleanup(func() { _ = db.Callback().Delete().Remove(name) })
	return sentinel, func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

func runIdentityErasure(t *testing.T, useMySQL bool) {
	for _, root := range identityRoots() {
		root := root
		t.Run(root.name+"_root_erases_the_whole_graph", func(t *testing.T) {
			db := openIdentityErasureDB(t, useMySQL)
			g := seedIdentityGraph(t, db)
			other := seedIdentityGraph(t, db)
			require.NoError(t, root.erase(db, g))
			assertGraphErased(t, db, g)

			// The other person, structured identically and sharing nothing,
			// is still there in full.
			holding := identityTablesHolding(t, db, other.needles)
			for _, table := range []string{"users", "customers", "leads", "ticket_comments", "form_submissions"} {
				assert.Contains(t, holding, table, "an unlinked person lost data in %s", table)
			}
		})
	}

	t.Run("soft_deleted_comments_are_redacted_and_stay_deleted", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		g := seedIdentityGraph(t, db)
		require.NoError(t, NewUserRepository(db).Delete(g.login.ID))
		assertCommentRedacted(t, db, g.commentLive.ID, false)
		assertCommentRedacted(t, db, g.commentGone.ID, true)
	})

	t.Run("staff_login_link_is_never_followed", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		g := seedIdentityGraph(t, db)
		for _, staff := range []*models.User{g.admin, g.sales, g.support} {
			require.NoError(t, db.Create(&models.APIKey{Name: "staff", KeyHash: fmt.Sprintf("staff-%s-%d", g.tag, staff.ID), Prefix: "gcrm_st", UserID: staff.ID}).Error)
			first := &models.Customer{FirstName: "Linked", LastName: string(staff.Role), Email: fmt.Sprintf("linked-a-%d-%s@person.test", staff.ID, g.tag), UserID: &staff.ID}
			second := &models.Customer{FirstName: "Linked", LastName: string(staff.Role), Email: fmt.Sprintf("linked-b-%d-%s@person.test", staff.ID, g.tag), UserID: &staff.ID}
			require.NoError(t, db.Create(first).Error)
			require.NoError(t, db.Create(second).Error)

			// Erasing a customer whose login is a staff account leaves the
			// staff account, and its credentials, alone.
			require.NoError(t, NewCustomerRepository(db).Delete(first.ID))
			var u models.User
			require.NoError(t, db.First(&u, staff.ID).Error, "%s survives a customer erasure", staff.Role)
			assert.Equal(t, "Staff", u.FirstName)
			assert.EqualValues(t, 1, countRows(t, db, &models.APIKey{}, "user_id = ?", staff.ID))

			// Erasing the staff account itself leaves the customers that
			// point at it alone.
			require.NoError(t, NewUserRepository(db).Delete(staff.ID))
			var c models.Customer
			require.NoError(t, db.First(&c, second.ID).Error, "a customer linked to an erased %s stays live", staff.Role)
			assert.Equal(t, "Linked", c.FirstName)
		}
	})

	t.Run("repeated_erasure_is_harmless", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		g := seedIdentityGraph(t, db)
		other := seedIdentityGraph(t, db)
		otherBefore := identityTablesHolding(t, db, other.needles)
		for _, root := range identityRoots() {
			require.NoError(t, root.erase(db, g), "re-erasing from %s", root.name)
		}
		assertGraphErased(t, db, g)
		assert.Equal(t, otherBefore, identityTablesHolding(t, db, other.needles))
	})

	t.Run("fault_rolls_back_the_whole_graph", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		for _, root := range identityRoots() {
			for _, table := range []string{"users", "customers", "leads"} {
				g := seedIdentityGraph(t, db)
				before := snapshotIdentityDB(t, db)
				sentinel, calls := failSoftDeletesOn(t, db, table)
				err := root.erase(db, g)
				require.NoError(t, db.Callback().Delete().Remove("identity_erasure_test:fail_"+table))
				require.ErrorIs(t, err, sentinel, "root %s, failing %s", root.name, table)
				assert.GreaterOrEqual(t, calls(), 1, "root %s reached %s", root.name, table)
				assert.Equal(t, before, snapshotIdentityDB(t, db), "root %s, failing %s: nothing may change", root.name, table)
			}
		}
	})

	t.Run("caller_transaction_commit_after_a_failed_erasure_keeps_only_the_callers_work", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		g := seedIdentityGraph(t, db)
		before := snapshotIdentityDB(t, db, "labels")
		sentinel, calls := failSoftDeletesOn(t, db, "customers")

		tx := db.Begin()
		require.NoError(t, tx.Error)
		label := &models.Label{Name: "outer-" + g.tag, Color: "#112233"}
		require.NoError(t, tx.Create(label).Error)
		err := NewUserRepository(db).WithTx(tx).Delete(g.login.ID)
		require.ErrorIs(t, err, sentinel)
		require.GreaterOrEqual(t, calls(), 1)
		// The caller handles the error and commits its own work anyway.
		require.NoError(t, tx.Commit().Error)
		require.NoError(t, db.Callback().Delete().Remove("identity_erasure_test:fail_customers"))

		assert.EqualValues(t, 1, countRows(t, db, &models.Label{}, "name = ?", label.Name), "the caller's own write is committed")
		assert.Equal(t, before, snapshotIdentityDB(t, db, "labels"), "no part of the failed erasure may be committed")
	})

	t.Run("each_node_is_scrubbed_exactly_once", func(t *testing.T) {
		for _, root := range identityRoots()[:3] {
			db := openIdentityErasureDB(t, useMySQL)
			g := seedIdentityGraph(t, db)
			// The ids overlap across tables on a fresh schema (user 4, customer 1,
			// lead 1 ...), so the count is kept per table and id.
			var mu sync.Mutex
			counts := map[string]int{}
			erasureScrubObserver = func(table string, id uint) {
				mu.Lock()
				defer mu.Unlock()
				counts[fmt.Sprintf("%s:%d", table, id)]++
			}
			err := root.erase(db, g)
			erasureScrubObserver = nil
			require.NoError(t, err)
			want := map[string]int{
				fmt.Sprintf("users:%d", g.login.ID):            1,
				fmt.Sprintf("customers:%d", g.customerLive.ID): 1,
				fmt.Sprintf("customers:%d", g.customerGone.ID): 1,
				fmt.Sprintf("leads:%d", g.leadLive.ID):         1,
				fmt.Sprintf("leads:%d", g.leadGone.ID):         1,
				fmt.Sprintf("leads:%d", g.leadB.ID):            1,
			}
			assert.Equal(t, want, counts, "root %s", root.name)
		}
	})

	t.Run("unconverted_only_refuses_a_converted_lead_and_changes_nothing", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		g := seedIdentityGraph(t, db)
		before := snapshotIdentityDB(t, db)
		for _, id := range []uint{g.leadLive.ID, g.leadGone.ID, g.leadB.ID} {
			err := NewLeadRepository(db).DeleteUnconverted(id)
			require.ErrorIs(t, err, ErrConvertedLeadNeedsAdmin, "lead %d", id)
			require.ErrorIs(t, err, apperrors.ErrForbidden)
			err = db.Transaction(func(tx *gorm.DB) error { return NewLeadRepository(db).WithTx(tx).DeleteUnconverted(id) })
			require.ErrorIs(t, err, ErrConvertedLeadNeedsAdmin, "lead %d through WithTx", id)
		}
		assert.Equal(t, before, snapshotIdentityDB(t, db), "a refused erasure changes nothing")
	})

	t.Run("unconverted_only_erases_an_unconverted_lead", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		g := seedIdentityGraph(t, db)
		lead := &models.Lead{FirstName: "Unconverted" + g.tag, LastName: "Prospect" + g.tag, Email: "prospect." + g.tag + "@person.test",
			Phone: "+1-555-" + g.tag, Notes: "met at the fair " + g.tag, OwnerID: g.sales.ID, Status: models.LeadStatusNew}
		require.NoError(t, db.Create(lead).Error)
		require.NoError(t, NewLeadRepository(db).DeleteUnconverted(lead.ID))
		assert.Empty(t, identitySweep(t, db, []string{lead.FirstName, lead.LastName, lead.Email, lead.Phone, lead.Notes}))
		var erased models.Lead
		require.NoError(t, db.Unscoped().First(&erased, lead.ID).Error)
		assert.True(t, erased.DeletedAt.Valid)
		// The converted person next to it is untouched.
		assert.Contains(t, identityTablesHolding(t, db, g.needles), "customers")
	})

	t.Run("failed_savepoint_rollback_aborts_the_caller_transaction", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		g := seedIdentityGraph(t, db)
		before := snapshotIdentityDB(t, db)
		sentinel, _ := failSoftDeletesOn(t, db, "customers")
		rollbackFault := errors.New("injected ROLLBACK TO SAVEPOINT failure")
		var rollbackCalls int
		const name = "identity_erasure_test:fail_rollback_to"
		require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register(name, func(tx *gorm.DB) {
			if strings.HasPrefix(tx.Statement.SQL.String(), "ROLLBACK TO SAVEPOINT gocrm_erasure_") {
				rollbackCalls++
				_ = tx.AddError(rollbackFault)
			}
		}))
		t.Cleanup(func() { _ = db.Callback().Raw().Remove(name) })

		tx := db.Begin()
		require.NoError(t, tx.Error)
		require.NoError(t, tx.Create(&models.Label{Name: "outer-" + g.tag, Color: "#112233"}).Error)
		err := NewUserRepository(db).WithTx(tx).Delete(g.login.ID)
		require.ErrorIs(t, err, sentinel)
		require.ErrorIs(t, err, rollbackFault)
		require.ErrorIs(t, err, utils.ErrRollbackFailed)
		require.ErrorIs(t, err, utils.ErrTransactionPoisoned)
		require.Equal(t, 1, rollbackCalls)
		assert.Error(t, tx.Commit().Error, "the caller can no longer commit anything")
		require.NoError(t, db.Callback().Raw().Remove(name))
		require.NoError(t, db.Callback().Delete().Remove("identity_erasure_test:fail_customers"))

		assert.Equal(t, before, snapshotIdentityDB(t, db), "neither the erasure nor the caller's write survived")
	})
}
