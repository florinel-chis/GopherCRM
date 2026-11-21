package repository

// Repository roots of #385: one lock order for the user-, customer- and
// lead-rooted erasure of a person (automation runs cancelled, then leads,
// customers, users, refresh_tokens, password_reset_tokens, api_keys, ids
// ascending and by primary key), the re-check of the graph under those locks
// (ErrErasureGraphChanged and the bounded restart from a new discovery), the
// bounded replay of a transaction that lost a deadlock, and two erasures of
// one person entered from different roots, which used to end in error 1213
// for one of them.
//
// The roots use the production schema with its foreign keys
// (migrateIdentityErasureDB). Interleavings are forced through the instance-local
// ErasureHooks and observed through information_schema.PROCESSLIST
// (awaitLockOrderWait); nothing sleeps. The MySQL roots use a disposable
// TEST_MYSQL_DSN schema, skip without it at the root only and fail when it is
// unreachable.

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// erasureCrossRootPairs is the number of free-running pairs per mode of
// TestErasureCrossRootMySQL. On the code before #385 every pair ended in
// error 1213 for one caller (60 of 60 measured on MySQL 8 and MariaDB 10.11).
const erasureCrossRootPairs = 20

// erasureStatement is one captured statement: its SQL (placeholders kept), its
// bind values and whether it ran on a transaction or on the root handle.
type erasureStatement struct {
	sql  string
	vars []interface{}
	inTx bool
}

// captureErasureTrace is captureErasureStatements with nothing dropped: the
// schema probes are kept, every statement carries its bind values, and a
// statement issued on a transaction is told from one issued on the root
// handle by its connection pool. The callbacks are registered on db for the
// duration of call only.
func captureErasureTrace(t *testing.T, db *gorm.DB, call func() error) ([]erasureStatement, error) {
	t.Helper()
	var mu sync.Mutex
	var trace []erasureStatement
	record := func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		sql := strings.TrimSpace(tx.Statement.SQL.String())
		if sql == "" {
			return
		}
		_, inTx := tx.Statement.ConnPool.(gorm.TxCommitter)
		mu.Lock()
		trace = append(trace, erasureStatement{sql: sql, vars: append([]interface{}(nil), tx.Statement.Vars...), inTx: inTx})
		mu.Unlock()
	}

	name := fmt.Sprintf("erasure_lock_order:trace_%d", leadErasureCaptureSeq.Add(1))
	cb := db.Callback()
	require.NoError(t, cb.Query().After("gorm:query").Register(name, record))
	require.NoError(t, cb.Row().After("gorm:row").Register(name, record))
	require.NoError(t, cb.Raw().After("gorm:raw").Register(name, record))
	require.NoError(t, cb.Create().After("gorm:create").Register(name, record))
	require.NoError(t, cb.Update().After("gorm:update").Register(name, record))
	require.NoError(t, cb.Delete().After("gorm:delete").Register(name, record))

	err := call()

	require.NoError(t, cb.Query().Remove(name))
	require.NoError(t, cb.Row().Remove(name))
	require.NoError(t, cb.Raw().Remove(name))
	require.NoError(t, cb.Create().Remove(name))
	require.NoError(t, cb.Update().Remove(name))
	require.NoError(t, cb.Delete().Remove(name))

	mu.Lock()
	defer mu.Unlock()
	return append([]erasureStatement(nil), trace...), err
}

// The locking reads of the graph, as GORM writes them on both dialects. The
// lead and customer reads are erasureLeadLockRead and erasureCustomerRead.
var (
	erasureUserLockRead     = regexp.MustCompile("^SELECT `id`,`role` FROM `users` WHERE `users`.`id` = \\?")
	erasureRefreshLockRead  = regexp.MustCompile("^SELECT `id` FROM `refresh_tokens` WHERE id IN ")
	erasureResetLockRead    = regexp.MustCompile("^SELECT `id` FROM `password_reset_tokens` WHERE id IN ")
	erasureAPIKeyLockRead   = regexp.MustCompile("^SELECT `id` FROM `api_keys` WHERE id IN ")
	erasureGraphScrubWrite  = regexp.MustCompile("^UPDATE `(leads|customers|users)` SET ")
	erasureCredentialDelete = regexp.MustCompile("^DELETE FROM `(refresh_tokens|password_reset_tokens|api_keys)`")
	// erasureLeadLockWait is what a second erasure of the same person waits
	// on: the cancel of a run the first one cancelled, or the lead it locked.
	erasureLeadLockWait = regexp.MustCompile("(?s)^(UPDATE `automation_runs` SET |SELECT `id`,`status`,`customer_id`,`deleted_at` FROM `leads` WHERE .*FOR UPDATE$)")
)

