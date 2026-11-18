package repository

// Repository roots of #337: the statement order of a lead-rooted erasure
// (a plain discovery of the graph, then the automation cancel ahead of every
// locking read and every write of leads, then a locking read of the lead by
// primary key that decides the refusal and the graph, then the customer by
// primary key, with a plain converted-lead pluck; the discovery and the
// one-order locks are #385), the locking clauses on MySQL, the survival of
// the erasure against an automation step that holds the lead row, and the
// instance-local erasure hooks. Statements are captured through GORM
// callbacks registered only for the duration of the repository call. The MySQL
// roots use a disposable TEST_MYSQL_DSN schema, skip without it and fail when
// it is unreachable.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var leadErasureCaptureSeq atomic.Uint64

// captureErasureStatements runs call with GORM callbacks registered on db's
// query, row, raw, create, update and delete processors and removed again
// right after, and returns the SQL (placeholders kept) of every statement
// call issued, in order. Schema probes (HasTable and the current-database
// read) are dropped.
func captureErasureStatements(t *testing.T, db *gorm.DB, call func() error) ([]string, error) {
	t.Helper()
	var mu sync.Mutex
	var statements []string
	record := func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		sql := strings.TrimSpace(tx.Statement.SQL.String())
		if sql == "" || isErasureSchemaProbe(sql) {
			return
		}
		mu.Lock()
		statements = append(statements, sql)
		mu.Unlock()
	}

	name := fmt.Sprintf("lead_erasure_lock_order:capture_%d", leadErasureCaptureSeq.Add(1))
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
	return append([]string(nil), statements...), err
}

func isErasureSchemaProbe(sql string) bool {
	lower := strings.ToLower(sql)
	return strings.Contains(lower, "sqlite_master") ||
		strings.Contains(lower, "information_schema") ||
		strings.HasPrefix(lower, "select database()") ||
		strings.HasPrefix(lower, "pragma")
}

// The statements of the lead path, as GORM writes them on both dialects
// (backtick quoting, placeholders kept).
var (
	erasureRunCancel     = regexp.MustCompile("^UPDATE `automation_runs` SET ")
	erasureLeadLockRead  = regexp.MustCompile("^SELECT `id`,`status`,`customer_id` FROM `leads` WHERE `leads`.`id` = \\?")
	erasureLeadsSelect   = regexp.MustCompile("^SELECT .*FROM `leads`")
	erasureLeadsUpdate   = regexp.MustCompile("^UPDATE `leads` SET ")
	erasureCustomerRead  = regexp.MustCompile("^SELECT `id`,`user_id` FROM `customers` WHERE `customers`.`id` = \\?")
	erasureCustomersUpd  = regexp.MustCompile("^UPDATE `customers` SET ")
	erasureUsersUpdate   = regexp.MustCompile("^UPDATE `users` SET ")
	erasureConvertedPick = regexp.MustCompile("^SELECT `id` FROM `leads` WHERE customer_id = \\?")
	erasureLeadsDelete   = regexp.MustCompile("^DELETE FROM `leads`")
	erasureLeadEmailRead = regexp.MustCompile("^SELECT `email` FROM `leads` WHERE id = \\?")
	erasureCustEmailRead = regexp.MustCompile("^SELECT `email` FROM `customers` WHERE id = \\?")
)

// lastMatch is the index of the last statement re matches, or -1.
func lastMatch(statements []string, re *regexp.Regexp) int {
	for i := len(statements) - 1; i >= 0; i-- {
		if re.MatchString(statements[i]) {
			return i
		}
	}
	return -1
}

func firstMatch(statements []string, re *regexp.Regexp) int {
	for i, s := range statements {
		if re.MatchString(s) {
			return i
		}
	}
	return -1
}

// leadErasureSeed is a lead of a sales owner with one pending automation run
// and, when converted, the customer it became.
type leadErasureSeed struct {
	lead     *models.Lead
	customer *models.Customer
	run      *models.AutomationRun
}

