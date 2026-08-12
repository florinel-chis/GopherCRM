package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupFormTestDB gives each test its own in-memory schema, with the pool
// pinned to a single connection: with the ":memory:" DSN a second pooled
// connection would open a second, empty database.
func setupFormTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&models.Form{},
		&models.FormSubmission{},
		&models.FormConfirmationToken{},
	))
	return db
}

func makeForm(t *testing.T, db *gorm.DB, name, publicID string, status models.FormStatus) *models.Form {
	t.Helper()
	form := &models.Form{
		Name:     name,
		PublicID: publicID,
		Status:   status,
		Fields: []models.FormFieldDef{
			{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true, MaxLength: 1000},
		},
		NotifyEmails: []string{"sales@example.com"},
	}
	require.NoError(t, db.Create(form).Error)
	return form
}

func makeSubmission(t *testing.T, db *gorm.DB, formID uint, email string, status models.FormSubmissionStatus) *models.FormSubmission {
	t.Helper()
	submission := &models.FormSubmission{
		FormID: formID,
		Email:  email,
		Status: status,
		Data:   map[string]string{"email": email},
	}
	require.NoError(t, db.Create(submission).Error)
	return submission
}

func makeConfirmationToken(t *testing.T, db *gorm.DB, submissionID uint, hash string, expiresAt time.Time) *models.FormConfirmationToken {
	t.Helper()
	token := &models.FormConfirmationToken{
		SubmissionID: submissionID,
		TokenHash:    hash,
		ExpiresAt:    expiresAt,
	}
	require.NoError(t, db.Create(token).Error)
	return token
}