// erasureIDs returns the bind values of a statement as ids.
func erasureIDs(t *testing.T, s erasureStatement) []uint {
	t.Helper()
	ids := make([]uint, 0, len(s.vars))
	for _, v := range s.vars {
		value := reflect.ValueOf(v)
		switch {
		case value.CanUint():
			ids = append(ids, uint(value.Uint()))
		case value.CanInt():
			ids = append(ids, uint(value.Int()))
		default:
			t.Fatalf("a bind value of %q is not an id: %T", s.sql, v)
		}
	}
	return ids
}

// erasureLockReads returns the index of every transaction statement that
// matches re, and the ids those statements were bound to, in order.
func erasureLockReads(t *testing.T, trace []erasureStatement, re *regexp.Regexp) (at []int, ids []uint) {
	t.Helper()
	for i, s := range trace {
		if s.inTx && re.MatchString(s.sql) {
			at = append(at, i)
			// A read by `id = ?` is bound to the id first (MySQL binds its
			// LIMIT after it); a read by `id IN (...)` to the ids only.
			if strings.Contains(s.sql, " IN ") {
				ids = append(ids, erasureIDs(t, s)...)
			} else {
				require.NotEmpty(t, s.vars, "%q", s.sql)
				ids = append(ids, erasureIDs(t, erasureStatement{sql: s.sql, vars: s.vars[:1]})...)
			}
		}
	}
	return at, ids
}

// erasureTxMatches returns the index of every transaction statement that
// matches re.
func erasureTxMatches(trace []erasureStatement, re *regexp.Regexp) []int {
	var at []int
	for i, s := range trace {
		if s.inTx && re.MatchString(s.sql) {
			at = append(at, i)
		}
	}
	return at
}

