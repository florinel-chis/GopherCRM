package integration

import (
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Form submissions duplicate the personal data of the lead they produced: the
// submitted values, the email address, the visitor's IP address, user agent and
// the page they came from. Erasing the lead while its submissions keep all of
// that would erase nobody, so the lead erasure has to scrub the linked
// submissions in the same transaction — the same reasoning that makes the
// lead/customer conversion cascade follow leads.customer_id.

func setupFormErasureDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupDB(t)
	require.NoError(t, db.AutoMigrate(
		&models.User{},
		&models.Lead{},
		&models.Customer{},
		&models.APIKey{},
		&models.RefreshToken{},
		&models.PasswordResetToken{},
		&models.Ticket{},
		&models.Task{},
		&models.Form{},
		&models.FormSubmission{},
		&models.FormConfirmationToken{},
	))
	return db
}

func seedFormSubmission(t *testing.T, db *gorm.DB, formID uint, leadID *uint, email string) *models.FormSubmission {
	t.Helper()
	sub := &models.FormSubmission{
		FormID: formID,
		Data: map[string]string{
			"email":   email,
			"message": "Please call me about the new project",
		},
		Email:     email,
		Status:    models.FormSubmissionConfirmed,
		LeadID:    leadID,
		IPAddress: "198.51.100.7",
		UserAgent: "Mozilla/5.0 (integration test)",
		Referrer:  "https://example.com/landing",
	}
	require.NoError(t, db.Create(sub).Error)
	return sub
}

func TestLeadErasureScrubsLinkedFormSubmissions(t *testing.T) {
	db := setupFormErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	form := &models.Form{
		Name:     "Quote request",
		PublicID: "erasure-test-form",
		Status:   models.FormStatusPublished,
		Fields: []models.FormFieldDef{
			{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
		},
		CreateLead:     true,
		DefaultOwnerID: owner.ID,
		CreatedByID:    owner.ID,
	}
	require.NoError(t, db.Create(form).Error)

	lead := seedLead(t, db, owner.ID, "ingrid@example.com")
	linked := seedFormSubmission(t, db, form.ID, &lead.ID, lead.Email)
	require.NoError(t, db.Create(&models.FormConfirmationToken{
		SubmissionID: linked.ID,
		TokenHash:    strings.Repeat("a", 64),
	}).Error)

	// A submission that never became this lead must stay untouched.
	unrelated := &models.FormSubmission{
		FormID:    form.ID,
		Data:      map[string]string{"email": "someone-else@example.com", "message": "Unrelated enquiry"},
		Email:     "someone-else@example.com",
		Status:    models.FormSubmissionReceived,
		IPAddress: "203.0.113.9",
		UserAgent: "Mozilla/5.0 (integration test)",
		Referrer:  "https://example.com/pricing",
	}
	require.NoError(t, db.Create(unrelated).Error)

	require.NoError(t, leadRepo.Delete(lead.ID))

	var erasedLead models.Lead
	require.NoError(t, db.Unscoped().First(&erasedLead, lead.ID).Error)

	var scrubbed models.FormSubmission
	require.NoError(t, db.Unscoped().First(&scrubbed, linked.ID).Error)
	assert.Empty(t, scrubbed.Data, "submitted values must be gone")
	assert.Equal(t, erasedLead.Email, scrubbed.Email,
		"the submission keeps the lead's placeholder address, not the real one")
	assert.Contains(t, scrubbed.Email, ".invalid")
	assert.Empty(t, scrubbed.IPAddress)
	assert.Empty(t, scrubbed.UserAgent)
	assert.Empty(t, scrubbed.Referrer)

	var tokens int64
	require.NoError(t, db.Unscoped().Model(&models.FormConfirmationToken{}).
		Where("submission_id = ?", linked.ID).Count(&tokens).Error)
	assert.Zero(t, tokens, "confirmation tokens must be hard-deleted")

	// Nothing in the submissions table may still carry the person's data.
	assertColumnsFreeOf(t, db, "form_submissions",
		"ingrid@example.com", "198.51.100.7", "Please call me about the new project")

	var untouched models.FormSubmission
	require.NoError(t, db.Unscoped().First(&untouched, unrelated.ID).Error)
	assert.Equal(t, "someone-else@example.com", untouched.Email)
	assert.Equal(t, "203.0.113.9", untouched.IPAddress)
	assert.NotEmpty(t, untouched.Data)
}

func TestConvertedLeadErasureScrubsSubmissionsViaTheCascade(t *testing.T) {
	db := setupFormErasureDB(t)
	customerRepo := repository.NewCustomerRepositoryWithLeadErasure(db)

	owner := seedLeadOwner(t, db)
	form := &models.Form{
		Name:     "Contact",
		PublicID: "erasure-test-form-2",
		Status:   models.FormStatusPublished,
		Fields: []models.FormFieldDef{
			{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
		},
		CreateLead:     true,
		DefaultOwnerID: owner.ID,
		CreatedByID:    owner.ID,
	}
	require.NoError(t, db.Create(form).Error)

	// A converted pair: erasing the CUSTOMER follows leads.customer_id back to
	// the lead, whose submissions must be scrubbed by the same cascade.
	customer := &models.Customer{
		FirstName: "Ingrid",
		LastName:  "Vasilescu",
		Email:     "ingrid@example.com",
	}
	require.NoError(t, db.Create(customer).Error)
	lead := seedLead(t, db, owner.ID, "ingrid@example.com")
	require.NoError(t, db.Model(&models.Lead{}).Where("id = ?", lead.ID).
		Updates(map[string]interface{}{"status": models.LeadStatusConverted, "customer_id": customer.ID}).Error)

	sub := seedFormSubmission(t, db, form.ID, &lead.ID, lead.Email)

	require.NoError(t, customerRepo.Delete(customer.ID))

	var scrubbed models.FormSubmission
	require.NoError(t, db.Unscoped().First(&scrubbed, sub.ID).Error)
	assert.Empty(t, scrubbed.Data)
	assert.Contains(t, scrubbed.Email, ".invalid")
	assert.Empty(t, scrubbed.IPAddress)
}
