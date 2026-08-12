package integration

import (
	"fmt"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Isolation of a single ITEM of a bulk erasure.
//
// A bulk erasure is not one operation, it is N of them, and it deliberately
// keeps going after one fails: the response reports which IDs could not be
// erased and the rest of the batch is still carried out. That per-item error
// accounting is only honest if each item is genuinely its own unit of work.
//
// It stops being one as soon as the batch runs inside a caller's transaction —
// which is exactly how the service layer runs it (bulkOperationService wraps the
// whole batch in TransactionManager.WithTransaction and hands the repository
// WithTx(tx)). If an item's erasure merely JOINS that shared transaction, a
// failure half way through the item leaves the statements it already issued
// sitting on the shared transaction, and the batch — which reports the item as a
// failure and returns nil — commits them. The person is then neither intact nor
// erased: a live, still-listed account with a placeholder address, no name, an
// unusable password and no credentials, and an operator who was told the
// deletion did not happen.
//
// The tests below assert on the DATABASE STATE AFTER THE COMMIT, because that is
// the only place the difference shows. A count of returned errors is identical
// either way — the broken implementation reports precisely the same one failure
// while silently persisting the half-erasure.

// --- Defect A: an item's failure must not be committed by the batch -----------

func TestBulkUserErasureInACallerTransactionCommitsNoHalfErasure(t *testing.T) {
	db := setupBulkErasureDB(t)

	first := seedUser(t, db, "isolated-item-1@example.com")
	second := seedUser(t, db, "isolated-item-2@example.com")
	third := seedUser(t, db, "isolated-item-3@example.com")
	require.NoError(t, db.Create(&models.APIKey{
		Name: "ci", KeyHash: "hash-isolated-item", Prefix: "gcrm_iso", UserID: second.ID, IsActive: true,
	}).Error)

	// Only the middle item's soft delete fails — after its columns have been
	// scrubbed and its credentials purged.
	failure := failDeletesOn(t, db, "users", func(call int) bool { return call == 2 })

	// This mirrors the service layer exactly: one transaction around the whole
	// batch, per-item failures collected and reported, and the transaction
	// COMMITTED because the batch itself did not fail.
	var errs []error
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		errs = repository.NewBulkRepository(db).WithTx(tx).
			BulkDeleteUsers([]uint{first.ID, second.ID, third.ID})
		return nil
	}))

	require.Len(t, errs, 1, "exactly the failing item must be reported")
	assert.Contains(t, errs[0].Error(), fmt.Sprintf("ID %d", second.ID))
	require.Equal(t, 3, failure.Calls(), "every item must have reached its soft delete")

	// The whole point: the reported failure must have left NOTHING behind.
	assertUserIsUntouched(t, db, second.ID, "isolated-item-2@example.com")

	var keys int64
	require.NoError(t, db.Unscoped().Model(&models.APIKey{}).Where("user_id = ?", second.ID).Count(&keys).Error)
	assert.Equal(t, int64(1), keys,
		"the failing item's credential purge must have been rolled back, not committed with the batch")

	// ...while the items around it are fully erased, so isolating the failure did
	// not cost the batch the work it did succeed at.
	assert.True(t, fetchErasedUser(t, db, first.ID).DeletedAt.Valid)
	assert.True(t, fetchErasedUser(t, db, third.ID).DeletedAt.Valid)
	assertColumnsFreeOf(t, db, "users", "isolated-item-1@example.com", "isolated-item-3@example.com")
}

// The same property for the widest item there is: a lead whose erasure cascades
// into its customer. Two tables, several statements, one shared transaction —
// and the failing item must still come back whole.
func TestBulkLeadCascadeInACallerTransactionCommitsNoHalfErasure(t *testing.T) {
	db := setupBulkErasureDB(t)

	owner := seedLeadOwner(t, db)
	survivor := seedLead(t, db, owner.ID, "isolated-cascade-ok@example.com")
	doomed := seedLead(t, db, owner.ID, "isolated-cascade-fail@example.com")
	customer := convertLead(t, db, doomed)

	// The lead half of the doomed item is erased first; failing the customer
	// half leaves the lead scrubbed on the shared transaction.
	failure := failDeletesOn(t, db, "customers", nil)

	var errs []error
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		errs = repository.NewBulkRepository(db).WithTx(tx).
			BulkDeleteLeads([]uint{survivor.ID, doomed.ID})
		return nil
	}))

	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), fmt.Sprintf("ID %d", doomed.ID))
	require.Equal(t, 1, failure.Calls())

	var reloaded models.Lead
	require.NoError(t, db.First(&reloaded, doomed.ID).Error,
		"the failing item's lead must still be live — a committed half-erasure is unrecoverable")
	assert.False(t, reloaded.DeletedAt.Valid)
	assert.Equal(t, "isolated-cascade-fail@example.com", reloaded.Email)
	assert.Equal(t, "Ingrid", reloaded.FirstName)
	assert.Equal(t, "Met Ingrid at the Bucharest expo; call her mobile after 18:00.", reloaded.Notes)

	var reloadedCustomer models.Customer
	require.NoError(t, db.First(&reloadedCustomer, customer.ID).Error)
	assert.False(t, reloadedCustomer.DeletedAt.Valid)
	assert.Equal(t, "isolated-cascade-fail@example.com", reloadedCustomer.Email)

	// The healthy item of the same batch survived the neighbour's rollback.
	assert.True(t, fetchErasedLead(t, db, survivor.ID).DeletedAt.Valid)
	assertColumnsFreeOf(t, db, "leads", "isolated-cascade-ok@example.com")
}