func sortedUints(ids ...uint) []uint {
	out := append([]uint(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// erasureWhere is the WHERE clause of a statement, or "" without one.
func erasureWhere(sql string) string {
	at := strings.Index(sql, " WHERE ")
	if at < 0 {
		return ""
	}
	return sql[at:]
}

// lockOrderRoot is one way into the graph of an identityGraph.
type lockOrderRoot struct {
	name  string
	erase func(db *gorm.DB, g *identityGraph) error
}

func lockOrderRoots() []lockOrderRoot {
	return []lockOrderRoot{
		{"user root", func(db *gorm.DB, g *identityGraph) error {
			return NewUserRepository(db).Delete(context.Background(), g.login.ID)
		}},
		{"customer root", func(db *gorm.DB, g *identityGraph) error {
			return NewCustomerRepository(db).Delete(context.Background(), g.customerGone.ID)
		}},
		{"lead root", func(db *gorm.DB, g *identityGraph) error {
			return NewLeadRepository(db).Delete(context.Background(), g.leadB.ID)
		}},
	}
}

func TestErasureLockOrderSQLite(t *testing.T) {
	runErasureLockOrder(t, false)
}

func TestErasureLockOrderMySQL(t *testing.T) {
	testsupport.MySQLFromEnv(t)
	runErasureLockOrder(t, true)
}

// runErasureLockOrder erases a person stored as one login, two customers and
// three converted leads from each root and reads the order of the statements.
func runErasureLockOrder(t *testing.T, useMySQL bool) {
	db := migrateIdentityErasureDB(t, testsupport.Open(t, testsupport.DialectFor(useMySQL), "identity_erasure", identityErasureDB))

	for _, root := range lockOrderRoots() {
		t.Run(root.name, func(t *testing.T) {
			g := seedIdentityGraph(t, db)
			trace, err := captureErasureTrace(t, db, func() error { return root.erase(db, g) })
			require.NoError(t, err)
			assertGraphErased(t, db, g)

			rendered := make([]string, len(trace))
			for i, s := range trace {
				rendered[i] = s.sql
			}

			firstTx := -1
			for i, s := range trace {
				if s.inTx {
					firstTx = i
					break
				}
			}
			require.Greater(t, firstTx, 0, "the trace holds root-handle statements and then the transaction: %q", rendered)

			// The capture sees both handles: the discovery and the schema
			// probes are on the root handle, before the transaction.
			discoveryReads, probes := 0, 0
			for _, s := range trace[:firstTx] {
				require.False(t, s.inTx)
				switch {
				case isErasureSchemaProbe(s.sql):
					probes++
				case strings.HasPrefix(s.sql, "SELECT "):
					discoveryReads++
					assert.NotContains(t, s.sql, "FOR UPDATE", "the discovery reads are plain: %q", s.sql)
				default:
					t.Errorf("the root handle issued something other than a read before the transaction: %q", s.sql)
				}
			}
			assert.Positive(t, discoveryReads, "the discovery reads were captured on the root handle: %q", rendered)
			assert.Positive(t, probes, "the schema probes of the cancel were captured on the root handle: %q", rendered)
			for _, s := range trace[firstTx:] {
				assert.True(t, s.inTx, "after its transaction began the erasure issues nothing on the root handle: %q", s.sql)
			}

			cancels := erasureTxMatches(trace, erasureRunCancel)
			leadsAt, leadIDs := erasureLockReads(t, trace, erasureLeadLockRead)
			customersAt, customerIDs := erasureLockReads(t, trace, erasureCustomerRead)
			usersAt, userIDs := erasureLockReads(t, trace, erasureUserLockRead)
			refreshAt, refreshIDs := erasureLockReads(t, trace, erasureRefreshLockRead)
			resetAt, resetIDs := erasureLockReads(t, trace, erasureResetLockRead)
			keysAt, keyIDs := erasureLockReads(t, trace, erasureAPIKeyLockRead)

			// Ids ascending inside each table, and exactly the graph.
			assert.Equal(t, sortedUints(g.leadLive.ID, g.leadGone.ID, g.leadB.ID), leadIDs, "the leads are locked in ascending id")
			assert.Equal(t, sortedUints(g.customerLive.ID, g.customerGone.ID), customerIDs, "the customers are locked in ascending id")
			assert.Equal(t, []uint{g.login.ID}, userIDs, "the login is locked")
			for name, ids := range map[string][]uint{"refresh_tokens": refreshIDs, "password_reset_tokens": resetIDs, "api_keys": keyIDs} {
				assert.Len(t, ids, 1, "the one %s row of the login is locked by id", name)
				assert.True(t, sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] < ids[j] }), "%s ids ascending", name)
			}
			require.Len(t, refreshAt, 1)
			require.Len(t, resetAt, 1)
			require.Len(t, keysAt, 1)

			// The order of the tables.
			require.Len(t, cancels, 3, "one cancel per lead: %q", rendered)
			assert.Equal(t, firstTx, cancels[0], "the cancel is the first statement of the transaction")
			var cancelled []uint
			for _, i := range cancels {
				// The bind values end in entity_id and the three statuses.
				vars := trace[i].vars
				require.GreaterOrEqual(t, len(vars), 4)
				cancelled = append(cancelled, erasureIDs(t, erasureStatement{sql: trace[i].sql, vars: vars[len(vars)-4 : len(vars)-3]})...)
			}
			assert.Equal(t, leadIDs, cancelled, "the runs are cancelled lead by lead, in ascending id")
			assert.Less(t, cancels[2], leadsAt[0], "every cancel precedes the first lock of leads")
			assert.Less(t, leadsAt[len(leadsAt)-1], customersAt[0], "leads, then customers")
			assert.Less(t, customersAt[len(customersAt)-1], usersAt[0], "customers, then users")
			assert.Less(t, usersAt[len(usersAt)-1], refreshAt[0], "users, then refresh_tokens")
			assert.Less(t, refreshAt[0], resetAt[0], "refresh_tokens, then password_reset_tokens")
			assert.Less(t, resetAt[0], keysAt[0], "password_reset_tokens, then api_keys")
			for i, s := range trace {
				if erasureGraphScrubWrite.MatchString(s.sql) || erasureCredentialDelete.MatchString(s.sql) {
					assert.Greater(t, i, keysAt[0], "nothing of the graph is written before its last lock: %q", s.sql)
				}
			}

			if !useMySQL {
				return
			}

			// MySQL/MariaDB: the locks are FOR UPDATE reads by primary key.
			for _, at := range [][]int{leadsAt, customersAt, usersAt, refreshAt, resetAt, keysAt} {
				for _, i := range at {
					sql := trace[i].sql
					assert.True(t, strings.HasSuffix(sql, "FOR UPDATE"), "a graph lock is a locking read: %q", sql)
					where := erasureWhere(sql)
					assert.Regexp(t, "^ WHERE (`[a-z_]+`\\.)?`?id`? (= \\?|IN \\(\\?(,\\?)*\\))( ORDER BY| FOR UPDATE)", where, "a graph lock filters on id only: %q", sql)
				}
			}
			for _, s := range trace {
				if !strings.Contains(s.sql, "FOR UPDATE") {
					continue
				}
				where := erasureWhere(s.sql)
				for _, column := range []string{"customer_id", "user_id", "deleted_at"} {
					assert.NotContains(t, where, column, "no locking read filters on %s: %q", column, s.sql)
				}
			}

			// The snapshot rule: from its first statement through the locking
			// read of users the transaction issues no plain read of any kind,
			// a schema probe included.
			for _, s := range trace[firstTx : usersAt[len(usersAt)-1]+1] {
				if strings.HasPrefix(strings.ToUpper(s.sql), "SELECT") {
					assert.True(t, strings.HasSuffix(s.sql, "FOR UPDATE"),
						"a plain read precedes the last graph lock on the transaction: %q", s.sql)
				}
				assert.NotContains(t, strings.ToLower(s.sql), "information_schema", "a schema probe ran on the transaction before the locks")
			}
			sawProbe := false
			for _, s := range trace[:firstTx] {
				sawProbe = sawProbe || strings.Contains(strings.ToLower(s.sql), "information_schema")
			}
			assert.True(t, sawProbe, "the information_schema probes were captured, on the root handle")
		})
	}

	t.Run("DeleteUnconverted stays on the lead", func(t *testing.T) {
		g := seedIdentityGraph(t, db)
		lead := &models.Lead{FirstName: "Solo", LastName: "Lead", Email: "solo.lead." + g.tag + "@person.test",
			Status: models.LeadStatusQualified, OwnerID: g.sales.ID}
		require.NoError(t, db.Create(lead).Error)

		trace, err := captureErasureTrace(t, db, func() error { return NewLeadRepository(db).DeleteUnconverted(context.Background(), lead.ID) })
		require.NoError(t, err)
		_, leadIDs := erasureLockReads(t, trace, erasureLeadLockRead)
		assert.Equal(t, []uint{lead.ID}, leadIDs, "the root lead is locked")
		for _, s := range trace {
			assert.NotContains(t, s.sql, "`customers`", "DeleteUnconverted issues nothing on customers: %q", s.sql)
			assert.NotContains(t, s.sql, "`users`", "DeleteUnconverted issues nothing on users: %q", s.sql)
		}
		var erased models.Lead
		require.NoError(t, db.Unscoped().First(&erased, lead.ID).Error)
		assert.True(t, erased.DeletedAt.Valid)

		// A converted lead is refused on its locked row, with the same reach.
		trace, err = captureErasureTrace(t, db, func() error { return NewLeadRepository(db).DeleteUnconverted(context.Background(), g.leadB.ID) })
		require.ErrorIs(t, err, ErrConvertedLeadNeedsAdmin)
		for _, s := range trace {
			assert.NotContains(t, s.sql, "`customers`", "a refused erasure issues nothing on customers: %q", s.sql)
			assert.NotContains(t, s.sql, "`users`", "a refused erasure issues nothing on users: %q", s.sql)
		}
	})
}

