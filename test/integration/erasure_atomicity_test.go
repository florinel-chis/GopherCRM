package integration

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Atomicity of the erasure.
//
// An erasure is several statements: overwrite the personal-data columns, purge
// whatever must not outlive the person, soft-delete the row. erasure.go promises
// they happen as ONE unit — "the scrub and the soft delete cannot be separated
// by a failure" — and that promise is the whole reason the code wraps them in a
// transaction rather than issuing them in sequence.
//
// A promise like that is only worth what a test makes of it. Every happy-path
// test in this suite passes just as well against an implementation that runs the
// statements one after another with no transaction at all, because on the happy
// path there is nothing to separate. What distinguishes the two implementations
// is a failure AFTER the scrub, and the only way to pin the difference down is
// to cause one.
//
// The tests below do exactly that: they make the final soft delete fail, and
// then assert that the row is still there, still live, and still holding the
// person's ORIGINAL data. Delete the transaction from runErasureUnit (which
// eraseGraph runs every erasure in) and every one of them goes red, because the half-erasure the transaction prevents becomes
// visible: an anonymised, still-live, still-listed account whose credentials
// were purged and whose data is gone, with nothing to say the deletion failed.

// deleteFailure is a controllable, table-scoped failure injected into the DELETE
// path. It is deliberately placed on the LAST statement of an erasure so that
// everything before it — the column scrub, and for a user the credential purge —
// has already been issued and can only be undone by a rollback.
type deleteFailure struct {
	mu    sync.Mutex
	err   error
	calls int
	when  func(call int) bool
}

// Calls reports how many deletes against the target table were intercepted, so a
// test can prove the injection actually fired rather than passing because the
// erasure never got that far.
func (f *deleteFailure) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// failDeletesOn makes soft deletes against one table fail, from inside GORM, at
// the point the erasure issues them.
//
// A callback is used rather than a broken schema because it is surgical: the
// scrub UPDATE, the credential purge and the surrounding transaction all still
// work normally, and only the very last statement of the erasure fails. That
// isolates the property under test — what happens to the work already done when
// a later step of the same erasure fails.
//
// when is called with the 1-based number of the intercepted delete and decides
// whether that one fails; nil fails all of them.
func failDeletesOn(t *testing.T, db *gorm.DB, table string, when func(call int) bool) *deleteFailure {
	t.Helper()

	failure := &deleteFailure{
		err:  errors.New("simulated database failure while soft-deleting " + table),
		when: when,
	}

	const callbackName = "erasure_test:fail_delete"
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
		if statementTable(tx) != table {
			return
		}
		failure.mu.Lock()
		failure.calls++
		call := failure.calls
		failure.mu.Unlock()

		if failure.when != nil && !failure.when(call) {
			return
		}
		_ = tx.AddError(failure.err)
	}))
	t.Cleanup(func() {
		require.NoError(t, db.Callback().Delete().Remove(callbackName))
	})

	return failure
}

func statementTable(tx *gorm.DB) string {
	if tx.Statement == nil {
		return ""
	}
	if tx.Statement.Table != "" {
		return tx.Statement.Table
	}
	if tx.Statement.Schema != nil {
		return tx.Statement.Schema.Table
	}
	return ""
}

// assertUserIsUntouched re-reads the user through an ORDINARY (scoped) query and
// checks that the person is still fully there. Scoped on purpose: if the row had
// been soft-deleted this would not find it at all.
func assertUserIsUntouched(t *testing.T, db *gorm.DB, id uint, email string) {
	t.Helper()

	var user models.User
	require.NoError(t, db.First(&user, id).Error,
		"the row must still be live — a failed erasure that soft-deleted anyway is a silent, irreversible deletion")

	assert.False(t, user.DeletedAt.Valid, "the failed erasure must not have soft-deleted the row")
	assert.Equal(t, email, user.Email, "the address must be back, not a placeholder")
	assert.Equal(t, "Erasure", user.FirstName, "the scrub must have been rolled back")
	assert.Equal(t, "Subject", user.LastName)
	assert.True(t, user.IsActive, "the account must not have been left deactivated by a failed erasure")
	assert.NotNil(t, user.LastLoginAt, "the login bookkeeping must be back too")
	assert.Equal(t, 3, user.FailedLoginAttempts)
	assert.NotEqual(t, "!erased", user.Password, "the password hash must have been restored")
}