// --- Defect B: "already erased" must mean "erasure committed" -----------------

// The cascade remembers the rows it has erased so that a batch does not chase
// the same person round the lead/customer link forever, and bulkErase treats a
// remembered row as a success instead of reporting it as missing. That makes the
// memory a claim about the database, and the claim has to be true: a row that is
// remembered but was never actually erased is reported to the operator as erased
// while every one of the person's fields is still sitting there in a live row.
//
// Three leads converted into ONE customer is the shape that exposes it —
// leads.customer_id carries no unique constraint and the cascade explicitly
// handles several leads per customer. Erasing the first cascades through the
// customer into the other two, so a failure on the LAST of them rolls back an
// attempt during which the middle one had already been marked.
func TestBulkLeadErasureNeverReportsAnIntactLeadAsErased(t *testing.T) {
	db := setupBulkErasureDB(t)
	bulkRepo := repository.NewBulkRepository(db)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	first := seedLead(t, db, owner.ID, "shared-customer-1@example.com")
	customer := convertLead(t, db, first)
	second := seedLead(t, db, owner.ID, "shared-customer-2@example.com")
	require.NoError(t, leadRepo.ConvertToCustomer(second.ID, customer.ID))
	third := seedLead(t, db, owner.ID, "shared-customer-3@example.com")
	require.NoError(t, leadRepo.ConvertToCustomer(third.ID, customer.ID))

	// The first item's cascade erases first, then second, then third; failing
	// the third rolls the attempt back with the second already marked.
	failure := failDeletesOn(t, db, "leads", func(call int) bool { return call == 3 })

	errs := bulkRepo.BulkDeleteLeads([]uint{first.ID, second.ID, third.ID})

	require.Len(t, errs, 1, "only the item whose cascade failed may be reported")
	assert.Contains(t, errs[0].Error(), fmt.Sprintf("ID %d", first.ID))
	require.GreaterOrEqual(t, failure.Calls(), 3, "the injected failure must actually have fired")

	// The invariant. Every ID the batch did NOT report as a failure was reported
	// to the operator as erased, so none of them may still describe the person.
	// The second lead is the one the leaked mark hides: it is counted as a
	// success without a single statement ever having touched it.
	secondRow := fetchErasedLead(t, db, second.ID)
	assert.True(t, secondRow.DeletedAt.Valid,
		"a lead counted as erased must actually be soft-deleted, not merely remembered")
	assert.NotEqual(t, "shared-customer-2@example.com", secondRow.Email,
		"a lead counted as erased must not still hold the person's address")
	assert.Empty(t, secondRow.FirstName)
	assert.Empty(t, secondRow.LastName)
	assert.Empty(t, secondRow.Phone)
	assert.Empty(t, secondRow.Notes)
	assert.Empty(t, secondRow.ExternalID)

	thirdRow := fetchErasedLead(t, db, third.ID)
	assert.True(t, thirdRow.DeletedAt.Valid)
	assert.NotEqual(t, "shared-customer-3@example.com", thirdRow.Email)

	assertColumnsFreeOf(t, db, "leads",
		"shared-customer-2@example.com", "shared-customer-3@example.com",
		"Ingrid", "Vasilescu", "legacy-crm-contact-99182")
}

// The mirror image, entered from the customer end: one customer, several leads,
// and a failure deep in the cascade. The customer is marked before its leads are
// walked, so a leaked mark here would let a LATER item of the batch skip the
// customer entirely and report the person as erased.
func TestBulkCustomerErasureNeverReportsAnIntactCustomerAsErased(t *testing.T) {
	db := setupBulkErasureDB(t)
	bulkRepo := repository.NewBulkRepository(db)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "customer-mark-1@example.com")
	shared := convertLead(t, db, lead)
	sibling := seedLead(t, db, owner.ID, "customer-mark-2@example.com")
	require.NoError(t, leadRepo.ConvertToCustomer(sibling.ID, shared.ID))

	standalone := seedCustomer(t, db, "customer-mark-standalone@example.com")

	// Fail the second lead of the shared customer's cascade.
	failure := failDeletesOn(t, db, "leads", func(call int) bool { return call == 2 })

	errs := bulkRepo.BulkDeleteCustomers([]uint{shared.ID, standalone.ID})

	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), fmt.Sprintf("ID %d", shared.ID))
	require.GreaterOrEqual(t, failure.Calls(), 2)

	// The failed item rolled back completely: both leads and the customer are
	// untouched and live, and nothing about them was reported as done.
	var reloaded models.Customer
	require.NoError(t, db.First(&reloaded, shared.ID).Error)
	assert.False(t, reloaded.DeletedAt.Valid)
	assert.Equal(t, "customer-mark-1@example.com", reloaded.Email)

	for _, id := range []uint{lead.ID, sibling.ID} {
		var reloadedLead models.Lead
		require.NoError(t, db.First(&reloadedLead, id).Error,
			"every lead of the failed cascade must still be live")
		assert.Equal(t, "Ingrid", reloadedLead.FirstName)
		assert.Equal(t, "Vasilescu", reloadedLead.LastName)
	}

	// ...and the healthy item of the batch was still erased.
	assert.True(t, fetchErasedCustomer(t, db, standalone.ID).DeletedAt.Valid)

	// A retry must now work, because nothing was left remembered as erased.
	require.Empty(t, bulkRepo.BulkDeleteCustomers([]uint{shared.ID}))
	assertColumnsFreeOf(t, db, "leads", "customer-mark-1@example.com", "customer-mark-2@example.com", "Ingrid")
	assertColumnsFreeOf(t, db, "customers", "customer-mark-1@example.com")
}
