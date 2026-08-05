package integration

import (
	"errors"
	"strings"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These tests pin down the GDPR Article 17 erasure of a LEAD, and of the
// lead/customer pair a conversion creates.
//
// A lead holds the same categories of personal data as a user or a customer —
// name, email, phone, employer, job title, free-text notes — about someone who
// typically never signed up for anything. Deleting one used to be a bare soft
// delete: the row vanished from the lists and kept every one of those fields.
//
// Conversion makes it worse. leadService.ConvertToCustomer COPIES the lead's
// email, names, phone and company into a new customer row and leaves the lead
// standing, linked by leads.customer_id. Erasing one side of that pair on its
// own therefore erases nobody: the same person is still fully described by the
// other side. Erasure has to follow the link, in both directions, atomically.

func setupLeadErasureDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupDB(t)
	require.NoError(t, db.AutoMigrate(
		&models.User{},
		&models.Lead{},
		&models.Customer{},
		&models.APIKey{},
		&models.RefreshToken{},
		&models.Ticket{},
		&models.Task{},
	))
	return db
}

// seedLeadOwner creates the sales user every lead has to belong to.
func seedLeadOwner(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	owner := &models.User{
		Email:     "lead-owner@example.com",
		Password:  "hashed",
		FirstName: "Sales",
		LastName:  "Owner",
		Role:      models.RoleSales,
		IsActive:  true,
	}
	require.NoError(t, db.Create(owner).Error)
	return owner
}

func seedLead(t *testing.T, db *gorm.DB, ownerID uint, email string) *models.Lead {
	t.Helper()
	lead := &models.Lead{
		FirstName:      "Ingrid",
		LastName:       "Vasilescu",
		Email:          email,
		Phone:          "+40 722 333 444",
		Company:        "Vasilescu Consulting",
		Position:       "Managing Partner",
		Source:         "trade show",
		Status:         models.LeadStatusQualified,
		Classification: models.LeadClassificationHotLead,
		ExternalID:     "hubspot-contact-99182",
		Notes:          "Met Ingrid at the Bucharest expo; call her mobile after 18:00.",
		OwnerID:        ownerID,
	}
	require.NoError(t, db.Create(lead).Error)
	return lead
}

// leadPersonalData is every string the seeded lead is identifiable by. The
// erasure has to leave none of it anywhere in the table.
func leadPersonalData(lead *models.Lead) []string {
	return []string{
		lead.Email, lead.FirstName, lead.LastName, lead.Phone,
		lead.Company, lead.Position, lead.ExternalID, lead.Notes,
	}
}

func fetchErasedLead(t *testing.T, db *gorm.DB, id uint) models.Lead {
	t.Helper()
	var lead models.Lead
	require.NoError(t, db.Unscoped().First(&lead, id).Error)
	return lead
}

// convertLead runs a real conversion through the service, which is what creates
// the lead/customer pair and the leads.customer_id link the cascade follows.
func convertLead(t *testing.T, db *gorm.DB, lead *models.Lead) *models.Customer {
	t.Helper()
	leadService := service.NewLeadService(
		repository.NewLeadRepository(db),
		repository.NewCustomerRepository(db),
		utils.NewTransactionManager(db),
	)
	customer, err := leadService.ConvertToCustomer(lead.ID, &models.Customer{})
	require.NoError(t, err)

	// The link the erasure depends on. If this ever stops being set, the
	// cascade has nothing to follow and the tests below would pass vacuously.
	linked, err := repository.NewLeadRepository(db).GetByID(lead.ID)
	require.NoError(t, err)
	require.NotNil(t, linked.CustomerID, "conversion must record the customer on the lead")
	require.Equal(t, customer.ID, *linked.CustomerID)

	return customer
}

// --- Plain lead erasure ------------------------------------------------------

func TestLeadErasureRemovesPersonalDataFromTheTable(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "ingrid@example.com")

	require.NoError(t, leadRepo.Delete(lead.ID))

	// Nothing anywhere in the leads table may still carry any of it, including
	// the soft-deleted rows an ordinary query would hide.
	assertColumnsFreeOf(t, db, "leads", leadPersonalData(lead)...)
}

