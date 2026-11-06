package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #220: erasing a lead empties the six attribution columns of every
// form_submissions row linked to it, live and soft-deleted, in the erasure
// transaction. Rows of other leads and unlinked rows keep theirs, and the
// lead's automation_events rows (lead_form_submitted included) keep only ids
// and types. Run on the application's real schema, on SQLite and on a
// disposable TEST_MYSQL_DSN schema.

func TestFormAttributionErasedWithLeadSQLite(t *testing.T) {
	runFormAttributionErasedWithLead(t, false)
}

func TestFormAttributionErasedWithLeadMySQL(t *testing.T) {
	if os.Getenv("TEST_MYSQL_DSN") == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	runFormAttributionErasedWithLead(t, true)
}

type attributionErasureFixture struct {
	form                 *models.Form
	lead, other          *models.Lead
	live, gone           *models.FormSubmission
	otherLead, unlinked  *models.FormSubmission
	liveEvent, goneEvent *models.AutomationEvent
	needles              []string
}

func attributionSubmission(t *testing.T, db *gorm.DB, formID uint, leadID *uint, email, label string) (*models.FormSubmission, []string) {
	t.Helper()
	values := []string{
		"src-" + label, "medium-" + label, "campaign-" + label, "term-" + label, "content-" + label, label + ".host.example",
	}
	submission := &models.FormSubmission{
		FormID: formID, Data: map[string]string{"email": email, "utm_campaign": values[2]}, Email: email,
		Status: models.FormSubmissionReceived, LeadID: leadID, Referrer: "https://" + values[5] + "/?utm_campaign=" + values[2],
		UTMSource: values[0], UTMMedium: values[1], UTMCampaign: values[2], UTMTerm: values[3], UTMContent: values[4], ReferrerHost: values[5],
	}
	require.NoError(t, db.Create(submission).Error)
	return submission, values
}

func seedAttributionErasure(t *testing.T, db *gorm.DB) *attributionErasureFixture {
	t.Helper()
	tag := nextIdentityTag()
	owner := seedIdentityStaff(t, db, tag, models.RoleSales)
	admin := seedIdentityStaff(t, db, tag, models.RoleAdmin)
	f := &attributionErasureFixture{}
	f.form = &models.Form{Name: "Attribution " + tag, PublicID: "attr-" + tag, Status: models.FormStatusPublished,
		Fields:     []models.FormFieldDef{{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true}},
		CreateLead: true, DefaultOwnerID: owner.ID, CreatedByID: admin.ID}
	require.NoError(t, db.Create(f.form).Error)

	newLead := func(email string) *models.Lead {
		lead := &models.Lead{FirstName: "Ilinca" + tag, LastName: "Voda" + tag, Email: email, Source: f.form.Name,
			Status: models.LeadStatusNew, OwnerID: owner.ID}
		require.NoError(t, db.Create(lead).Error)
		return lead
	}
	f.lead = newLead("ilinca." + tag + "@person.test")
	f.other = newLead("someone.else." + tag + "@person.test")

	var liveValues, goneValues []string
	f.live, liveValues = attributionSubmission(t, db, f.form.ID, &f.lead.ID, f.lead.Email, "ilinca-live-"+tag)
	f.gone, goneValues = attributionSubmission(t, db, f.form.ID, &f.lead.ID, f.lead.Email, "ilinca-gone-"+tag)
	require.NoError(t, db.Delete(&models.FormSubmission{}, f.gone.ID).Error, "a soft-deleted submission still holds the person")
	f.otherLead, _ = attributionSubmission(t, db, f.form.ID, &f.other.ID, f.other.Email, "bystander-"+tag)
	f.unlinked, _ = attributionSubmission(t, db, f.form.ID, nil, "spam."+tag+"@person.test", "unlinked-"+tag)
	f.needles = append(liveValues, goneValues...)

	repo := NewAutomationRepository(db)
	record := func(submission *models.FormSubmission) *models.AutomationEvent {
		formID, submissionID := f.form.ID, submission.ID
		event := &models.AutomationEvent{EntityType: models.AutomationEntityLead, EntityID: f.lead.ID,
			EventType: models.AutomationEventLeadFormSubmitted, Origin: models.AutomationOriginForm,
			FormID: &formID, SubmissionID: &submissionID, CreatedAt: time.Now().UTC()}
		require.NoError(t, repo.RecordEvent(event))
		return event
	}
	f.liveEvent = record(f.live)
	f.goneEvent = record(f.gone)

	// The sweep sees every seeded copy before the erasure.
	hits := identitySweep(t, db, f.needles)
	for i, column := range identityAttributionColumns {
		for _, values := range [][]string{liveValues, goneValues} {
			require.Contains(t, hits, fmt.Sprintf("form_submissions.%s contains %q", column, values[i]))
		}
	}
	return f
}