func TestFormRepositoryCRUDRoundTrip(t *testing.T) {
	db := setupFormTestDB(t)
	repo := NewFormRepository(db)

	form := &models.Form{
		Name:           "Contact us",
		PublicID:       "pub-contact",
		Status:         models.FormStatusPublished,
		SubmitAction:   models.FormSubmitActionMessage,
		CreateLead:     true,
		DefaultOwnerID: 3,
		Fields: []models.FormFieldDef{
			{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true, MaxLength: 1000},
			{Name: "topic", Label: "Topic", Type: models.FormFieldSelect, Options: []string{"Sales", "Support"}, MaxLength: 1000},
		},
		NotifyEmails:   []string{"sales@example.com"},
		AllowedDomains: []string{"example.com"},
	}
	require.NoError(t, repo.Create(form))
	require.NotZero(t, form.ID)

	loaded, err := repo.GetByID(form.ID)
	require.NoError(t, err)
	assert.Equal(t, form.Fields, loaded.Fields)
	assert.Equal(t, []string{"sales@example.com"}, loaded.NotifyEmails)
	assert.Equal(t, []string{"example.com"}, loaded.AllowedDomains)

	loaded.Name = "Contact sales"
	loaded.Fields = append(loaded.Fields, models.FormFieldDef{Name: "company", Label: "Company", Type: models.FormFieldText, MaxLength: 1000})
	require.NoError(t, repo.Update(loaded))

	reloaded, err := repo.GetByID(form.ID)
	require.NoError(t, err)
	assert.Equal(t, "Contact sales", reloaded.Name)
	assert.Len(t, reloaded.Fields, 3)

	require.NoError(t, repo.Delete(form.ID))
	_, err = repo.GetByID(form.ID)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestFormRepositoryDeleteMissingIsNotFound(t *testing.T) {
	repo := NewFormRepository(setupFormTestDB(t))
	assert.ErrorIs(t, repo.Delete(4242), gorm.ErrRecordNotFound)
}

// A form is found by its public id whatever its status: hiding an unpublished
// form is the service's decision, not the repository's.
func TestFormRepositoryGetByPublicID(t *testing.T) {
	db := setupFormTestDB(t)
	repo := NewFormRepository(db)
	makeForm(t, db, "Draft form", "pub-draft", models.FormStatusDraft)

	found, err := repo.GetByPublicID("pub-draft")
	require.NoError(t, err)
	assert.Equal(t, models.FormStatusDraft, found.Status)

	_, err = repo.GetByPublicID("pub-unknown")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestFormRepositoryListFiltersAndCounts(t *testing.T) {
	db := setupFormTestDB(t)
	repo := NewFormRepository(db)

	makeForm(t, db, "Alpha", "pub-a", models.FormStatusPublished)
	makeForm(t, db, "Bravo", "pub-b", models.FormStatusDraft)
	makeForm(t, db, "Charlie", "pub-c", models.FormStatusPublished)

	all, total, err := repo.List(0, 20, "", "", "")
	require.NoError(t, err)
	assert.Len(t, all, 3)
	assert.EqualValues(t, 3, total)

	published, total, err := repo.List(0, 20, string(models.FormStatusPublished), "name", "asc")
	require.NoError(t, err)
	require.Len(t, published, 2)
	assert.EqualValues(t, 2, total)
	assert.Equal(t, "Alpha", published[0].Name)
	assert.Equal(t, "Charlie", published[1].Name)

	// The total describes the filter, not the page.
	page, total, err := repo.List(1, 1, string(models.FormStatusPublished), "name", "asc")
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, "Charlie", page[0].Name)
	assert.EqualValues(t, 2, total)
}

func TestFormRepositoryListRejectsUnknownSortColumn(t *testing.T) {
	repo := NewFormRepository(setupFormTestDB(t))

	_, _, err := repo.List(0, 20, "", "evil;DROP TABLE forms", "asc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid sort column")

	_, _, err = repo.List(0, 20, "", "public_id", "asc")
	assert.Error(t, err, "a real column outside the allowlist is still rejected")
}

func TestFormRepositorySubmissionCounts(t *testing.T) {
	db := setupFormTestDB(t)
	repo := NewFormRepository(db)

	first := makeForm(t, db, "First", "pub-1", models.FormStatusPublished)
	second := makeForm(t, db, "Second", "pub-2", models.FormStatusPublished)
	empty := makeForm(t, db, "Empty", "pub-3", models.FormStatusDraft)

	makeSubmission(t, db, first.ID, "a@example.com", models.FormSubmissionReceived)
	makeSubmission(t, db, first.ID, "b@example.com", models.FormSubmissionSpam)
	makeSubmission(t, db, second.ID, "c@example.com", models.FormSubmissionConfirmed)

	counts, err := repo.SubmissionCounts([]uint{first.ID, second.ID, empty.ID})
	require.NoError(t, err)
	assert.EqualValues(t, 2, counts[first.ID])
	assert.EqualValues(t, 1, counts[second.ID])
	_, present := counts[empty.ID]
	assert.False(t, present, "a form with no submissions is absent, not zero")

	counts, err = repo.SubmissionCounts(nil)
	require.NoError(t, err)
	assert.Empty(t, counts)
}

func TestFormRepositorySubmissionLifecycle(t *testing.T) {
	db := setupFormTestDB(t)
	repo := NewFormRepository(db)
	form := makeForm(t, db, "Contact", "pub-contact", models.FormStatusPublished)

	submission := &models.FormSubmission{
		FormID:    form.ID,
		Email:     "visitor@example.com",
		Status:    models.FormSubmissionPending,
		Data:      map[string]string{"email": "visitor@example.com", "first_name": "Ada"},
		IPAddress: "203.0.113.7",
	}
	require.NoError(t, repo.CreateSubmission(submission))

	loaded, err := repo.GetSubmissionByID(submission.ID)
	require.NoError(t, err)
	assert.Equal(t, submission.Data, loaded.Data)

	leadID := uint(11)
	now := time.Now()
	loaded.Status = models.FormSubmissionConfirmed
	loaded.ConfirmedAt = &now
	loaded.LeadID = &leadID
	require.NoError(t, repo.UpdateSubmission(loaded))

	reloaded, err := repo.GetSubmissionByID(submission.ID)
	require.NoError(t, err)
	assert.Equal(t, models.FormSubmissionConfirmed, reloaded.Status)
	require.NotNil(t, reloaded.LeadID)
	assert.EqualValues(t, 11, *reloaded.LeadID)
	assert.NotNil(t, reloaded.ConfirmedAt)

	_, err = repo.GetSubmissionByID(9999)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestFormRepositoryListSubmissions(t *testing.T) {
	db := setupFormTestDB(t)
	repo := NewFormRepository(db)

	form := makeForm(t, db, "Contact", "pub-contact", models.FormStatusPublished)
	other := makeForm(t, db, "Other", "pub-other", models.FormStatusPublished)

	oldest := makeSubmission(t, db, form.ID, "old@example.com", models.FormSubmissionReceived)
	require.NoError(t, db.Model(oldest).UpdateColumn("created_at", time.Now().Add(-2*time.Hour)).Error)
	makeSubmission(t, db, form.ID, "spam@example.com", models.FormSubmissionSpam)
	newest := makeSubmission(t, db, form.ID, "new@example.com", models.FormSubmissionReceived)
	makeSubmission(t, db, other.ID, "elsewhere@example.com", models.FormSubmissionReceived)

	all, total, err := repo.ListSubmissions(form.ID, 0, 20, "")
	require.NoError(t, err)
	assert.Len(t, all, 3)
	assert.EqualValues(t, 3, total)
	assert.Equal(t, newest.ID, all[0].ID, "newest first")
	assert.Equal(t, oldest.ID, all[len(all)-1].ID)

	spam, total, err := repo.ListSubmissions(form.ID, 0, 20, string(models.FormSubmissionSpam))
	require.NoError(t, err)
	require.Len(t, spam, 1)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, "spam@example.com", spam[0].Email)
}

// Used, expired and unknown tokens must be indistinguishable to the caller.
func TestFormRepositoryConfirmationTokenLookupOnlySpendable(t *testing.T) {
	db := setupFormTestDB(t)
	repo := NewFormRepository(db)
	form := makeForm(t, db, "Contact", "pub-contact", models.FormStatusPublished)
	submission := makeSubmission(t, db, form.ID, "visitor@example.com", models.FormSubmissionPending)

	live := makeConfirmationToken(t, db, submission.ID, "hash-live", time.Now().Add(48*time.Hour))
	makeConfirmationToken(t, db, submission.ID, "hash-expired", time.Now().Add(-time.Minute))
	used := makeConfirmationToken(t, db, submission.ID, "hash-used", time.Now().Add(48*time.Hour))
	spent := time.Now()
	require.NoError(t, db.Model(used).Update("used_at", &spent).Error)

	found, err := repo.GetConfirmationTokenByHash("hash-live")
	require.NoError(t, err)
	assert.Equal(t, live.ID, found.ID)
	assert.Equal(t, submission.ID, found.SubmissionID)

	for _, hash := range []string{"hash-expired", "hash-used", "hash-unknown"} {
		_, err := repo.GetConfirmationTokenByHash(hash)
		assert.ErrorIsf(t, err, gorm.ErrRecordNotFound, "hash %s must be a not-found", hash)
	}
}

func TestFormRepositoryMarkConfirmationTokenUsed(t *testing.T) {
	db := setupFormTestDB(t)
	repo := NewFormRepository(db)
	form := makeForm(t, db, "Contact", "pub-contact", models.FormStatusPublished)
	submission := makeSubmission(t, db, form.ID, "visitor@example.com", models.FormSubmissionPending)
	token := makeConfirmationToken(t, db, submission.ID, "hash-live", time.Now().Add(48*time.Hour))

	require.NoError(t, repo.MarkConfirmationTokenUsed(token.ID))

	_, err := repo.GetConfirmationTokenByHash("hash-live")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "a spent token is no longer spendable")

	var stored models.FormConfirmationToken
	require.NoError(t, db.First(&stored, token.ID).Error)
	assert.NotNil(t, stored.UsedAt, "the row is kept, only stamped")
}