func TestLeadErasureKeepsTheRowSoftDeletedWithAPlaceholderEmail(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "placeholder@example.com")

	require.NoError(t, leadRepo.Delete(lead.ID))

	// The row survives so that tasks and the conversion history keep resolving.
	erased := fetchErasedLead(t, db, lead.ID)
	assert.True(t, erased.DeletedAt.Valid, "the row must remain, soft-deleted")
	assert.Equal(t, lead.ID, erased.ID)

	assert.True(t, strings.HasSuffix(erased.Email, erasedEmailSuffix),
		"expected an %s placeholder, got %q", erasedEmailSuffix, erased.Email)
	assert.NotContains(t, erased.Email, "placeholder@",
		"the placeholder must not be derived from the original address")

	assert.Empty(t, erased.FirstName)
	assert.Empty(t, erased.LastName)
	assert.Empty(t, erased.Phone)
	assert.Empty(t, erased.Company)
	assert.Empty(t, erased.Position)
	assert.Empty(t, erased.Notes)
	assert.Empty(t, erased.ExternalID)

	// A scoped lookup must not resurrect it.
	_, err := leadRepo.GetByID(lead.ID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// The erasure is about the person, not about the business record. What the
// enquiry was and who handled it are not personal data of the data subject and
// have to survive, or the erasure would be quietly destroying company records.
func TestLeadErasureKeepsTheNonPersonalBusinessFields(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "business-fields@example.com")

	require.NoError(t, leadRepo.Delete(lead.ID))

	erased := fetchErasedLead(t, db, lead.ID)
	assert.Equal(t, "trade show", erased.Source)
	assert.Equal(t, models.LeadStatusQualified, erased.Status)
	assert.Equal(t, models.LeadClassificationHotLead, erased.Classification)
	assert.Equal(t, owner.ID, erased.OwnerID, "the owning salesperson is not the data subject")
}

// Lead search matches on first_name, last_name, email, company, phone and
// notes. A soft delete alone already hides the row from the scoped search, so
// the interesting half of this test is the second query: the same search
// INCLUDING soft-deleted rows must match nothing either, because the data those
// terms would match is no longer in the table at all.
func TestErasedLeadCannotBeMatchedByAnyOfItsFormerSearchTerms(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "searchable@example.com")

	require.NoError(t, leadRepo.Delete(lead.ID))

	for _, needle := range []string{"Ingrid", "Vasilescu", "searchable@example.com", "+40 722 333 444", "Bucharest expo"} {
		found, err := leadRepo.Search(needle, 0, 10, "", "")
		require.NoError(t, err)
		assert.Empty(t, found, "searching for %q must not turn the erased lead up", needle)

		pattern := "%" + needle + "%"
		var retained int64
		require.NoError(t, db.Unscoped().Model(&models.Lead{}).Where(
			"first_name LIKE ? OR last_name LIKE ? OR email LIKE ? OR company LIKE ? OR phone LIKE ? OR notes LIKE ?",
			pattern, pattern, pattern, pattern, pattern, pattern,
		).Count(&retained).Error)
		assert.Zero(t, retained, "%q still matches a retained row: the data was hidden, not erased", needle)
	}
}

// --- The conversion link, lead first ----------------------------------------

func TestErasingAConvertedLeadAlsoErasesTheCustomerItBecame(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "converted-lead@example.com")
	customer := convertLead(t, db, lead)

	require.NoError(t, leadRepo.Delete(lead.ID))

	// Both halves of the same person, or the erasure was cosmetic.
	assertColumnsFreeOf(t, db, "leads", leadPersonalData(lead)...)
	assertColumnsFreeOf(t, db, "customers", leadPersonalData(lead)...)

	erasedCustomer := fetchErasedCustomer(t, db, customer.ID)
	assert.True(t, erasedCustomer.DeletedAt.Valid, "the customer must be soft-deleted too")
	assert.True(t, strings.HasSuffix(erasedCustomer.Email, erasedEmailSuffix))
	assert.Empty(t, erasedCustomer.FirstName)
	assert.Empty(t, erasedCustomer.Phone)
}

// --- The conversion link, customer first -------------------------------------