// crossRootPerson is one person as a customer-role login with its
// credentials, the customer linked to it and the lead converted into that
// customer, with a pending automation run on the lead.
type crossRootPerson struct {
	login    *models.User
	customer *models.Customer
	lead     *models.Lead
	run      *models.AutomationRun
}

// crossRootFixture seeds persons on one database.
type crossRootFixture struct {
	db    *gorm.DB
	owner *models.User
	rule  *models.AutomationRule
}

func newCrossRootFixture(t *testing.T, db *gorm.DB) *crossRootFixture {
	t.Helper()
	tag := nextIdentityTag()
	f := &crossRootFixture{db: db, owner: seedIdentityStaff(t, db, tag, models.RoleSales)}
	f.rule = &models.AutomationRule{Name: "Cross root " + tag, EntityType: models.AutomationEntityLead, TriggerType: "lead_created", Enabled: true, Version: 1}
	require.NoError(t, NewAutomationRepository(db).CreateRule(f.rule))
	return f
}

// person seeds a login with one refresh token, one reset token and one API
// key, and its customer. withLead adds the converted lead and its pending run.
func (f *crossRootFixture) person(t *testing.T, withLead bool) *crossRootPerson {
	t.Helper()
	tag := nextIdentityTag()
	p := &crossRootPerson{}
	p.login = &models.User{Email: "cross.login." + tag + "@person.test", Password: "$2a$10$crosshash" + tag,
		FirstName: "Cross", LastName: "Root", Role: models.RoleCustomer, IsActive: true}
	require.NoError(t, f.db.Create(p.login).Error)
	expires := time.Now().Add(time.Hour)
	require.NoError(t, f.db.Create(&models.RefreshToken{UserID: p.login.ID, TokenHash: "xrt" + tag, ExpiresAt: expires}).Error)
	require.NoError(t, f.db.Create(&models.PasswordResetToken{UserID: p.login.ID, TokenHash: "xpr" + tag, ExpiresAt: expires}).Error)
	require.NoError(t, f.db.Create(&models.APIKey{Name: "cli", KeyHash: "xkh" + tag, Prefix: "gcrm_ab", UserID: p.login.ID}).Error)
	p.customer = f.customerOf(t, p.login.ID)
	if withLead {
		p.lead = f.leadOf(t, &p.customer.ID)
		p.run = f.runOf(t, p.lead.ID)
	}
	return p
}

