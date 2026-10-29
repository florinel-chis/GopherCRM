package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// AC-3: every committed lead write records its events in automation_events
// inside the write's own transaction, with no personal data in any column:
// LeadService.Create and Update, a form submission's create or update, bulk
// status, and ConvertToCustomer. A rolled-back write and a refused bulk batch
// record nothing.

func TestAutomationLeadEventsSQLite(t *testing.T) {
	runAutomationLeadEvents(t, openAutomationSQLite(t))
}

func TestAutomationLeadEventsMySQL(t *testing.T) {
	runAutomationLeadEvents(t, openAutomationMySQL(t, 2))
}

// failingLeadRepo makes Update fail so the write transaction rolls back.
type failingLeadRepo struct {
	repository.LeadRepository
	fail *bool
}

func (r *failingLeadRepo) Update(lead *models.Lead) error {
	if *r.fail {
		if err := r.LeadRepository.Update(lead); err != nil {
			return err
		}
		return errors.New("injected lead update fault")
	}
	return r.LeadRepository.Update(lead)
}

func (r *failingLeadRepo) WithTx(tx *gorm.DB) repository.LeadRepository {
	return &failingLeadRepo{LeadRepository: r.LeadRepository.WithTx(tx), fail: r.fail}
}

// assertEventsHoldNoPersonalData sweeps every column of every event for the
// canary values and the lead's address.
func assertEventsHoldNoPersonalData(t *testing.T, db *gorm.DB, needles ...string) {
	t.Helper()
	var rows []map[string]interface{}
	require.NoError(t, db.Table("automation_events").Find(&rows).Error)
	for _, row := range rows {
		for column, value := range row {
			text := strings.ToLower(fmt.Sprintf("%v", value))
			for _, needle := range needles {
				assert.NotContainsf(t, text, strings.ToLower(needle), "automation_events.%s holds personal data", column)
			}
		}
	}
}