func seedLeadForErasure(t *testing.T, db *gorm.DB, owner *models.User, rule *models.AutomationRule, converted bool) leadErasureSeed {
	t.Helper()
	seq := leadErasureCaptureSeq.Add(1)
	seed := leadErasureSeed{}
	lead := &models.Lead{FirstName: "Lock", LastName: "Order", Email: fmt.Sprintf("lock-order-lead-%d@repo.test", seq),
		Phone: "+1 555 0137", Status: models.LeadStatusQualified, OwnerID: owner.ID}
	if converted {
		customer := &models.Customer{FirstName: "Lock", LastName: "Order", Email: fmt.Sprintf("lock-order-customer-%d@repo.test", seq)}
		require.NoError(t, db.Create(customer).Error)
		seed.customer = customer
		lead.Status = models.LeadStatusConverted
		lead.CustomerID = &customer.ID
	}
	require.NoError(t, db.Create(lead).Error)
	seed.lead = lead
	seed.run = seedAutomationRun(t, db, rule, lead.ID, models.AutomationRunPending)
	return seed
}

func TestLeadErasureLockOrderSQLite(t *testing.T) {
	db := openAutomationRepoDB(t, false, 1)
	owner := seedAutomationUser(t, db, "lock-order-owner", models.RoleSales)
	rule := seedAutomationRule(t, db, models.AutomationEntityLead)

	// The discovery (#385) reads the graph with plain reads before the cancel;
	// what stays ahead of every lock and every write of leads is the cancel
	// (#337). SQLite drops FOR UPDATE, so the locking read is recognised by its
	// shape; nothing but a SELECT precedes the cancel at all.
	assertCancelFirst := func(t *testing.T, statements []string) {
		t.Helper()
		require.NotEmpty(t, statements)
		cancelAt := firstMatch(statements, erasureRunCancel)
		require.GreaterOrEqual(t, cancelAt, 0, "the automation_runs cancel: %q", statements)
		for _, s := range statements[:cancelAt] {
			assert.True(t, strings.HasPrefix(s, "SELECT "), "only discovery reads precede the cancel: %q", s)
			assert.NotContains(t, s, "FOR UPDATE", "no locking read precedes the cancel: %q", s)
			assert.NotRegexp(t, erasureLeadLockRead, s, "no locking read of leads precedes the cancel")
		}
		for _, re := range []*regexp.Regexp{erasureLeadLockRead, erasureLeadsUpdate, erasureLeadsDelete} {
			at := firstMatch(statements, re)
			assert.True(t, at < 0 || at > cancelAt, "no locking read and no write of leads precedes the cancel: %q", statements)
		}
		assert.GreaterOrEqual(t, firstMatch(statements[:cancelAt], erasureLeadsSelect), 0, "the discovery read the lead before the cancel: %q", statements)
	}

	t.Run("admin erasure of a converted lead", func(t *testing.T) {
		seed := seedLeadForErasure(t, db, owner, rule, true)
		statements, err := captureErasureStatements(t, db, func() error { return NewLeadRepository(db).Delete(seed.lead.ID) })
		require.NoError(t, err)
		assertCancelFirst(t, statements)

		leadRead := firstMatch(statements, erasureLeadLockRead)
		leadScrub := firstMatch(statements, erasureLeadsUpdate)
		customerRead := firstMatch(statements, erasureCustomerRead)
		pluck := lastMatch(statements, erasureConvertedPick)
		require.GreaterOrEqual(t, leadRead, 0, "the lead read: %q", statements)
		require.GreaterOrEqual(t, leadScrub, 0, "the leads scrub: %q", statements)
		require.GreaterOrEqual(t, customerRead, 0, "the customer read: %q", statements)
		require.GreaterOrEqual(t, pluck, 0, "the converted-lead pluck: %q", statements)
		assert.Less(t, leadRead, leadScrub, "the lead read precedes the leads scrub UPDATE")
		assert.Less(t, leadRead, customerRead, "the customer read follows the lead read")
		assert.Less(t, leadRead, pluck, "the re-check's converted-lead pluck follows the lead read")
		assert.Less(t, customerRead, leadScrub, "every graph lock precedes the first scrub")
		assert.Equal(t, 1, countMatches(statements, erasureLeadLockRead), "the lead is read once")
		assertWritesByPrimaryKeyOnly(t, statements)

		var erased models.Customer
		require.NoError(t, db.Unscoped().First(&erased, seed.customer.ID).Error)
		assert.True(t, erased.DeletedAt.Valid, "the customer is erased")
	})

	t.Run("refused non-admin erasure of a converted lead", func(t *testing.T) {
		seed := seedLeadForErasure(t, db, owner, rule, true)
		statements, err := captureErasureStatements(t, db, func() error { return NewLeadRepository(db).DeleteUnconverted(seed.lead.ID) })
		require.ErrorIs(t, err, ErrConvertedLeadNeedsAdmin)
		require.ErrorIs(t, err, apperrors.ErrForbidden)
		assertCancelFirst(t, statements)
		assert.GreaterOrEqual(t, firstMatch(statements, erasureLeadLockRead), 0, "the refusal is decided on the lead read")
		assert.Equal(t, -1, firstMatch(statements, erasureLeadsUpdate), "no scrub UPDATE of leads: %q", statements)
		assert.Equal(t, -1, firstMatch(statements, erasureCustomersUpd), "no scrub UPDATE of customers: %q", statements)

		var run models.AutomationRun
		require.NoError(t, db.First(&run, seed.run.ID).Error)
		assert.Equal(t, models.AutomationRunPending, run.Status, "the cancel rolled back with the refusal")
		var lead models.Lead
		require.NoError(t, db.First(&lead, seed.lead.ID).Error)
		assert.Equal(t, seed.lead.Email, lead.Email, "the lead is unchanged")
	})

	t.Run("non-admin erasure of an unconverted lead", func(t *testing.T) {
		seed := seedLeadForErasure(t, db, owner, rule, false)
		statements, err := captureErasureStatements(t, db, func() error { return NewLeadRepository(db).DeleteUnconverted(seed.lead.ID) })
		require.NoError(t, err)
		assertCancelFirst(t, statements)
		leadRead := firstMatch(statements, erasureLeadLockRead)
		leadScrub := firstMatch(statements, erasureLeadsUpdate)
		require.GreaterOrEqual(t, leadRead, 0)
		require.GreaterOrEqual(t, leadScrub, 0)
		assert.Less(t, leadRead, leadScrub)
		assert.Equal(t, -1, firstMatch(statements, erasureCustomerRead), "no customer is reached")
	})
}