func (f *crossRootFixture) customerOf(t *testing.T, loginID uint) *models.Customer {
	t.Helper()
	customer := &models.Customer{FirstName: "Cross", LastName: "Root", Email: "cross.customer." + nextIdentityTag() + "@person.test",
		Phone: "+44 7700 900123", UserID: &loginID}
	require.NoError(t, f.db.Create(customer).Error)
	return customer
}

func (f *crossRootFixture) leadOf(t *testing.T, customerID *uint) *models.Lead {
	t.Helper()
	lead := &models.Lead{FirstName: "Cross", LastName: "Root", Email: "cross.lead." + nextIdentityTag() + "@person.test",
		Phone: "+44 7700 900123", Status: models.LeadStatusQualified, OwnerID: f.owner.ID}
	if customerID != nil {
		lead.Status = models.LeadStatusConverted
		lead.CustomerID = customerID
	}
	require.NoError(t, f.db.Create(lead).Error)
	return lead
}

func (f *crossRootFixture) runOf(t *testing.T, leadID uint) *models.AutomationRun {
	t.Helper()
	run := &models.AutomationRun{RuleID: f.rule.ID, RuleVersion: 1, EntityType: models.AutomationEntityLead, EntityID: leadID,
		DedupeKey: "cross-root:" + nextIdentityTag(), Status: models.AutomationRunPending, NextPosition: 1}
	inserted, err := NewAutomationRepository(f.db).CreateRunIgnoringDuplicate(run)
	require.NoError(t, err)
	require.True(t, inserted)
	return run
}

func assertUserErased(t *testing.T, db *gorm.DB, id uint) {
	t.Helper()
	var user models.User
	require.NoError(t, db.Unscoped().First(&user, id).Error)
	assert.True(t, user.DeletedAt.Valid, "user %d is soft-deleted", id)
	assert.True(t, strings.HasSuffix(user.Email, "@"+anonymizedEmailDomain), "user %d has a placeholder address", id)
	assert.Empty(t, user.FirstName, "user %d", id)
	assert.Empty(t, user.LastName, "user %d", id)
	assert.Equal(t, unusablePasswordHash, user.Password, "user %d", id)
	for _, model := range []interface{}{&models.RefreshToken{}, &models.PasswordResetToken{}, &models.APIKey{}} {
		assert.Zero(t, countRows(t, db, model, "user_id = ?", id), "%T of user %d must be gone", model, id)
	}
}

func assertCustomerErased(t *testing.T, db *gorm.DB, id uint) {
	t.Helper()
	var customer models.Customer
	require.NoError(t, db.Unscoped().First(&customer, id).Error)
	assert.True(t, customer.DeletedAt.Valid, "customer %d is soft-deleted", id)
	assert.True(t, strings.HasSuffix(customer.Email, "@"+anonymizedEmailDomain), "customer %d has a placeholder address", id)
	assert.Empty(t, customer.FirstName, "customer %d", id)
	assert.Empty(t, customer.LastName, "customer %d", id)
	assert.Empty(t, customer.Phone, "customer %d", id)
}

func assertLeadErased(t *testing.T, db *gorm.DB, id uint) {
	t.Helper()
	var lead models.Lead
	require.NoError(t, db.Unscoped().First(&lead, id).Error)
	assert.True(t, lead.DeletedAt.Valid, "lead %d is soft-deleted", id)
	assert.True(t, strings.HasSuffix(lead.Email, "@"+anonymizedEmailDomain), "lead %d has a placeholder address", id)
	assert.Empty(t, lead.FirstName, "lead %d", id)
	assert.Empty(t, lead.LastName, "lead %d", id)
	assert.Empty(t, lead.Phone, "lead %d", id)
}

// assertPersonErased checks the committed rows of a whole person.
func assertPersonErased(t *testing.T, db *gorm.DB, p *crossRootPerson) {
	t.Helper()
	assertUserErased(t, db, p.login.ID)
	assertCustomerErased(t, db, p.customer.ID)
	if p.lead == nil {
		return
	}
	assertLeadErased(t, db, p.lead.ID)
	var run models.AutomationRun
	require.NoError(t, db.First(&run, p.run.ID).Error)
	assert.Equal(t, models.AutomationRunCancelled, run.Status, "the lead's run is cancelled")
}