// This is the direction that mattered most: the customer is the record a
// support agent deletes on request, and the lead it came from is invisible from
// there while holding a complete copy of the person.
func TestErasingAConvertedCustomerAlsoErasesTheOriginatingLead(t *testing.T) {
	db := setupLeadErasureDB(t)
	customerRepo := repository.NewCustomerRepositoryWithLeadErasure(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "converted-customer@example.com")
	customer := convertLead(t, db, lead)

	require.NoError(t, customerRepo.Delete(customer.ID))

	assertColumnsFreeOf(t, db, "customers", leadPersonalData(lead)...)
	assertColumnsFreeOf(t, db, "leads", leadPersonalData(lead)...)

	erasedLead := fetchErasedLead(t, db, lead.ID)
	assert.True(t, erasedLead.DeletedAt.Valid, "the originating lead must be soft-deleted too")
	assert.True(t, strings.HasSuffix(erasedLead.Email, erasedEmailSuffix))
	assert.Empty(t, erasedLead.Notes)
}

// The same thing through the service the HTTP handler actually calls, wired the
// way cmd/main.go wires it.
func TestCustomerServiceErasureFollowsTheConversionLink(t *testing.T) {
	db := setupLeadErasureDB(t)
	customerService := service.NewCustomerService(
		repository.NewCustomerRepositoryWithLeadErasure(db),
		repository.NewUserRepository(db),
	)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "service-path@example.com")
	customer := convertLead(t, db, lead)

	require.NoError(t, customerService.Delete(customer.ID))

	assertColumnsFreeOf(t, db, "customers", leadPersonalData(lead)...)
	assertColumnsFreeOf(t, db, "leads", leadPersonalData(lead)...)
}

// Nothing in the schema stops two leads from being converted into the same
// customer, and every one of them holds its own copy of the person.
func TestErasingACustomerErasesEveryLeadConvertedIntoIt(t *testing.T) {
	db := setupLeadErasureDB(t)
	customerRepo := repository.NewCustomerRepositoryWithLeadErasure(db)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	first := seedLead(t, db, owner.ID, "duplicate-enquiry-1@example.com")
	customer := convertLead(t, db, first)

	// A second enquiry from the same person, merged into the same customer.
	second := seedLead(t, db, owner.ID, "duplicate-enquiry-2@example.com")
	require.NoError(t, leadRepo.ConvertToCustomer(second.ID, customer.ID))

	require.NoError(t, customerRepo.Delete(customer.ID))

	assertColumnsFreeOf(t, db, "leads",
		append(leadPersonalData(first), second.Email)...)
	assert.True(t, fetchErasedLead(t, db, first.ID).DeletedAt.Valid)
	assert.True(t, fetchErasedLead(t, db, second.ID).DeletedAt.Valid)
}

// A lead that was never converted must not drag an unrelated customer down with
// it, and vice versa. A cascade that erases too much is as much of a bug as one
// that erases too little.
func TestErasureDoesNotReachPeopleWhoAreNotLinked(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)
	customerRepo := repository.NewCustomerRepositoryWithLeadErasure(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "unconverted@example.com")
	bystander := seedCustomer(t, db, "bystander@example.com")
	otherLead := seedLead(t, db, owner.ID, "other-lead@example.com")

	require.NoError(t, leadRepo.Delete(lead.ID))

	survivingCustomer, err := customerRepo.GetByID(bystander.ID)
	require.NoError(t, err, "an unrelated customer must not be erased")
	assert.Equal(t, "bystander@example.com", survivingCustomer.Email)

	survivingLead, err := leadRepo.GetByID(otherLead.ID)
	require.NoError(t, err, "an unrelated lead must not be erased")
	assert.Equal(t, "other-lead@example.com", survivingLead.Email)

	// ...and the other way round.
	require.NoError(t, customerRepo.Delete(bystander.ID))
	survivingLead, err = leadRepo.GetByID(otherLead.ID)
	require.NoError(t, err)
	assert.Equal(t, "other-lead@example.com", survivingLead.Email)
}

// --- Atomicity ---------------------------------------------------------------