// assertWritesByPrimaryKeyOnly checks that no UPDATE of leads or customers in
// the erasure names deleted_at in its WHERE clause. GORM's own soft delete adds
// `AND deleted_at IS NULL`, which MariaDB 10.11 may serve through the
// deleted_at index as a locking range scan: next-key locks on every live row
// and a gap lock that blocks an unrelated conversion's customer INSERT (#337,
// AC-9). The erasure soft-deletes by primary key only (softDeleteErased),
// through GORM's Delete with AllowGlobalUpdate for that one statement, so the
// check also pins that the soft delete of every erased table (leads, customers,
// users) carries exactly the `id = ?` predicate and never an empty WHERE.
func assertWritesByPrimaryKeyOnly(t *testing.T, statements []string) {
	t.Helper()
	softDeletes := 0
	for _, s := range statements {
		if !erasureLeadsUpdate.MatchString(s) && !erasureCustomersUpd.MatchString(s) && !erasureUsersUpdate.MatchString(s) {
			continue
		}
		at := strings.Index(s, " WHERE ")
		if !assert.GreaterOrEqual(t, at, 0, "an erasure UPDATE has a WHERE clause: %q", s) {
			continue
		}
		assert.NotContains(t, s[at:], "deleted_at", "an erasure UPDATE filters by primary key only: %q", s)
		if strings.Contains(s[:at], "`deleted_at`=") {
			assert.Equal(t, " WHERE id = ?", strings.TrimRight(s[at:], " "), "a soft delete names the primary key only: %q", s)
			softDeletes++
		}
	}
	assert.Equal(t, 2, softDeletes, "the lead and the customer are soft-deleted by primary key: %q", statements)
}

func countMatches(statements []string, re *regexp.Regexp) int {
	n := 0
	for _, s := range statements {
		if re.MatchString(s) {
			n++
		}
	}
	return n
}