// crossRootSide is one of the two erasures of a pair.
type crossRootSide struct {
	name  string
	erase func(db *gorm.DB, p *crossRootPerson, hooks ErasureHooks) error
}

var (
	crossRootUser = crossRootSide{"user-rooted", func(db *gorm.DB, p *crossRootPerson, hooks ErasureHooks) error {
		return NewUserRepository(db, WithUserErasureHooks(hooks)).Delete(context.Background(), p.login.ID)
	}}
	crossRootCustomer = crossRootSide{"customer-rooted", func(db *gorm.DB, p *crossRootPerson, hooks ErasureHooks) error {
		return NewCustomerRepository(db, WithCustomerErasureHooks(hooks)).Delete(context.Background(), p.customer.ID)
	}}
	crossRootLead = crossRootSide{"lead-rooted", func(db *gorm.DB, p *crossRootPerson, hooks ErasureHooks) error {
		return NewLeadRepository(db, WithLeadErasureLockHooks(hooks)).Delete(context.Background(), p.lead.ID)
	}}
)

func awaitErasure(t *testing.T, who string, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(lockOrderRepoTimeout):
		t.Fatalf("the %s erasure did not finish", who)
		return nil
	}
}

// TestErasureCrossRootMySQL runs two erasures of one person entered from
// different roots: with each side parked in turn right after its first lock
// stage while the other one arrives and waits, and in free-running pairs.
// Both calls return nil (so neither is the victim of a deadlock that reaches
// the caller) and the person is erased.
func TestErasureCrossRootMySQL(t *testing.T) {
	testsupport.MySQLFromEnv(t)
	db := migrateIdentityErasureDB(t, testsupport.Open(t, testsupport.MySQL, "identity_erasure", identityErasureDB))
	f := newCrossRootFixture(t, db)

	parked := func(t *testing.T, holder, waiter crossRootSide) {
		p := f.person(t, true)
		reached, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		holderDone := make(chan error, 1)
		go func() {
			holderDone <- holder.erase(db, p, ErasureHooks{AfterLock: func(table string) {
				if table == erasureStageLeads {
					once.Do(func() {
						close(reached)
						<-release
					})
				}
			}})
		}()
		select {
		case <-reached:
		case <-time.After(lockOrderRepoTimeout):
			t.Fatalf("the %s erasure never held its first lock stage", holder.name)
		}

		waiterDone := make(chan error, 1)
		go func() { waiterDone <- waiter.erase(db, p, ErasureHooks{}) }()
		// The other root queues behind the first rows of the same order: the
		// run the holder cancelled, or the lead it locked.
		awaitLockOrderWait(t, db, erasureLeadLockWait)
		close(release)

		holderErr := awaitErasure(t, holder.name, holderDone)
		waiterErr := awaitErasure(t, waiter.name, waiterDone)
		require.NoError(t, holderErr, "the %s erasure", holder.name)
		require.NoError(t, waiterErr, "the %s erasure", waiter.name)
		assertPersonErased(t, db, p)
	}

	freeRunning := func(t *testing.T, a, b crossRootSide) {
		for pair := 0; pair < erasureCrossRootPairs; pair++ {
			p := f.person(t, true)
			start := make(chan struct{})
			aDone, bDone := make(chan error, 1), make(chan error, 1)
			go func() { <-start; aDone <- a.erase(db, p, ErasureHooks{}) }()
			go func() { <-start; bDone <- b.erase(db, p, ErasureHooks{}) }()
			close(start)
			aErr := awaitErasure(t, a.name, aDone)
			bErr := awaitErasure(t, b.name, bDone)
			require.NoError(t, aErr, "pair %d: the %s erasure", pair, a.name)
			require.NoError(t, bErr, "pair %d: the %s erasure", pair, b.name)
			assertPersonErased(t, db, p)
		}
	}

	for _, pairing := range [][2]crossRootSide{{crossRootUser, crossRootCustomer}, {crossRootLead, crossRootCustomer}} {
		a, b := pairing[0], pairing[1]
		t.Run(a.name+" vs "+b.name, func(t *testing.T) {
			t.Run(a.name+" parked after its first lock stage", func(t *testing.T) { parked(t, a, b) })
			t.Run(b.name+" parked after its first lock stage", func(t *testing.T) { parked(t, b, a) })
			t.Run(fmt.Sprintf("%d free-running pairs", erasureCrossRootPairs), func(t *testing.T) { freeRunning(t, a, b) })
		})
	}
}