// Re-submitting the same address must leave exactly one working link, and must
// not touch the tokens of another form or another address.
func TestFormRepositoryInvalidatePendingTokens(t *testing.T) {
	db := setupFormTestDB(t)
	repo := NewFormRepository(db)

	form := makeForm(t, db, "Contact", "pub-contact", models.FormStatusPublished)
	other := makeForm(t, db, "Other", "pub-other", models.FormStatusPublished)

	target := makeSubmission(t, db, form.ID, "visitor@example.com", models.FormSubmissionPending)
	sameFormOtherEmail := makeSubmission(t, db, form.ID, "someone@example.com", models.FormSubmissionPending)
	otherFormSameEmail := makeSubmission(t, db, other.ID, "visitor@example.com", models.FormSubmissionPending)
	alreadyConfirmed := makeSubmission(t, db, form.ID, "visitor@example.com", models.FormSubmissionConfirmed)

	expiry := time.Now().Add(48 * time.Hour)
	makeConfirmationToken(t, db, target.ID, "hash-target", expiry)
	makeConfirmationToken(t, db, sameFormOtherEmail.ID, "hash-other-email", expiry)
	makeConfirmationToken(t, db, otherFormSameEmail.ID, "hash-other-form", expiry)
	makeConfirmationToken(t, db, alreadyConfirmed.ID, "hash-confirmed", expiry)

	require.NoError(t, repo.InvalidatePendingTokens(form.ID, "visitor@example.com"))

	_, err := repo.GetConfirmationTokenByHash("hash-target")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "the outstanding token for this form+address is spent")

	for _, hash := range []string{"hash-other-email", "hash-other-form", "hash-confirmed"} {
		_, err := repo.GetConfirmationTokenByHash(hash)
		assert.NoErrorf(t, err, "hash %s must be untouched", hash)
	}
}

func TestFormRepositoryInvalidatePendingTokensWithNoMatchIsNoOp(t *testing.T) {
	db := setupFormTestDB(t)
	repo := NewFormRepository(db)
	form := makeForm(t, db, "Contact", "pub-contact", models.FormStatusPublished)

	assert.NoError(t, repo.InvalidatePendingTokens(form.ID, "nobody@example.com"))
}

func TestFormRepositoryWithTxSharesTheTransaction(t *testing.T) {
	db := setupFormTestDB(t)
	repo := NewFormRepository(db)

	err := db.Transaction(func(tx *gorm.DB) error {
		txRepo := repo.WithTx(tx)
		form := &models.Form{Name: "Rolled back", PublicID: "pub-rollback"}
		if err := txRepo.Create(form); err != nil {
			return err
		}
		return errors.New("nope")
	})
	require.Error(t, err)

	_, err = repo.GetByPublicID("pub-rollback")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