func TestLeadErasureDiscoveryReadsLockMySQL(t *testing.T) {
	db := openAutomationRepoDB(t, true, 2)
	owner := seedAutomationUser(t, db, "discovery-owner", models.RoleSales)
	rule := seedAutomationRule(t, db, models.AutomationEntityLead)
	seed := seedLeadForErasure(t, db, owner, rule, true)

	statements, err := captureErasureStatements(t, db, func() error { return NewLeadRepository(db).Delete(seed.lead.ID) })
	require.NoError(t, err)

	locked := func(re *regexp.Regexp, what string) {
		t.Helper()
		at := firstMatch(statements, re)
		if !assert.GreaterOrEqual(t, at, 0, "%s was not captured: %q", what, statements) {
			return
		}
		assert.True(t, strings.HasSuffix(statements[at], "FOR UPDATE"), "%s carries FOR UPDATE: %q", what, statements[at])
		assert.Contains(t, statements[at], "id` = ?", "%s", what)
	}
	lockedByID := func(re *regexp.Regexp, what string) {
		t.Helper()
		at := firstMatch(statements, re)
		if !assert.GreaterOrEqual(t, at, 0, "%s was not captured: %q", what, statements) {
			return
		}
		assert.True(t, strings.HasSuffix(statements[at], "FOR UPDATE"), "%s carries FOR UPDATE: %q", what, statements[at])
		assert.Contains(t, statements[at], "WHERE id = ?", "%s", what)
	}
	locked(erasureLeadLockRead, "the lead read that decides the refusal and the graph")
	locked(erasureCustomerRead, "eraseCustomer's customer read")
	lockedByID(erasureLeadEmailRead, "readErasedEmail of the lead")
	lockedByID(erasureCustEmailRead, "readErasedEmail of the customer")

	require.Equal(t, 2, countMatches(statements, erasureConvertedPick), "the converted-lead pluck of the discovery and of the re-check: %q", statements)
	for _, s := range statements {
		if erasureConvertedPick.MatchString(s) {
			assert.NotContains(t, s, "FOR UPDATE", "the converted-lead pluck is a plain read")
		}
	}

	// The discovery reads leads plainly before the cancel (#385); the first
	// statement that locks or writes leads comes after it.
	cancelAt := firstMatch(statements, erasureRunCancel)
	firstLeads := -1
	for i, s := range statements {
		if strings.Contains(s, "`leads`") && (!strings.HasPrefix(s, "SELECT ") || strings.Contains(s, "FOR UPDATE")) {
			firstLeads = i
			break
		}
	}
	require.GreaterOrEqual(t, cancelAt, 0)
	require.GreaterOrEqual(t, firstLeads, 0)
	assert.Less(t, cancelAt, firstLeads, "the automation_runs cancel precedes the first lock and the first write of leads")
	assert.Regexp(t, erasureLeadLockRead, statements[firstLeads], "and that statement is the locking read of the lead")
}

// isDeadlockError reports MySQL/MariaDB error 1213.
func isDeadlockError(err error) bool {
	if err == nil {
		return false
	}
	var driverErr *mysqldriver.MySQLError
	if errors.As(err, &driverErr) {
		return driverErr.Number == 1213
	}
	return strings.Contains(err.Error(), "1213")
}