// erasureCounters counts the discoveries and the transactions of an erasure
// through its hooks: a discovery is a call of AfterDiscovery, a transaction a
// call of BeforeLock for the leads stage.
type erasureCounters struct {
	discoveries  atomic.Int32
	transactions atomic.Int32
}

// hooks returns counting hooks. afterDiscovery and beforeLeads, when set, run
// with the 1-based number of the discovery or transaction.
func (c *erasureCounters) hooks(afterDiscovery func(n int32), beforeLeads func(n int32) error) ErasureHooks {
	return ErasureHooks{
		AfterDiscovery: func() {
			n := c.discoveries.Add(1)
			if afterDiscovery != nil {
				afterDiscovery(n)
			}
		},
		BeforeLock: func(table string) error {
			if table != erasureStageLeads {
				return nil
			}
			n := c.transactions.Add(1)
			if beforeLeads != nil {
				return beforeLeads(n)
			}
			return nil
		},
	}
}

func TestErasureGraphChangedSQLite(t *testing.T) {
	runErasureGraphChanged(t, false)
}

func TestErasureGraphChangedMySQL(t *testing.T) {
	testsupport.MySQLFromEnv(t)
	runErasureGraphChanged(t, true)
}

// runErasureGraphChanged moves the graph between an erasure's discovery and
// its locks. The MySQL root adds the two cases that need a second connection
// while the erasure is in flight.
func runErasureGraphChanged(t *testing.T, useMySQL bool) {
	db := migrateIdentityErasureDB(t, testsupport.Open(t, testsupport.DialectFor(useMySQL), "identity_erasure", identityErasureDB))
	f := newCrossRootFixture(t, db)

	t.Run("(1) a conversion commits after the discovery of a lead-rooted erasure", func(t *testing.T) {
		lead := f.leadOf(t, nil)
		run := f.runOf(t, lead.ID)
		var customer *models.Customer
		var counters erasureCounters
		hooks := counters.hooks(func(n int32) {
			if n != 1 {
				return
			}
			// What a conversion commits: a new customer, and the lead pointing
			// at it.
			customer = &models.Customer{FirstName: "Cross", LastName: "Root", Email: "converted." + nextIdentityTag() + "@person.test"}
			require.NoError(t, db.Create(customer).Error)
			require.NoError(t, db.Model(&models.Lead{}).Where("id = ?", lead.ID).
				Updates(map[string]interface{}{"customer_id": customer.ID, "status": models.LeadStatusConverted}).Error)
		}, nil)

		require.NoError(t, NewLeadRepository(db, WithLeadErasureLockHooks(hooks)).Delete(context.Background(), lead.ID))
		require.NotNil(t, customer)
		assertLeadErased(t, db, lead.ID)
		assertCustomerErased(t, db, customer.ID)
		assert.EqualValues(t, 2, counters.discoveries.Load(), "the erasure discovered the graph again once")
		assert.EqualValues(t, 2, counters.transactions.Load())
		var stored models.AutomationRun
		require.NoError(t, db.First(&stored, run.ID).Error)
		assert.Equal(t, models.AutomationRunCancelled, stored.Status)
	})

	if useMySQL {
		t.Run("(2) a customer is linked to the login after the discovery of a user-rooted erasure", func(t *testing.T) {
			p := f.person(t, false)
			var second *models.Customer
			var counters erasureCounters
			hooks := counters.hooks(func(n int32) {
				if n == 1 {
					second = f.customerOf(t, p.login.ID)
				}
			}, nil)

			require.NoError(t, NewUserRepository(db, WithUserErasureHooks(hooks)).Delete(context.Background(), p.login.ID))
			require.NotNil(t, second)
			assertPersonErased(t, db, p)
			assertCustomerErased(t, db, second.ID)
			assert.EqualValues(t, 2, counters.discoveries.Load(), "the erasure discovered the graph again once")
		})
	}

	t.Run("(3) the graph changes after every discovery", func(t *testing.T) {
		p := f.person(t, true)
		before := snapshotIdentityDB(t, db)
		var added []uint
		var counters erasureCounters
		hooks := counters.hooks(func(int32) {
			// Another lead is converted into the customer, every time.
			added = append(added, f.leadOf(t, &p.customer.ID).ID)
		}, nil)

		err := NewCustomerRepository(db, WithCustomerErasureHooks(hooks)).Delete(context.Background(), p.customer.ID)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrErasureGraphChanged)
		assert.EqualValues(t, erasureGraphMaxRestarts+1, counters.discoveries.Load(), "one discovery and the bounded restarts")
		assert.EqualValues(t, erasureGraphMaxRestarts+1, counters.transactions.Load(), "one transaction per discovery")
		require.Len(t, added, erasureGraphMaxRestarts+1)

		// Nothing of any attempt is kept: without the leads the hook added,
		// every row of the database is what it was.
		require.NoError(t, db.Unscoped().Where("id IN ?", added).Delete(&models.Lead{}).Error)
		assert.Equal(t, before, snapshotIdentityDB(t, db), "every row of the graph is unchanged")
	})

	if useMySQL {
		t.Run("(4) a customer is linked to the login after the cancel, before the locks", func(t *testing.T) {
			p := f.person(t, true)
			var second *models.Customer
			var counters erasureCounters
			hooks := counters.hooks(nil, func(n int32) error {
				if n == 1 {
					// From another connection, while the erasure's transaction
					// is open and has cancelled the lead's run. The erasure
					// sees it only when nothing has fixed its snapshot yet.
					second = f.customerOf(t, p.login.ID)
				}
				return nil
			})

			require.NoError(t, NewUserRepository(db, WithUserErasureHooks(hooks)).Delete(context.Background(), p.login.ID))
			require.NotNil(t, second)
			assertPersonErased(t, db, p)
			assertCustomerErased(t, db, second.ID)
			assert.EqualValues(t, 2, counters.discoveries.Load(), "the erasure discovered the graph again once")
			assert.EqualValues(t, 2, counters.transactions.Load())
		})
	}
}