// --- Users -------------------------------------------------------------------

// The user erasure is the widest one: it scrubs seven columns, purges two
// credential tables and then soft-deletes. Failing the last step must undo all
// of it. Without the transaction the account would be left anonymised, live and
// locked out, its credentials destroyed, and the caller would be told the
// deletion failed — an unrecoverable state produced by an operation that
// reported an error.
func TestUserErasureRollsBackEverythingWhenTheSoftDeleteFails(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "atomic-user@example.com")
	require.NoError(t, db.Create(&models.APIKey{
		Name: "ci", KeyHash: "hash-must-survive", Prefix: "gcrm_abc", UserID: user.ID, IsActive: true,
	}).Error)
	require.NoError(t, db.Create(&models.RefreshToken{
		UserID: user.ID, TokenHash: "refresh-must-survive", ExpiresAt: time.Now().Add(24 * time.Hour),
	}).Error)

	failure := failDeletesOn(t, db, "users", nil)

	err := userRepo.Delete(user.ID)
	require.Error(t, err, "an erasure whose soft delete fails must not report success")
	require.Equal(t, 1, failure.Calls(), "the failure must have been injected into the erasure's own delete")

	assertUserIsUntouched(t, db, user.ID, "atomic-user@example.com")

	// The credential purge ran BEFORE the failing delete, so it is the clearest
	// evidence of a rollback: without one, the keys would be gone for good while
	// the account they belonged to is still live and unerased.
	var keys int64
	require.NoError(t, db.Model(&models.APIKey{}).Where("user_id = ?", user.ID).Count(&keys).Error)
	assert.Equal(t, int64(1), keys, "the credential purge must have been rolled back with the rest")

	var tokens int64
	require.NoError(t, db.Model(&models.RefreshToken{}).Where("user_id = ?", user.ID).Count(&tokens).Error)
	assert.Equal(t, int64(1), tokens, "the refresh token purge must have been rolled back with the rest")
}

// And the person must still be erasable afterwards. A rollback that left the row
// scrubbed-but-live would make the retry look like a second erasure of an
// already-anonymous shell — the data would be gone with no record of it ever
// having been erased.
func TestAUserErasureCanBeRetriedAfterAFailedAttempt(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "retry-after-failure@example.com")

	failure := failDeletesOn(t, db, "users", func(call int) bool { return call == 1 })

	require.Error(t, userRepo.Delete(user.ID))
	assertUserIsUntouched(t, db, user.ID, "retry-after-failure@example.com")

	require.NoError(t, userRepo.Delete(user.ID), "the second attempt must succeed")
	require.Equal(t, 2, failure.Calls())

	assertColumnsFreeOf(t, db, "users", "retry-after-failure@example.com", "Erasure", "Subject")
	assert.True(t, fetchErasedUser(t, db, user.ID).DeletedAt.Valid)
}

// --- Customers ---------------------------------------------------------------

// The purest form of the property: a customer erasure is a scrub followed by a
// soft delete with nothing in between, so this test isolates the gap the
// transaction exists to close. If the two statements were issued directly, the
// customer would be sitting in the table right now with a deleted-<random>
// address, no name, no phone and no notes, and still visible in every customer
// list — data destroyed, person not deleted, caller told it failed.
func TestCustomerErasureRollsBackWhenTheSoftDeleteFails(t *testing.T) {
	db := setupErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	customer := seedCustomer(t, db, "atomic-customer@example.com")
	failure := failDeletesOn(t, db, "customers", nil)

	err := customerRepo.Delete(customer.ID)
	require.Error(t, err, "an erasure whose soft delete fails must not report success")
	require.Equal(t, 1, failure.Calls())

	var reloaded models.Customer
	require.NoError(t, db.First(&reloaded, customer.ID).Error, "the row must still be live")
	assert.False(t, reloaded.DeletedAt.Valid)
	assert.Equal(t, "atomic-customer@example.com", reloaded.Email)
	assert.Equal(t, "Erasure", reloaded.FirstName)
	assert.Equal(t, "Subject", reloaded.LastName)
	assert.Equal(t, "+40 721 000 111", reloaded.Phone)
	assert.Equal(t, "Subject Industries", reloaded.Company)
	assert.Equal(t, "12 Privacy Lane", reloaded.Address)
	assert.Equal(t, "Prefers to be called on the mobile number above.", reloaded.Notes)
}