func runAutomationLeadEvents(t *testing.T, db *gorm.DB) {
	env := newAutomationEnv(t, db)
	fail := false
	leadRepo := &failingLeadRepo{LeadRepository: env.leads, fail: &fail}
	companyRepo := repository.NewCompanyRepository(db)
	leadService := NewLeadService(leadRepo, env.customers, companyRepo, env.txManager, WithLeadAutomationEvents(env.repo))

	t.Run("create and update through the lead service", func(t *testing.T) {
		lead := &models.Lead{
			FirstName: automationCanaryFirstName, LastName: automationCanaryLastName,
			Email: "zelda-service@example.com", Phone: automationCanaryPhone, Notes: automationCanaryNotes, OwnerID: env.sales.ID,
		}
		require.NoError(t, leadService.Create(lead))
		events := env.eventsOf(models.AutomationEntityLead, lead.ID)
		require.Equal(t, []string{models.AutomationEventLeadCreated}, eventTypes(events))
		assert.Equal(t, models.AutomationOriginUser, events[0].Origin)
		assert.Nil(t, events[0].ProcessedAt)

		_, err := leadService.Update(lead.ID, map[string]interface{}{"status": models.LeadStatusContacted, "notes": "called twice"})
		require.NoError(t, err)
		events = env.eventsOf(models.AutomationEntityLead, lead.ID)
		require.Equal(t, []string{models.AutomationEventLeadCreated, models.AutomationEventLeadUpdated, models.AutomationEventLeadStatusChanged}, eventTypes(events))
		assert.Equal(t, string(models.LeadStatusNew), events[2].FromValue)
		assert.Equal(t, string(models.LeadStatusContacted), events[2].ToValue)
		assert.Equal(t, models.AutomationOriginUser, events[2].Origin)

		_, err = leadService.Update(lead.ID, map[string]interface{}{"company": "Changed Co"})
		require.NoError(t, err)
		events = env.eventsOf(models.AutomationEntityLead, lead.ID)
		assert.Equal(t, models.AutomationEventLeadUpdated, events[len(events)-1].EventType, "an update without a status change records lead_updated only")
		assert.Len(t, events, 4)

		// A write that rolls back records nothing: the fault fires after the
		// row was updated inside the transaction, so the rollback has to
		// discard both the row and the event.
		fail = true
		_, err = leadService.Update(lead.ID, map[string]interface{}{"status": models.LeadStatusQualified})
		fail = false
		require.Error(t, err)
		assert.Len(t, env.eventsOf(models.AutomationEntityLead, lead.ID), 4, "a rolled-back update records no event")
		assert.Equal(t, models.LeadStatusContacted, env.lead(lead.ID).Status, "the rolled-back write left the row as it was")

		assertEventsHoldNoPersonalData(t, db, automationCanaryFirstName, automationCanaryLastName, automationCanaryPhone, "zelda-service@example.com", "called twice")
	})

	t.Run("form submission creates and then updates the lead", func(t *testing.T) {
		f := newFormFixtureOn(t, config.FormsConfig{PublicBaseURL: formTestPublicBase}, db)
		f.service = NewFormService(f.repo, f.leadRepo, f.userRepo, f.mailer, env.txManager, f.cfg, formTestAPIPrefix,
			WithFormAutomationEvents(env.repo))
		form := f.publish(t, f.newForm())

		_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
		require.NoError(t, err)
		lead, err := env.leads.GetLatestByEmail("ada@example.com")
		require.NoError(t, err)
		events := env.eventsOf(models.AutomationEntityLead, lead.ID)
		require.Equal(t, []string{models.AutomationEventLeadCreated}, eventTypes(events))
		assert.Equal(t, models.AutomationOriginForm, events[0].Origin)

		_, err = f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
		require.NoError(t, err)
		events = env.eventsOf(models.AutomationEntityLead, lead.ID)
		require.Equal(t, []string{models.AutomationEventLeadCreated, models.AutomationEventLeadUpdated}, eventTypes(events))
		assert.Equal(t, models.AutomationOriginForm, events[1].Origin)
		assertEventsHoldNoPersonalData(t, db, "ada@example.com", "Lovelace", "Please call me back")
	})

	t.Run("bulk status", func(t *testing.T) {
		bulk := NewBulkStatusService(repository.NewBulkOperationRepository(db), repository.NewBulkStatusRepository(db),
			env.txManager, logrus.New(), WithBulkAutomationEvents(env.repo))
		first := env.createLead(env.sales, models.LeadStatusNew)
		second := env.createLead(env.sales, models.LeadStatusQualified)

		// All-or-nothing status update: a missing id refuses the batch and
		// records nothing for any item.
		_, err := bulk.BulkSetLeadStatus(env.admin.ID, models.RoleAdmin, []uint{first.ID, second.ID, 999999}, models.LeadStatusQualified)
		require.Error(t, err)
		assert.Empty(t, env.eventsOf(models.AutomationEntityLead, first.ID))
		assert.Empty(t, env.eventsOf(models.AutomationEntityLead, second.ID))

		_, err = bulk.BulkSetLeadStatus(env.admin.ID, models.RoleAdmin, []uint{first.ID, second.ID}, models.LeadStatusQualified)
		require.NoError(t, err)
		firstEvents := env.eventsOf(models.AutomationEntityLead, first.ID)
		require.Equal(t, []string{models.AutomationEventLeadUpdated, models.AutomationEventLeadStatusChanged}, eventTypes(firstEvents))
		assert.Equal(t, models.AutomationOriginBulk, firstEvents[0].Origin)
		assert.Equal(t, string(models.LeadStatusNew), firstEvents[1].FromValue)
		assert.Equal(t, string(models.LeadStatusQualified), firstEvents[1].ToValue)
		require.NotNil(t, firstEvents[0].ActorID)
		assert.Equal(t, env.admin.ID, *firstEvents[0].ActorID)
		assert.Equal(t, []string{models.AutomationEventLeadUpdated}, eventTypes(env.eventsOf(models.AutomationEntityLead, second.ID)),
			"a lead already in the status records lead_updated only")

		// The same batch again: every lead is already qualified, so each
		// records lead_updated and no status change.
		_, err = bulk.BulkSetLeadStatus(env.admin.ID, models.RoleAdmin, []uint{first.ID, second.ID}, models.LeadStatusQualified)
		require.NoError(t, err)
		firstEvents = env.eventsOf(models.AutomationEntityLead, first.ID)
		require.Equal(t, []string{models.AutomationEventLeadUpdated, models.AutomationEventLeadStatusChanged, models.AutomationEventLeadUpdated}, eventTypes(firstEvents))

		// A sales caller that owns neither lead is refused for the whole batch
		// and nothing is recorded.
		_, err = bulk.BulkSetLeadStatus(env.other.ID, models.RoleSales, []uint{first.ID, second.ID}, models.LeadStatusContacted)
		require.Error(t, err)
		assert.Len(t, env.eventsOf(models.AutomationEntityLead, first.ID), 3)
		assert.Len(t, env.eventsOf(models.AutomationEntityLead, second.ID), 2)
		assert.Equal(t, models.LeadStatusQualified, env.lead(first.ID).Status)

		// Moving to converted records the status change with origin bulk.
		_, err = bulk.BulkSetLeadStatus(env.admin.ID, models.RoleAdmin, []uint{second.ID}, models.LeadStatusConverted)
		require.NoError(t, err)
		secondEvents := env.eventsOf(models.AutomationEntityLead, second.ID)
		require.Equal(t, []string{models.AutomationEventLeadUpdated, models.AutomationEventLeadUpdated, models.AutomationEventLeadUpdated, models.AutomationEventLeadStatusChanged}, eventTypes(secondEvents))
		assert.Equal(t, string(models.LeadStatusQualified), secondEvents[3].FromValue)
		assert.Equal(t, string(models.LeadStatusConverted), secondEvents[3].ToValue)
		assert.Equal(t, models.AutomationOriginBulk, secondEvents[3].Origin)
		assertEventsHoldNoPersonalData(t, db, automationCanaryFirstName, automationCanaryLastName, automationCanaryPhone, automationCanaryNotes)
	})

	t.Run("conversion records the status change with origin conversion", func(t *testing.T) {
		lead := env.createLead(env.sales, models.LeadStatusQualified)
		customer, err := leadService.ConvertToCustomer(lead.ID, &models.Customer{})
		require.NoError(t, err)
		require.NotZero(t, customer.ID)
		events := env.eventsOf(models.AutomationEntityLead, lead.ID)
		require.Equal(t, []string{models.AutomationEventLeadUpdated, models.AutomationEventLeadStatusChanged}, eventTypes(events))
		assert.Equal(t, models.AutomationOriginConversion, events[0].Origin)
		assert.Equal(t, string(models.LeadStatusQualified), events[1].FromValue)
		assert.Equal(t, string(models.LeadStatusConverted), events[1].ToValue)
		assertEventsHoldNoPersonalData(t, db, lead.Email, automationCanaryFirstName)
	})

	t.Run("every event row is ids, types, values and origins only", func(t *testing.T) {
		for _, event := range env.events() {
			assert.Equal(t, models.AutomationEntityLead, event.EntityType)
			assert.NotZero(t, event.EntityID)
			assert.Contains(t, []string{models.AutomationOriginUser, models.AutomationOriginForm, models.AutomationOriginBulk, models.AutomationOriginConversion}, event.Origin)
			for _, value := range []string{event.FromValue, event.ToValue} {
				if value != "" {
					assert.True(t, isValidLeadStatus(models.LeadStatus(value)), "from/to carry statuses only, got %q", value)
				}
			}
		}
	})
}