// errInjectedDeadlock is what a hook returns in place of the engine: the text
// the transaction manager recognises as a deadlock.
var errInjectedDeadlock error = &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock; try restarting transaction"}

// TestErasureRetryBoundSQLite makes the first lock stage of each root fail
// with a deadlock: always (the transaction is replayed erasureMaxRetries times
// on one discovery and the error surfaces, nothing kept) and once (the replay
// erases the person).
func TestErasureRetryBoundSQLite(t *testing.T) {
	db := migrateIdentityErasureDB(t, testsupport.Open(t, testsupport.SQLite, "identity_erasure", identityErasureDB))

	roots := []struct {
		name  string
		erase func(g *identityGraph, hooks ErasureHooks) error
	}{
		{"user root", func(g *identityGraph, hooks ErasureHooks) error {
			return NewUserRepository(db, WithUserErasureHooks(hooks)).Delete(context.Background(), g.login.ID)
		}},
		{"customer root", func(g *identityGraph, hooks ErasureHooks) error {
			return NewCustomerRepository(db, WithCustomerErasureHooks(hooks)).Delete(context.Background(), g.customerLive.ID)
		}},
		{"lead root", func(g *identityGraph, hooks ErasureHooks) error {
			return NewLeadRepository(db, WithLeadErasureLockHooks(hooks)).Delete(context.Background(), g.leadLive.ID)
		}},
	}

	for _, root := range roots {
		t.Run(root.name+", a deadlock on every transaction", func(t *testing.T) {
			g := seedIdentityGraph(t, db)
			before := snapshotIdentityDB(t, db)
			var counters erasureCounters
			err := root.erase(g, counters.hooks(nil, func(int32) error { return errInjectedDeadlock }))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "1213")
			assert.NotErrorIs(t, err, ErrErasureGraphChanged)
			assert.EqualValues(t, erasureMaxRetries+1, counters.transactions.Load(), "one transaction and the bounded replays")
			assert.EqualValues(t, 1, counters.discoveries.Load(), "a replay reuses the discovery")

			// The root row, its credentials and its automation runs, with
			// every other row of the database.
			assert.Equal(t, before, snapshotIdentityDB(t, db), "nothing of any attempt is kept")
			assert.EqualValues(t, 1, countRows(t, db, &models.RefreshToken{}, "user_id = ?", g.login.ID))
			assert.EqualValues(t, 1, countRows(t, db, &models.PasswordResetToken{}, "user_id = ?", g.login.ID))
			assert.EqualValues(t, 1, countRows(t, db, &models.APIKey{}, "user_id = ?", g.login.ID))
			var run models.AutomationRun
			require.NoError(t, db.First(&run, g.run.ID).Error)
			assert.Equal(t, models.AutomationRunPending, run.Status, "the cancel rolled back with the attempt")
		})

		t.Run(root.name+", a deadlock once", func(t *testing.T) {
			g := seedIdentityGraph(t, db)
			var counters erasureCounters
			err := root.erase(g, counters.hooks(nil, func(n int32) error {
				if n == 1 {
					return errInjectedDeadlock
				}
				return nil
			}))
			require.NoError(t, err)
			assert.EqualValues(t, 2, counters.transactions.Load(), "the failed transaction and its replay")
			assert.EqualValues(t, 1, counters.discoveries.Load())
			assertGraphErased(t, db, g)
		})
	}
}