// --- Leads and the conversion link -------------------------------------------

func TestLeadErasureRollsBackWhenTheSoftDeleteFails(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "atomic-lead@example.com")

	failure := failDeletesOn(t, db, "leads", nil)

	require.Error(t, leadRepo.Delete(lead.ID))
	require.Equal(t, 1, failure.Calls())

	var reloaded models.Lead
	require.NoError(t, db.First(&reloaded, lead.ID).Error)
	assert.False(t, reloaded.DeletedAt.Valid)
	assert.Equal(t, "atomic-lead@example.com", reloaded.Email)
	assert.Equal(t, "Ingrid", reloaded.FirstName)
	assert.Equal(t, "Vasilescu", reloaded.LastName)
	assert.Equal(t, "+40 722 333 444", reloaded.Phone)
	assert.Equal(t, "legacy-crm-contact-99182", reloaded.ExternalID)
	assert.Equal(t, "Met Ingrid at the Bucharest expo; call her mobile after 18:00.", reloaded.Notes)
}

// A converted lead and its customer are erased in one cascade, lead first. This
// fails the SECOND half — the customer's soft delete — after the lead has
// already been fully erased. The whole cascade has to come back, or the person
// is left half-erased across two tables: no trace of them on the lead, a
// complete copy of them on the customer, and an error returned to the operator
// who asked for the erasure.
func TestConvertedPairErasureRollsBackWhenTheSecondHalfFails(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "atomic-cascade@example.com")
	customer := convertLead(t, db, lead)

	failure := failDeletesOn(t, db, "customers", nil)

	require.Error(t, leadRepo.Delete(lead.ID),
		"a cascade that cannot finish must not report success")
	require.Equal(t, 1, failure.Calls())

	var reloadedLead models.Lead
	require.NoError(t, db.First(&reloadedLead, lead.ID).Error,
		"the lead half must be back — it was erased before the failure")
	assert.False(t, reloadedLead.DeletedAt.Valid)
	assert.Equal(t, "atomic-cascade@example.com", reloadedLead.Email)
	assert.Equal(t, "Ingrid", reloadedLead.FirstName)
	assert.Equal(t, "Met Ingrid at the Bucharest expo; call her mobile after 18:00.", reloadedLead.Notes)

	var reloadedCustomer models.Customer
	require.NoError(t, db.First(&reloadedCustomer, customer.ID).Error)
	assert.False(t, reloadedCustomer.DeletedAt.Valid)
	assert.Equal(t, "atomic-cascade@example.com", reloadedCustomer.Email)
}

// The same cascade entered from the customer end, failing on the lead half.
func TestCustomerFirstCascadeRollsBackWhenTheLeadHalfFails(t *testing.T) {
	db := setupLeadErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "atomic-cascade-2@example.com")
	customer := convertLead(t, db, lead)

	failure := failDeletesOn(t, db, "leads", nil)

	require.Error(t, customerRepo.Delete(customer.ID))
	require.Equal(t, 1, failure.Calls())

	var reloadedCustomer models.Customer
	require.NoError(t, db.First(&reloadedCustomer, customer.ID).Error,
		"the customer half must be back — it was erased before the failure")
	assert.False(t, reloadedCustomer.DeletedAt.Valid)
	assert.Equal(t, "atomic-cascade-2@example.com", reloadedCustomer.Email)
	assert.Equal(t, "Ingrid", reloadedCustomer.FirstName)

	var reloadedLead models.Lead
	require.NoError(t, db.First(&reloadedLead, lead.ID).Error)
	assert.False(t, reloadedLead.DeletedAt.Valid)
	assert.Equal(t, "Ingrid", reloadedLead.FirstName)
}