// The two halves are erased in ONE transaction. Committing the customer and
// then failing on the lead would leave precisely the half-erased state the
// cascade exists to prevent.
func TestConvertedPairErasureRollsBackWithTheCallersTransaction(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "atomic-pair@example.com")
	customer := convertLead(t, db, lead)

	abort := errors.New("caller aborted the unit of work")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := leadRepo.WithTx(tx).Delete(lead.ID); err != nil {
			return err
		}
		return abort
	})
	require.ErrorIs(t, err, abort)

	// Neither half may have been erased behind the caller's back.
	var reloadedLead models.Lead
	require.NoError(t, db.First(&reloadedLead, lead.ID).Error)
	assert.False(t, reloadedLead.DeletedAt.Valid)
	assert.Equal(t, "atomic-pair@example.com", reloadedLead.Email)
	assert.Equal(t, "Ingrid", reloadedLead.FirstName)

	var reloadedCustomer models.Customer
	require.NoError(t, db.First(&reloadedCustomer, customer.ID).Error)
	assert.False(t, reloadedCustomer.DeletedAt.Valid)
	assert.Equal(t, "atomic-pair@example.com", reloadedCustomer.Email)
}

func TestCustomerFirstPairErasureRollsBackWithTheCallersTransaction(t *testing.T) {
	db := setupLeadErasureDB(t)
	customerRepo := repository.NewCustomerRepositoryWithLeadErasure(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "atomic-pair-2@example.com")
	customer := convertLead(t, db, lead)

	abort := errors.New("caller aborted the unit of work")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := customerRepo.WithTx(tx).Delete(customer.ID); err != nil {
			return err
		}
		return abort
	})
	require.ErrorIs(t, err, abort)

	var reloadedLead models.Lead
	require.NoError(t, db.First(&reloadedLead, lead.ID).Error)
	assert.False(t, reloadedLead.DeletedAt.Valid)
	assert.Equal(t, "Ingrid", reloadedLead.FirstName)

	var reloadedCustomer models.Customer
	require.NoError(t, db.First(&reloadedCustomer, customer.ID).Error)
	assert.False(t, reloadedCustomer.DeletedAt.Valid)
}

// Repositories publish WithTx, so the handle may already BE a transaction; the
// erasure must join it rather than trying to begin a nested one.
func TestLeadErasureRunsInsideACallerSuppliedTransaction(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "caller-tx-lead@example.com")
	customer := convertLead(t, db, lead)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := leadRepo.WithTx(tx).Delete(lead.ID); err != nil {
			return err
		}
		return tx.Create(&models.Task{
			Title:        "Confirm the erasure with the data subject",
			Status:       models.TaskStatusPending,
			Priority:     models.TaskPriorityHigh,
			AssignedToID: owner.ID,
		}).Error
	}))

	assert.True(t, fetchErasedLead(t, db, lead.ID).DeletedAt.Valid)
	assert.True(t, fetchErasedCustomer(t, db, customer.ID).DeletedAt.Valid)
	assertColumnsFreeOf(t, db, "leads", leadPersonalData(lead)...)
}

// --- Service-level accounting ------------------------------------------------

// An erasure that matched no row must not report success. The scrub and the
// soft delete both match by primary key, and zero matched rows is not an error
// in SQL, so nothing below the service would notice.
func TestLeadServiceErasureOfAMissingLeadIsNotFound(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadService := service.NewLeadService(
		repository.NewLeadRepository(db),
		repository.NewCustomerRepository(db),
		utils.NewTransactionManager(db),
	)

	err := leadService.Delete(4242)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.ErrNotFound)
}

// Erasing the same lead twice must not be a way to break the pair apart: the
// second call finds nothing live and says so, and the first erasure stands.
func TestLeadServiceErasureIsNotRepeatable(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)
	leadService := service.NewLeadService(
		leadRepo,
		repository.NewCustomerRepository(db),
		utils.NewTransactionManager(db),
	)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "twice@example.com")

	require.NoError(t, leadService.Delete(lead.ID))
	assert.ErrorIs(t, leadService.Delete(lead.ID), apperrors.ErrNotFound)

	assertColumnsFreeOf(t, db, "leads", leadPersonalData(lead)...)
}