func loadSubmission(t *testing.T, db *gorm.DB, id uint) models.FormSubmission {
	t.Helper()
	var submission models.FormSubmission
	require.NoError(t, db.Unscoped().First(&submission, id).Error)
	return submission
}

func attributionColumns(s models.FormSubmission) []string {
	return []string{s.UTMSource, s.UTMMedium, s.UTMCampaign, s.UTMTerm, s.UTMContent, s.ReferrerHost}
}

func runFormAttributionErasedWithLead(t *testing.T, useMySQL bool) {
	db := openIdentityErasureDB(t, useMySQL)

	t.Run("an erasure that rolls back leaves the attribution", func(t *testing.T) {
		f := seedAttributionErasure(t, db)
		before := attributionColumns(loadSubmission(t, db, f.live.ID))
		abort := errors.New("abort after the erasure")
		err := utils.NewTransactionManager(db).WithTransaction(context.Background(), func(ctx context.Context) error {
			tx, ok := utils.GetTxFromContext(ctx)
			require.True(t, ok)
			require.NoError(t, NewLeadRepository(db).WithTx(tx).Delete(f.lead.ID))
			// Inside the transaction the columns are already empty.
			var inside models.FormSubmission
			require.NoError(t, tx.Unscoped().First(&inside, f.live.ID).Error)
			assert.Equal(t, []string{"", "", "", "", "", ""}, attributionColumns(inside))
			return abort
		})
		require.ErrorIs(t, err, abort)
		assert.Equal(t, before, attributionColumns(loadSubmission(t, db, f.live.ID)), "the rollback restores the attribution")
		require.NoError(t, NewLeadRepository(db).Delete(f.lead.ID), "clean up the graph")
	})

	t.Run("erasing the lead empties the attribution of every linked row", func(t *testing.T) {
		f := seedAttributionErasure(t, db)
		otherBefore := attributionColumns(loadSubmission(t, db, f.otherLead.ID))
		unlinkedBefore := attributionColumns(loadSubmission(t, db, f.unlinked.ID))

		require.NoError(t, NewLeadRepository(db).Delete(f.lead.ID))

		for _, id := range []uint{f.live.ID, f.gone.ID} {
			submission := loadSubmission(t, db, id)
			assert.Equal(t, []string{"", "", "", "", "", ""}, attributionColumns(submission), "submission %d", id)
			assert.Empty(t, submission.Referrer)
			require.NotNil(t, submission.LeadID, "the link is an id and stays")
			assert.Equal(t, f.lead.ID, *submission.LeadID)
		}
		assert.True(t, loadSubmission(t, db, f.gone.ID).DeletedAt.Valid, "the soft-deleted row keeps its deletion state")
		assert.Equal(t, otherBefore, attributionColumns(loadSubmission(t, db, f.otherLead.ID)), "another lead's enquiry is not touched")
		assert.Equal(t, unlinkedBefore, attributionColumns(loadSubmission(t, db, f.unlinked.ID)), "an unlinked row is outside the erasure")

		assert.Empty(t, identitySweep(t, db, f.needles), "no copy of the erased attribution is left anywhere")

		// The lead's events keep only ids and types.
		var events []models.AutomationEvent
		require.NoError(t, db.Where("entity_type = ? AND entity_id = ?", models.AutomationEntityLead, f.lead.ID).Order("id").Find(&events).Error)
		require.Len(t, events, 2)
		for i, want := range []*models.AutomationEvent{f.liveEvent, f.goneEvent} {
			assert.Equal(t, want.ID, events[i].ID)
			assert.Equal(t, models.AutomationEventLeadFormSubmitted, events[i].EventType)
			assert.Equal(t, models.AutomationOriginForm, events[i].Origin)
			require.NotNil(t, events[i].FormID)
			require.NotNil(t, events[i].SubmissionID)
			assert.Equal(t, *want.FormID, *events[i].FormID)
			assert.Equal(t, *want.SubmissionID, *events[i].SubmissionID)
			assert.Empty(t, events[i].FromValue)
			assert.Empty(t, events[i].ToValue)
		}
	})
}