// TestLeadErasureSurvivesAutomationStepMySQL keeps the automation-cancel-first
// property under the #337 lead lock: an erasure that waits on the lead row an
// automation step holds has already cancelled the lead's runs, so when the
// step then updates its run row the step, not the erasure, is the deadlock
// victim, and the erasure commits.
func TestLeadErasureSurvivesAutomationStepMySQL(t *testing.T) {
	db := openAutomationRepoDB(t, true, 4)
	repo := NewAutomationRepository(db)
	owner := seedAutomationUser(t, db, "survivor-owner", models.RoleSales)
	rule := seedAutomationRule(t, db, models.AutomationEntityLead)
	seed := seedLeadForErasure(t, db, owner, rule, false)
	now := time.Now().UTC()
	const stepOwner = "lead-erasure-step-owner"
	rows, err := repo.ClaimRun(seed.run.ID, models.AutomationRunPending, stepOwner, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	// The competitor holds the lead row FOR UPDATE, as an automation step does.
	step := db.Begin()
	require.NoError(t, step.Error)
	t.Cleanup(func() { _ = step.Rollback() })
	stepRepo := repo.WithTx(step)
	_, err = stepRepo.GetLeadForUpdate(seed.lead.ID)
	require.NoError(t, err)

	erased := make(chan error, 1)
	go func() { erased <- NewLeadRepository(db).Delete(seed.lead.ID) }()
	awaitLockOrderWait(t, db, regexp.MustCompile("(?s)^SELECT `id`,`status`,`customer_id` FROM `leads` WHERE .*FOR UPDATE$"))

	// By then the erasure has cancelled the run (uncommitted, read dirty).
	var dirty string
	require.NoError(t, db.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("SET SESSION TRANSACTION ISOLATION LEVEL READ UNCOMMITTED").Error; err != nil {
			return err
		}
		defer conn.Exec("SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ")
		return conn.Raw("SELECT status FROM automation_runs WHERE id = ?", seed.run.ID).Scan(&dirty).Error
	}))
	assert.Equal(t, models.AutomationRunCancelled, dirty, "the runs are cancelled before the erasure waits on the lead")

	// The step now advances its run: the run row is the erasure's, the lead
	// row the step's, and InnoDB rolls back the step, which modified nothing.
	_, err = stepRepo.AdvanceRun(seed.run.ID, 1, stepOwner,
		AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: 2}, now)
	require.Error(t, err, "the step is the deadlock victim")
	assert.True(t, isDeadlockError(err), "the step fails with error 1213, got %v", err)
	_ = step.Rollback()

	select {
	case err := <-erased:
		require.NoError(t, err, "the erasure commits")
		assert.False(t, isDeadlockError(err))
	case <-time.After(lockOrderRepoTimeout):
		t.Fatal("the erasure did not finish")
	}

	run, err := repo.GetRunByID(seed.run.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AutomationRunCancelled, run.Status)
	assert.Equal(t, models.AutomationCancelRecordDeleted, run.CancelReason)
	var lead models.Lead
	require.NoError(t, db.Unscoped().First(&lead, seed.lead.ID).Error)
	assert.True(t, lead.DeletedAt.Valid, "the lead is erased")
	assert.True(t, strings.HasSuffix(lead.Email, "@"+anonymizedEmailDomain))
	assert.Empty(t, lead.FirstName)
}

// TestLeadErasureHooksDefaultUnset proves the erasure hooks are instance-local:
// a repository built without the option, and its WithTx copies, carry none; a
// repository built with it carries them, its WithTx copies share them, and
// they run for that instance's erasures only.
func TestLeadErasureHooksDefaultUnset(t *testing.T) {
	db := openAutomationRepoDB(t, false, 1)
	owner := seedAutomationUser(t, db, "hooks-owner", models.RoleSales)
	rule := seedAutomationRule(t, db, models.AutomationEntityLead)

	plain := NewLeadRepository(db).(*leadRepository)
	assert.Nil(t, plain.erasureSeams, "no hooks without the option")
	assert.Nil(t, plain.WithTx(db).(*leadRepository).erasureSeams, "nor on a WithTx copy")

	var mu sync.Mutex
	var cancelled, locked []uint
	hooked := NewLeadRepository(db, WithLeadErasureHooks(LeadErasureHooks{
		AfterAutomationCancel: func(tx *gorm.DB, id uint) {
			require.NotNil(t, tx, "the hook receives the erasure unit's handle")
			mu.Lock()
			cancelled = append(cancelled, id)
			mu.Unlock()
		},
		AfterLeadLock: func(id uint) { mu.Lock(); locked = append(locked, id); mu.Unlock() },
	})).(*leadRepository)
	require.NotNil(t, hooked.erasureSeams)
	copied := hooked.WithTx(db).(*leadRepository)
	assert.Same(t, hooked.erasureSeams, copied.erasureSeams, "WithTx copies the hooks")

	first := seedLeadForErasure(t, db, owner, rule, false)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return hooked.WithTx(tx).Delete(first.lead.ID) }))
	mu.Lock()
	assert.Equal(t, []uint{first.lead.ID}, cancelled, "AfterAutomationCancel ran through the WithTx copy")
	assert.Equal(t, []uint{first.lead.ID}, locked, "AfterLeadLock ran through the WithTx copy")
	mu.Unlock()

	second := seedLeadForErasure(t, db, owner, rule, false)
	require.NoError(t, NewLeadRepository(db).Delete(second.lead.ID))
	mu.Lock()
	assert.Len(t, cancelled, 1, "another instance never runs these hooks")
	assert.Len(t, locked, 1)
	mu.Unlock()
}
