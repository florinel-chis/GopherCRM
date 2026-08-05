package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Deleting a person through a bulk endpoint has to mean exactly what deleting
// them one at a time means. It used to mean something else: bulkDelete issued a
// bare soft delete for every entity, so DELETE /api/v1/users/:id erased the
// person while POST /api/v1/users/bulk/delete merely hid them, leaving their
// name, email and API keys in the database and their address locked in the
// unique index for good. Two contradictory implementations of one operation is
// the bug; these tests hold the two paths to the same outcome.
//
// Tasks and tickets are the deliberate exception: they hold no personal data of
// their own, so their bulk delete stays a plain soft delete (see the last test).

func setupBulkErasureDB(t *testing.T) *gorm.DB {
	t.Helper()
	return setupLeadErasureDB(t)
}

func seedBulkUser(t *testing.T, db *gorm.DB, email string) *models.User {
	t.Helper()
	user := &models.User{
		Email:     email,
		Password:  "$2a$04$notarealhashbutlongenoughtolooklikeone",
		FirstName: "Bulk",
		LastName:  "Subject",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

// --- Users -------------------------------------------------------------------

func TestBulkDeleteUsersErasesInsteadOfHiding(t *testing.T) {
	db := setupBulkErasureDB(t)
	bulkRepo := repository.NewBulkRepository(db)

	user := seedBulkUser(t, db, "bulk-erase-me@example.com")
	require.NoError(t, db.Create(&models.APIKey{
		Name: "ci", KeyHash: "hash-bulk", Prefix: "gcrm_bulk", UserID: user.ID, IsActive: true,
	}).Error)
	require.NoError(t, db.Create(&models.RefreshToken{
		UserID: user.ID, TokenHash: "refresh-bulk", ExpiresAt: time.Now().Add(24 * time.Hour),
	}).Error)

	errs := bulkRepo.BulkDeleteUsers([]uint{user.ID})
	require.Empty(t, errs)

	assertColumnsFreeOf(t, db, "users", "bulk-erase-me@example.com", "Bulk", "Subject")

	erased := fetchErasedUser(t, db, user.ID)
	assert.True(t, erased.DeletedAt.Valid, "the row must remain, soft-deleted")
	assert.True(t, strings.HasSuffix(erased.Email, erasedEmailSuffix))
	assert.False(t, erased.IsActive)

	// The credential purge is part of erasing a user, and a bulk delete must not
	// leave keys behind that still authenticate as the erased account.
	var keys int64
	require.NoError(t, db.Unscoped().Model(&models.APIKey{}).Where("user_id = ?", user.ID).Count(&keys).Error)
	assert.Zero(t, keys, "API keys of a bulk-erased user must not survive")

	var tokens int64
	require.NoError(t, db.Unscoped().Model(&models.RefreshToken{}).Where("user_id = ?", user.ID).Count(&tokens).Error)
	assert.Zero(t, tokens, "refresh tokens of a bulk-erased user must not survive")
}

// The address must come free again. A bulk soft delete left it held by the
// unique index — which is not scoped to deleted_at — so the person could never
// sign up again.
func TestBulkDeletedUserEmailCanBeRegisteredAgain(t *testing.T) {
	db := setupBulkErasureDB(t)
	bulkRepo := repository.NewBulkRepository(db)

	user := seedBulkUser(t, db, "reusable-after-bulk@example.com")
	require.Empty(t, bulkRepo.BulkDeleteUsers([]uint{user.ID}))

	replacement := seedBulkUser(t, db, "reusable-after-bulk@example.com")
	assert.NotEqual(t, user.ID, replacement.ID)
}

// --- Leads -------------------------------------------------------------------

func TestBulkDeleteLeadsErasesAndFollowsTheConversionLink(t *testing.T) {
	db := setupBulkErasureDB(t)
	bulkRepo := repository.NewBulkRepository(db)

	owner := seedLeadOwner(t, db)
	plain := seedLead(t, db, owner.ID, "bulk-plain-lead@example.com")
	converted := seedLead(t, db, owner.ID, "bulk-converted-lead@example.com")
	customer := convertLead(t, db, converted)

	errs := bulkRepo.BulkDeleteLeads([]uint{plain.ID, converted.ID})
	require.Empty(t, errs)

	assertColumnsFreeOf(t, db, "leads", "bulk-plain-lead@example.com", "bulk-converted-lead@example.com", "Ingrid", "Vasilescu")
	// The converted lead's customer holds the same person and goes with it.
	assertColumnsFreeOf(t, db, "customers", "bulk-converted-lead@example.com", "Ingrid", "Vasilescu")

	assert.True(t, fetchErasedLead(t, db, plain.ID).DeletedAt.Valid)
	assert.True(t, fetchErasedCustomer(t, db, customer.ID).DeletedAt.Valid)
}

// --- Customers ---------------------------------------------------------------

func TestBulkDeleteCustomersErasesAndFollowsTheConversionLink(t *testing.T) {
	db := setupBulkErasureDB(t)
	bulkRepo := repository.NewBulkRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "bulk-customer-pair@example.com")
	customer := convertLead(t, db, lead)
	standalone := seedCustomer(t, db, "bulk-standalone-customer@example.com")

	errs := bulkRepo.BulkDeleteCustomers([]uint{customer.ID, standalone.ID})
	require.Empty(t, errs)

	assertColumnsFreeOf(t, db, "customers",
		"bulk-customer-pair@example.com", "bulk-standalone-customer@example.com", "Ingrid", "Vasilescu")
	assertColumnsFreeOf(t, db, "leads", "bulk-customer-pair@example.com", "Ingrid", "Vasilescu")

	assert.True(t, fetchErasedLead(t, db, lead.ID).DeletedAt.Valid,
		"the lead the bulk-deleted customer was converted from must be erased too")
}

// --- Per-item accounting -----------------------------------------------------

// The accounting the bulk paths already had must survive the switch to erasure:
// one statement (now one erasure) per ID, so partial success is genuine, and an
// ID that matched no live row is reported instead of being counted as deleted.
func TestBulkDeleteReportsIDsThatMatchedNoRow(t *testing.T) {
	db := setupBulkErasureDB(t)
	bulkRepo := repository.NewBulkRepository(db)

	owner := seedLeadOwner(t, db)
	user := seedBulkUser(t, db, "accounting-user@example.com")
	lead := seedLead(t, db, owner.ID, "accounting-lead@example.com")
	customer := seedCustomer(t, db, "accounting-customer@example.com")

	userErrs := bulkRepo.BulkDeleteUsers([]uint{user.ID, 999001})
	require.Len(t, userErrs, 1)
	assert.ErrorIs(t, userErrs[0], gorm.ErrRecordNotFound)
	assert.Contains(t, userErrs[0].Error(), "999001")

	leadErrs := bulkRepo.BulkDeleteLeads([]uint{lead.ID, 999002})
	require.Len(t, leadErrs, 1)
	assert.ErrorIs(t, leadErrs[0], gorm.ErrRecordNotFound)

	customerErrs := bulkRepo.BulkDeleteCustomers([]uint{customer.ID, 999003})
	require.Len(t, customerErrs, 1)
	assert.ErrorIs(t, customerErrs[0], gorm.ErrRecordNotFound)

	// The IDs that did exist were still erased: one bad ID must not take the
	// rest of the batch with it.
	assertColumnsFreeOf(t, db, "users", "accounting-user@example.com")
	assertColumnsFreeOf(t, db, "leads", "accounting-lead@example.com")
	assertColumnsFreeOf(t, db, "customers", "accounting-customer@example.com")
}

// An already soft-deleted row is still "no live row" and is still reported.
func TestBulkDeleteReportsAnAlreadyDeletedRow(t *testing.T) {
	db := setupBulkErasureDB(t)
	bulkRepo := repository.NewBulkRepository(db)

	user := seedBulkUser(t, db, "already-gone@example.com")
	require.Empty(t, bulkRepo.BulkDeleteUsers([]uint{user.ID}))

	errs := bulkRepo.BulkDeleteUsers([]uint{user.ID})
	require.Len(t, errs, 1)
	assert.ErrorIs(t, errs[0], gorm.ErrRecordNotFound)
}

// Two leads converted into the same customer: erasing the first cascades
// through the customer into the second, so by the time the batch reaches the
// second ID there is no live row left. That is the batch's own doing and the
// person is gone, which is what was asked for, so it must count as a success
// and not be reported as a missing row.
func TestBulkDeleteCountsALeadErasedByASiblingCascadeAsASuccess(t *testing.T) {
	db := setupBulkErasureDB(t)
	bulkRepo := repository.NewBulkRepository(db)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	first := seedLead(t, db, owner.ID, "sibling-1@example.com")
	customer := convertLead(t, db, first)
	second := seedLead(t, db, owner.ID, "sibling-2@example.com")
	require.NoError(t, leadRepo.ConvertToCustomer(second.ID, customer.ID))

	errs := bulkRepo.BulkDeleteLeads([]uint{first.ID, second.ID})
	assert.Empty(t, errs, "a row the batch itself erased must not be reported as missing")

	assertColumnsFreeOf(t, db, "leads", "sibling-1@example.com", "sibling-2@example.com", "Ingrid")
	assertColumnsFreeOf(t, db, "customers", "sibling-1@example.com", "Ingrid")
}

// --- Entities that hold no personal data -------------------------------------

// Tasks and tickets describe work, not people: a title, a description and
// foreign keys to the user, lead or customer involved, each of which is erased
// through its own row. Anonymising a task would destroy business history for no
// privacy gain, so their bulk delete stays a plain soft delete.
func TestBulkDeleteTasksAndTicketsRemainPlainSoftDeletes(t *testing.T) {
	db := setupBulkErasureDB(t)
	bulkRepo := repository.NewBulkRepository(db)

	owner := seedLeadOwner(t, db)
	customer := seedCustomer(t, db, "ticket-customer@example.com")

	task := &models.Task{
		Title:        "Prepare the quarterly report",
		Description:  "Numbers for the board pack",
		Status:       models.TaskStatusPending,
		Priority:     models.TaskPriorityHigh,
		AssignedToID: owner.ID,
	}
	require.NoError(t, db.Create(task).Error)

	ticket := &models.Ticket{
		Title:       "Invoice PDF will not download",
		Description: "Reported over the phone",
		Status:      models.TicketStatusOpen,
		Priority:    models.TicketPriorityMedium,
		CustomerID:  customer.ID,
	}
	require.NoError(t, db.Create(ticket).Error)

	require.Empty(t, bulkRepo.BulkDeleteTasks([]uint{task.ID}))
	require.Empty(t, bulkRepo.BulkDeleteTickets([]uint{ticket.ID}))

	var erasedTask models.Task
	require.NoError(t, db.Unscoped().First(&erasedTask, task.ID).Error)
	assert.True(t, erasedTask.DeletedAt.Valid)
	assert.Equal(t, "Prepare the quarterly report", erasedTask.Title,
		"a task holds no personal data and must not be anonymised")

	var erasedTicket models.Ticket
	require.NoError(t, db.Unscoped().First(&erasedTicket, ticket.ID).Error)
	assert.True(t, erasedTicket.DeletedAt.Valid)
	assert.Equal(t, "Invoice PDF will not download", erasedTicket.Title)

	// The customer the ticket points at is untouched by the ticket's deletion —
	// they are erased through the customers table, not through their tickets.
	var survivor models.Customer
	require.NoError(t, db.First(&survivor, customer.ID).Error)
	assert.Equal(t, "ticket-customer@example.com", survivor.Email)
}
