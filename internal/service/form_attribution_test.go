package service

// Regression roots of #220: per-submission attribution, the
// lead_form_submitted event, its dispatch filter, the attribution conditions
// and their validation. The database roots run the real form service, engine
// and repositories on a file-backed one-connection SQLite database and on a
// disposable TEST_MYSQL_DSN schema; the MySQL roots skip without the variable
// and fail when it names an unreachable server.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ------------------------------------------------------------------ AC-1 ---

func TestFormAttributionParse(t *testing.T) {
	long := strings.Repeat("É", 150)
	cases := []struct {
		name   string
		values map[string]string
		page   string
		want   formAttribution
	}{
		{
			name:   "a non-blank submitted field wins over the page URL",
			values: map[string]string{"utm_source": "  News Letter "},
			page:   "https://shop.example/landing?utm_source=other&utm_medium=Email",
			want:   formAttribution{UTMSource: "news letter", UTMMedium: "email", ReferrerHost: "shop.example"},
		},
		{
			name:   "a blank submitted field falls back to the page URL",
			values: map[string]string{"utm_source": "   ", "utm_campaign": "\u0007\t"},
			page:   "https://shop.example/?utm_source=Partner&utm_campaign=Spring%20Sale",
			want:   formAttribution{UTMSource: "partner", UTMCampaign: "spring sale", ReferrerHost: "shop.example"},
		},
		{
			name: "the first value of a repeated parameter is used",
			page: "https://shop.example/?utm_campaign=First&utm_campaign=second&utm_term=a&utm_term=b",
			want: formAttribution{UTMCampaign: "first", UTMTerm: "a", ReferrerHost: "shop.example"},
		},
		{
			name: "every utm key is read, unknown parameters are ignored",
			page: "http://shop.example/?utm_source=s&utm_medium=m&utm_campaign=c&utm_term=t&utm_content=x&gclid=secret&email=a@b.example",
			want: formAttribution{UTMSource: "s", UTMMedium: "m", UTMCampaign: "c", UTMTerm: "t", UTMContent: "x", ReferrerHost: "shop.example"},
		},
		{
			name:   "control characters are stripped and the value lower-cased",
			values: map[string]string{"utm_content": "Hea\u0000der\r\n-CTA\u007f"},
			want:   formAttribution{UTMContent: "header-cta"},
		},
		{
			name:   "values are cut to 100 runes on a rune boundary",
			values: map[string]string{"utm_term": long},
			want:   formAttribution{UTMTerm: strings.Repeat("é", 100)},
		},
		{
			name:   "invalid UTF-8 is dropped",
			values: map[string]string{"utm_source": "ab\xffcd"},
			want:   formAttribution{UTMSource: "abcd"},
		},
		{
			name: "the host drops credentials, port, path, query and fragment",
			page: "HTTPS://User:Pass@Shop.EXAMPLE.com:8443/path/page?q=1#frag",
			want: formAttribution{ReferrerHost: "shop.example.com"},
		},
		{
			name: "an IPv6 host loses its brackets and port",
			page: "http://[2001:DB8::1]:8080/x",
			want: formAttribution{ReferrerHost: "2001:db8::1"},
		},
		{
			name: "a host is cut to 255 characters",
			page: "https://" + strings.Repeat("a", 300) + ".example/",
			want: formAttribution{ReferrerHost: strings.Repeat("a", 255)},
		},
		{name: "a non-http scheme gives nothing", page: "ftp://shop.example/?utm_source=x", want: formAttribution{}},
		{name: "a javascript URL gives nothing", page: "javascript:alert(1)//?utm_source=x", want: formAttribution{}},
		{name: "a relative URL gives nothing", page: "/landing?utm_source=x", want: formAttribution{}},
		{name: "a URL without a host gives nothing", page: "https:///landing?utm_source=x", want: formAttribution{}},
		{name: "an unparsable URL gives nothing", page: "http://%zz/?utm_source=x", want: formAttribution{}},
		{name: "nothing submitted gives nothing", want: formAttribution{}},
		{
			name:   "submitted values still count with an unparsable URL",
			values: map[string]string{"utm_medium": "CPC"},
			page:   "not a url at all",
			want:   formAttribution{UTMMedium: "cpc"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseFormAttribution(tc.values, tc.page)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, got, parseFormAttribution(tc.values, tc.page), "parsing is deterministic")
			for _, value := range []string{got.UTMSource, got.UTMMedium, got.UTMCampaign, got.UTMTerm, got.UTMContent} {
				assert.True(t, utf8.ValidString(value))
				assert.LessOrEqual(t, utf8.RuneCountInString(value), 100)
			}
			assert.LessOrEqual(t, utf8.RuneCountInString(got.ReferrerHost), 255)
		})
	}

	t.Run("applyFormAttribution reads the stored values and referrer", func(t *testing.T) {
		submission := &models.FormSubmission{
			Data:     map[string]string{"utm_source": "Ads"},
			Referrer: "https://www.example.com/p?utm_campaign=Q4",
		}
		applyFormAttribution(submission)
		assert.Equal(t, "ads", submission.UTMSource)
		assert.Equal(t, "q4", submission.UTMCampaign)
		assert.Equal(t, "www.example.com", submission.ReferrerHost)
		assert.Empty(t, submission.UTMMedium)
	})
}

// ---------------------------------------------------------------- fixture ---

// attributionFixture is the form service, wired with automation events, and
// the automation engine and service on one database.
type attributionFixture struct {
	t     *testing.T
	db    *gorm.DB
	env   *automationEnv
	forms *formFixture
}

func newAttributionFixture(t *testing.T, db *gorm.DB) *attributionFixture {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&models.FormMailDelivery{}))
	env := newAutomationEnv(t, db)
	f := newFormFixtureOn(t, config.FormsConfig{PublicBaseURL: formTestPublicBase}, db)
	a := &attributionFixture{t: t, db: db, env: env, forms: f}
	a.wire(f.repo)
	return a
}

// wire rebuilds the form service around repo with automation events.
func (a *attributionFixture) wire(repo repository.FormRepository) {
	f := a.forms
	f.service = NewFormService(repo, f.leadRepo, f.userRepo, f.mailer, a.env.txManager, f.cfg, formTestAPIPrefix,
		WithFormAutomationEvents(a.env.repo))
}

func attributionFormFields() []models.FormFieldDef {
	return []models.FormFieldDef{
		{Name: "first_name", Label: "First name", Type: models.FormFieldText},
		{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
		{Name: "utm_source", Type: models.FormFieldHidden},
		{Name: "utm_medium", Type: models.FormFieldHidden},
		{Name: "utm_campaign", Type: models.FormFieldHidden},
		{Name: "utm_term", Type: models.FormFieldHidden},
		{Name: "utm_content", Type: models.FormFieldHidden},
	}
}

func (a *attributionFixture) form(name string, optIn, createLead bool) *models.Form {
	a.t.Helper()
	form := a.forms.newForm()
	form.Name = name
	form.Fields = attributionFormFields()
	form.NotifyEmails = nil
	form.DoubleOptIn = optIn
	form.CreateLead = createLead
	return a.forms.publish(a.t, form)
}

func attributedRequest(email, pageURL string, utm map[string]string) *PublicSubmissionRequest {
	values := map[string]string{"first_name": "Ada", "email": email}
	for key, value := range utm {
		values[key] = value
	}
	return &PublicSubmissionRequest{Values: values, Challenge: challengeAged(30 * time.Second), PageURL: pageURL}
}

func (a *attributionFixture) submit(form *models.Form, req *PublicSubmissionRequest) *models.FormSubmission {
	a.t.Helper()
	_, err := a.forms.service.SubmitPublic(context.Background(), form.PublicID, req, submissionMeta())
	require.NoError(a.t, err)
	return latestSubmission(a.t, a.db, form.ID)
}

// confirm spends the newest confirmation link mailed to the address.
func (a *attributionFixture) confirm(email string) {
	a.t.Helper()
	mails := a.forms.mailer.to(email)
	require.NotEmpty(a.t, mails, "a confirmation mail was sent")
	require.NoError(a.t, a.forms.service.ConfirmSubmission(context.Background(), tokenFromLink(a.t, mails[len(mails)-1].Body)))
}

func (a *attributionFixture) submission(id uint) *models.FormSubmission {
	a.t.Helper()
	var submission models.FormSubmission
	require.NoError(a.t, a.db.Unscoped().First(&submission, id).Error)
	return &submission
}

func (a *attributionFixture) eventsOfType(eventType string) []models.AutomationEvent {
	a.t.Helper()
	var events []models.AutomationEvent
	require.NoError(a.t, a.db.Where("event_type = ?", eventType).Order("id ASC").Find(&events).Error)
	return events
}

func attributionOf(s *models.FormSubmission) formAttribution {
	return formAttribution{
		UTMSource: s.UTMSource, UTMMedium: s.UTMMedium, UTMCampaign: s.UTMCampaign,
		UTMTerm: s.UTMTerm, UTMContent: s.UTMContent, ReferrerHost: s.ReferrerHost,
	}
}

// failingSubmissionUpdateRepo fails UpdateSubmission, the write that links a
// submission to its lead, after the lead and its events were written.
type failingSubmissionUpdateRepo struct {
	repository.FormRepository
}

func (r failingSubmissionUpdateRepo) WithTx(tx *gorm.DB) repository.FormRepository {
	return failingSubmissionUpdateRepo{FormRepository: r.FormRepository.WithTx(tx)}
}

func (r failingSubmissionUpdateRepo) UpdateSubmission(*models.FormSubmission) error {
	return errors.New("injected submission link fault")
}

// ------------------------------------------------------------------ AC-2 ---

func TestFormSubmissionAttributionSQLite(t *testing.T) {
	runFormSubmissionAttribution(t, openAutomationSQLite(t))
}

func TestFormSubmissionAttributionMySQL(t *testing.T) {
	runFormSubmissionAttribution(t, openAutomationMySQL(t, 2))
}

func runFormSubmissionAttribution(t *testing.T, db *gorm.DB) {
	a := newAttributionFixture(t, db)

	t.Run("a received submission commits its own attribution and the lead keeps its source", func(t *testing.T) {
		form := a.form("Pricing page", false, true)
		first := a.submit(form, attributedRequest("repeat@example.com",
			"https://User:pw@Shop.Example.com:8443/pricing?utm_source=ignored&utm_medium=Email&utm_campaign=Spring%20Sale",
			map[string]string{"utm_source": " Newsletter ", "utm_term": "CRM"}))
		assert.Equal(t, models.FormSubmissionReceived, first.Status)
		assert.Equal(t, formAttribution{
			UTMSource: "newsletter", UTMMedium: "email", UTMCampaign: "spring sale", UTMTerm: "crm", ReferrerHost: "shop.example.com",
		}, attributionOf(first))

		second := a.submit(form, attributedRequest("repeat@example.com",
			"https://www.partner.example/blog?utm_campaign=Autumn&utm_content=Footer",
			map[string]string{"utm_source": "Partner"}))
		assert.NotEqual(t, first.ID, second.ID)
		assert.Equal(t, formAttribution{
			UTMSource: "partner", UTMCampaign: "autumn", UTMContent: "footer", ReferrerHost: "www.partner.example",
		}, attributionOf(second))
		assert.Equal(t, attributionOf(first), attributionOf(a.submission(first.ID)), "the first enquiry keeps its own values")

		require.NotNil(t, first.LeadID)
		require.NotNil(t, second.LeadID)
		assert.Equal(t, *first.LeadID, *second.LeadID, "both enquiries feed one lead")
		lead := a.env.lead(*first.LeadID)
		assert.Equal(t, "Pricing page", lead.Source, "the lead keeps the form name it was created with")

		// The service read returns each submission's own attribution.
		read, err := a.forms.service.GetSubmission(second.ID)
		require.NoError(t, err)
		assert.Equal(t, "autumn", read.UTMCampaign)
		assert.Equal(t, "www.partner.example", read.ReferrerHost)
	})

	t.Run("a form without create_lead still stores the attribution of a received submission", func(t *testing.T) {
		form := a.form("Newsletter signup", false, false)
		stored := a.submit(form, attributedRequest("nolead@example.com", "https://news.example/?utm_medium=Social", nil))
		assert.Nil(t, stored.LeadID)
		assert.Equal(t, "social", stored.UTMMedium)
		assert.Equal(t, "news.example", stored.ReferrerHost)
	})

	t.Run("a double opt-in submission is attributed once confirmed", func(t *testing.T) {
		form := a.form("Gated guide", true, true)
		pending := a.submit(form, attributedRequest("optin-attr@example.com",
			"https://guides.example/download?utm_campaign=Guide&utm_source=ignored",
			map[string]string{"utm_source": "Webinar"}))
		assert.Equal(t, models.FormSubmissionPending, pending.Status)
		assert.Equal(t, formAttribution{}, attributionOf(pending), "a pending submission carries no attribution yet")

		a.confirm("optin-attr@example.com")
		confirmed := a.submission(pending.ID)
		assert.Equal(t, models.FormSubmissionConfirmed, confirmed.Status)
		assert.Equal(t, formAttribution{UTMSource: "webinar", UTMCampaign: "guide", ReferrerHost: "guides.example"}, attributionOf(confirmed))
		require.NotNil(t, confirmed.LeadID)
		assert.Equal(t, "Gated guide", a.env.lead(*confirmed.LeadID).Source)
	})

	t.Run("a spam row stores empty attribution", func(t *testing.T) {
		form := a.form("Spam target", false, true)
		req := attributedRequest("bot@example.com", "https://spam.example/?utm_source=Bot&utm_campaign=Flood",
			map[string]string{"utm_medium": "Spam", "utm_content": "x"})
		req.Honeypot = "filled"
		spam := a.submit(form, req)
		assert.Equal(t, models.FormSubmissionSpam, spam.Status)
		assert.Equal(t, formAttribution{}, attributionOf(spam), "spam keeps empty attribution whatever it carried")
		assert.Equal(t, "Spam", spam.Data["utm_medium"], "the submitted values themselves are stored as before")
	})
}

// ------------------------------------------------------------------ AC-3 ---

func TestFormSubmittedEventSQLite(t *testing.T) {
	runFormSubmittedEvent(t, openAutomationSQLite(t))
}

func TestFormSubmittedEventMySQL(t *testing.T) {
	runFormSubmittedEvent(t, openAutomationMySQL(t, 2))
}

func runFormSubmittedEvent(t *testing.T, db *gorm.DB) {
	a := newAttributionFixture(t, db)
	form := a.form("Event form", false, true)

	assertFormSubmitted := func(t *testing.T, event models.AutomationEvent, leadID uint, submission *models.FormSubmission) {
		t.Helper()
		assert.Equal(t, models.AutomationEventLeadFormSubmitted, event.EventType)
		assert.Equal(t, models.AutomationEntityLead, event.EntityType)
		assert.Equal(t, leadID, event.EntityID)
		assert.Equal(t, models.AutomationOriginForm, event.Origin)
		require.NotNil(t, event.FormID)
		require.NotNil(t, event.SubmissionID)
		assert.Equal(t, submission.FormID, *event.FormID)
		assert.Equal(t, submission.ID, *event.SubmissionID)
		assert.Empty(t, event.FromValue)
		assert.Empty(t, event.ToValue)
		assert.Nil(t, event.ActorID)
	}

	t.Run("a new lead records lead_created then lead_form_submitted", func(t *testing.T) {
		submission := a.submit(form, attributedRequest("event@example.com", "https://site.example/?utm_campaign=One", nil))
		require.NotNil(t, submission.LeadID)
		events := a.env.eventsOf(models.AutomationEntityLead, *submission.LeadID)
		require.Equal(t, []string{models.AutomationEventLeadCreated, models.AutomationEventLeadFormSubmitted}, eventTypes(events))
		assertFormSubmitted(t, events[1], *submission.LeadID, submission)
	})

	t.Run("a repeat enquiry records lead_updated then lead_form_submitted", func(t *testing.T) {
		submission := a.submit(form, attributedRequest("event@example.com", "https://site.example/?utm_campaign=Two", nil))
		events := a.env.eventsOf(models.AutomationEntityLead, *submission.LeadID)
		require.Equal(t, []string{
			models.AutomationEventLeadCreated, models.AutomationEventLeadFormSubmitted,
			models.AutomationEventLeadUpdated, models.AutomationEventLeadFormSubmitted,
		}, eventTypes(events))
		assertFormSubmitted(t, events[3], *submission.LeadID, submission)
		assert.NotEqual(t, *events[1].SubmissionID, *events[3].SubmissionID, "each enquiry names its own submission")
	})

	t.Run("pending records none; the confirmation records exactly one", func(t *testing.T) {
		optIn := a.form("Event opt-in", true, true)
		before := len(a.eventsOfType(models.AutomationEventLeadFormSubmitted))
		pending := a.submit(optIn, attributedRequest("event-optin@example.com", "", nil))
		assert.Len(t, a.eventsOfType(models.AutomationEventLeadFormSubmitted), before, "a pending submission records no event")

		a.confirm("event-optin@example.com")
		confirmed := a.submission(pending.ID)
		require.NotNil(t, confirmed.LeadID)
		events := a.env.eventsOf(models.AutomationEntityLead, *confirmed.LeadID)
		require.Equal(t, []string{models.AutomationEventLeadCreated, models.AutomationEventLeadFormSubmitted}, eventTypes(events))
		assertFormSubmitted(t, events[1], *confirmed.LeadID, confirmed)
	})

	t.Run("spam and create_lead-off submissions record none", func(t *testing.T) {
		before := len(a.env.events())
		spamReq := attributedRequest("event@example.com", "https://site.example/?utm_campaign=Spam", nil)
		spamReq.Honeypot = "bot"
		a.submit(form, spamReq)
		noLead := a.form("Event no lead", false, false)
		a.submit(noLead, attributedRequest("event@example.com", "https://site.example/?utm_campaign=NoLead", nil))
		assert.Len(t, a.env.events(), before, "neither records any event")
	})

	t.Run("a write that rolls back records none", func(t *testing.T) {
		before := len(a.env.events())
		submissionsBefore := len(a.forms.submissions(t, form.ID))
		a.wire(failingSubmissionUpdateRepo{FormRepository: a.forms.repo})
		_, err := a.forms.service.SubmitPublic(context.Background(), form.PublicID,
			attributedRequest("event@example.com", "https://site.example/?utm_campaign=Rollback", nil), submissionMeta())
		a.wire(a.forms.repo)
		require.Error(t, err)
		assert.Len(t, a.env.events(), before, "the rolled-back link left no event")
		assert.Len(t, a.forms.submissions(t, form.ID), submissionsBefore, "nor a submission")
	})

	t.Run("every lead_form_submitted names one distinct submission", func(t *testing.T) {
		seen := map[uint]bool{}
		for _, event := range a.eventsOfType(models.AutomationEventLeadFormSubmitted) {
			require.NotNil(t, event.SubmissionID)
			assert.False(t, seen[*event.SubmissionID], "submission %d has more than one event", *event.SubmissionID)
			seen[*event.SubmissionID] = true
			stored := a.submission(*event.SubmissionID)
			require.NotNil(t, stored.LeadID)
			assert.Equal(t, *stored.LeadID, event.EntityID)
		}
		assert.Len(t, seen, 3)
		assertEventsHoldNoPersonalData(t, db, "event@example.com", "event-optin@example.com", "site.example", "utm_campaign")
	})
}

// ------------------------------------------------------------------ AC-4 ---

func TestAutomationFormSubmittedDispatchSQLite(t *testing.T) {
	runAutomationFormSubmittedDispatch(t, openAutomationSQLite(t))
}

func TestAutomationFormSubmittedDispatchMySQL(t *testing.T) {
	runAutomationFormSubmittedDispatch(t, openAutomationMySQL(t, 2))
}

func runAutomationFormSubmittedDispatch(t *testing.T, db *gorm.DB) {
	a := newAttributionFixture(t, db)
	env := a.env
	formA := a.form("Dispatch A", false, true)
	formB := a.form("Dispatch B", false, true)

	anyForm := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{}, waitStep(60))
	onlyA := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{FormID: formA.ID}, waitStep(60))
	onlyB := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{FormID: formB.ID}, waitStep(60))
	disabled := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{}, waitStep(60))
	_, err := env.svc.SetEnabled(disabled.ID, false)
	require.NoError(t, err)
	created := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadCreated, models.AutomationTrigger{}, waitStep(60))
	updated := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadUpdated, models.AutomationTrigger{}, waitStep(60))

	counts := func() map[string]int {
		return map[string]int{
			"any": len(env.runsOf(anyForm.ID)), "a": len(env.runsOf(onlyA.ID)), "b": len(env.runsOf(onlyB.ID)),
			"disabled": len(env.runsOf(disabled.ID)), "created": len(env.runsOf(created.ID)), "updated": len(env.runsOf(updated.ID)),
		}
	}

	t.Run("a new lead on form A", func(t *testing.T) {
		submission := a.submit(formA, attributedRequest("dispatch@example.com", "", nil))
		assert.Equal(t, 3, env.dispatch(), "lead_created, the any-form rule and the form A rule")
		assert.Equal(t, map[string]int{"any": 1, "a": 1, "b": 0, "disabled": 0, "created": 1, "updated": 0}, counts())
		for _, run := range env.runsOf(onlyA.ID) {
			assert.Equal(t, *submission.LeadID, run.EntityID)
			require.NotNil(t, run.EventID)
		}
	})

	t.Run("a repeat enquiry on form B", func(t *testing.T) {
		a.submit(formB, attributedRequest("dispatch@example.com", "", nil))
		assert.Equal(t, 3, env.dispatch(), "lead_updated, the any-form rule and the form B rule")
		assert.Equal(t, map[string]int{"any": 2, "a": 1, "b": 1, "disabled": 0, "created": 1, "updated": 1}, counts())
	})

	t.Run("an event without a form id reaches only the any-form rule", func(t *testing.T) {
		lead := env.createLead(env.sales, models.LeadStatusNew)
		env.recordEvent(models.AutomationEntityLead, lead.ID, models.AutomationEventLeadFormSubmitted, "", "", models.AutomationOriginForm)
		assert.Equal(t, 1, env.dispatch())
		assert.Equal(t, map[string]int{"any": 3, "a": 1, "b": 1, "disabled": 0, "created": 1, "updated": 1}, counts())
	})

	t.Run("lead_created and lead_updated rules start exactly as before", func(t *testing.T) {
		lead := env.createLead(env.sales, models.LeadStatusNew)
		env.recordEvent(models.AutomationEntityLead, lead.ID, models.AutomationEventLeadCreated, "", "", models.AutomationOriginUser)
		env.recordEvent(models.AutomationEntityLead, lead.ID, models.AutomationEventLeadUpdated, "", "", models.AutomationOriginUser)
		assert.Equal(t, 2, env.dispatch())
		assert.Equal(t, map[string]int{"any": 3, "a": 1, "b": 1, "disabled": 0, "created": 2, "updated": 2}, counts())
	})

	t.Run("a repeated dispatch starts nothing", func(t *testing.T) {
		assert.Equal(t, 0, env.dispatch())
		for _, run := range env.allRuns() {
			assert.Equal(t, models.AutomationEntityLead, run.EntityType)
		}
	})
}

// ------------------------------------------------------------------ AC-5 ---

func TestAutomationFormTriggerValidation(t *testing.T) {
	env := newAutomationEnv(t, openAutomationSQLite(t))
	lead := env.createLead(env.sales, models.LeadStatusNew)

	rule := func(entityType, triggerType string, trigger models.AutomationTrigger, steps ...models.AutomationStep) *models.AutomationRule {
		return &models.AutomationRule{Name: "Validation", EntityType: entityType, TriggerType: triggerType, Trigger: trigger, Steps: steps}
	}
	refusal := func(t *testing.T, err error) *RuleValidationError {
		t.Helper()
		var verr *RuleValidationError
		require.True(t, errors.As(err, &verr), "a rule validation error, got %v", err)
		return verr
	}
	fieldCodes := func(verr *RuleValidationError) map[string]string {
		out := map[string]string{}
		for _, field := range verr.Fields {
			out[field.Field] = field.Code
		}
		return out
	}

	t.Run("lead_form_submitted is a lead trigger, with or without form_id", func(t *testing.T) {
		require.NoError(t, env.svc.validateRule(rule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{}, waitStep(5))))
		require.NoError(t, env.svc.validateRule(rule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{FormID: 7}, waitStep(5))))
	})

	t.Run("a deal rule on lead_form_submitted is refused on trigger_type", func(t *testing.T) {
		verr := refusal(t, env.svc.validateRule(rule(models.AutomationEntityDeal, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{}, waitStep(5))))
		assert.Equal(t, "trigger_type", verr.Fields[0].Field)
		assert.Equal(t, RuleErrorInvalidValue, verr.Fields[0].Code)
	})

	t.Run("trigger.form_id is not_applicable on every other trigger type", func(t *testing.T) {
		for _, c := range []struct{ entity, trigger string }{
			{models.AutomationEntityLead, models.AutomationEventLeadCreated},
			{models.AutomationEntityLead, models.AutomationEventLeadUpdated},
			{models.AutomationEntityLead, models.AutomationEventLeadStatusChanged},
			{models.AutomationEntityLead, models.AutomationTriggerSchedule},
			{models.AutomationEntityDeal, models.AutomationEventDealCreated},
			{models.AutomationEntityDeal, models.AutomationEventDealUpdated},
			{models.AutomationEntityDeal, models.AutomationEventDealStageChanged},
			{models.AutomationEntityDeal, models.AutomationTriggerSchedule},
		} {
			verr := refusal(t, env.svc.validateRule(rule(c.entity, c.trigger, models.AutomationTrigger{FormID: 3}, waitStep(5))))
			assert.Equal(t, RuleErrorNotApplicable, fieldCodes(verr)["trigger.form_id"], "%s %s", c.entity, c.trigger)
		}
	})

	attributionFields := []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "referrer_host"}

	t.Run("the attribution fields are string fields of a lead_form_submitted lead rule", func(t *testing.T) {
		for _, field := range attributionFields {
			for _, step := range []models.AutomationStep{
				conditionStep(field, "eq", "spring", "", 0),
				conditionStep(field, "in", []string{"a", "b"}, "", 0),
				conditionStep(field, "contains", "x", "", 0),
				conditionStep(field, "is_empty", nil, "", 0),
			} {
				require.NoError(t, env.svc.validateRule(rule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{}, step)), "%s %s", field, step.Config.Operator)
			}
			verr := refusal(t, env.svc.validateRule(rule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{},
				conditionStep(field, "gt", 3, "", 0))))
			assert.Equal(t, RuleErrorInvalidValue, fieldCodes(verr)["steps[0].config.operator"], "%s is a string field", field)
		}
	})

	t.Run("the attribution fields are not_applicable on every other rule", func(t *testing.T) {
		for _, field := range attributionFields {
			for _, c := range []struct{ entity, trigger string }{
				{models.AutomationEntityLead, models.AutomationEventLeadCreated},
				{models.AutomationEntityLead, models.AutomationEventLeadUpdated},
				{models.AutomationEntityLead, models.AutomationEventLeadStatusChanged},
				{models.AutomationEntityLead, models.AutomationTriggerSchedule},
				{models.AutomationEntityDeal, models.AutomationEventDealCreated},
				{models.AutomationEntityDeal, models.AutomationTriggerSchedule},
			} {
				verr := refusal(t, env.svc.validateRule(rule(c.entity, c.trigger, models.AutomationTrigger{}, conditionStep(field, "eq", "x", "", 0))))
				assert.Equal(t, RuleErrorNotApplicable, fieldCodes(verr)["steps[0].config.field"], "%s on %s %s", field, c.entity, c.trigger)
			}
		}
	})

	t.Run("CreateRule and the draft dry run validate the same way", func(t *testing.T) {
		bad := rule(models.AutomationEntityLead, models.AutomationEventLeadCreated, models.AutomationTrigger{FormID: 2},
			conditionStep("utm_campaign", "eq", "spring", "", 0))
		createErr := env.svc.CreateRule(bad, env.admin.ID)
		created := fieldCodes(refusal(t, createErr))
		assert.Equal(t, RuleErrorNotApplicable, created["trigger.form_id"])
		assert.Equal(t, RuleErrorNotApplicable, created["steps[0].config.field"])

		_, draftErr := env.svc.DryRunDraft(rule(models.AutomationEntityLead, models.AutomationEventLeadCreated, models.AutomationTrigger{FormID: 2},
			conditionStep("utm_campaign", "eq", "spring", "", 0)), models.AutomationEntityLead, lead.ID)
		assert.Equal(t, created, fieldCodes(refusal(t, draftErr)))
		assert.Equal(t, createErr.Error(), draftErr.Error())

		_, err := env.svc.DryRunDraft(rule(models.AutomationEntityDeal, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{}, waitStep(5)),
			models.AutomationEntityDeal, 1)
		assert.Equal(t, "trigger_type", refusal(t, err).Fields[0].Field)

		report, err := env.svc.DryRunDraft(rule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{FormID: 2},
			conditionStep("utm_campaign", "is_empty", nil, "", 0)), models.AutomationEntityLead, lead.ID)
		require.NoError(t, err)
		assert.True(t, report.Draft)
		assert.Equal(t, int64(0), env.count(&models.AutomationRule{}), "nothing was saved")
	})
}

// ------------------------------------------------------------------ AC-6 ---

func TestAutomationAttributionConditionsSQLite(t *testing.T) {
	runAutomationAttributionConditions(t, openAutomationSQLite(t))
}

func TestAutomationAttributionConditionsMySQL(t *testing.T) {
	runAutomationAttributionConditions(t, openAutomationMySQL(t, 2))
}

func conditionResult(t *testing.T, env *automationEnv, run models.AutomationRun) bool {
	t.Helper()
	steps := env.stepsOf(run.ID)
	require.NotEmpty(t, steps, "run %d executed its condition", run.ID)
	require.Equal(t, models.AutomationStepCondition, steps[0].StepType)
	result, ok := steps[0].Details["result"].(bool)
	require.True(t, ok, "the condition logged its result")
	return result
}

func runAutomationAttributionConditions(t *testing.T, db *gorm.DB) {
	a := newAttributionFixture(t, db)
	env := a.env
	form := a.form("Campaign form", false, true)
	rule := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{FormID: form.ID},
		conditionStep("utm_campaign", "eq", "spring", "", 0), taskStep("", 0, 1))

	spring := a.submit(form, attributedRequest("branch@example.com", "https://site.example/?utm_campaign=Spring", nil))
	autumn := a.submit(form, attributedRequest("branch@example.com", "https://site.example/?utm_campaign=Autumn", nil))
	require.Equal(t, *spring.LeadID, *autumn.LeadID, "two enquiries to the same lead")
	assert.Equal(t, 2, env.dispatch(), "one run per enquiry")
	env.executeDue()

	runs := env.runsOf(rule.ID)
	require.Len(t, runs, 2)
	for _, run := range runs {
		assert.Equal(t, *spring.LeadID, run.EntityID)
		assert.Equal(t, models.AutomationRunCompleted, env.run(run.ID).Status)
	}
	assert.True(t, conditionResult(t, env, runs[0]), "the spring enquiry matches")
	assert.False(t, conditionResult(t, env, runs[1]), "the autumn enquiry does not")
	assert.Len(t, env.stepsOf(runs[0].ID), 2, "the matching run went on to create the task")
	assert.Len(t, env.stepsOf(runs[1].ID), 1, "the other run stopped at the condition")
	tasks := env.tasksList()
	require.Len(t, tasks, 1)
	require.NotNil(t, tasks[0].LeadID)
	assert.Equal(t, *spring.LeadID, *tasks[0].LeadID)

	t.Run("the submission is read at step time", func(t *testing.T) {
		third := a.submit(form, attributedRequest("branch@example.com", "https://site.example/?utm_campaign=Winter", nil))
		assert.Equal(t, 1, env.dispatch())
		// Changed after dispatch, before the step: the step sees the row as it
		// is when its transaction reads it.
		require.NoError(t, db.Model(&models.FormSubmission{}).Where("id = ?", third.ID).Update("utm_campaign", "spring").Error)
		env.executeDue()
		runs := env.runsOf(rule.ID)
		require.Len(t, runs, 3)
		assert.True(t, conditionResult(t, env, runs[2]))
	})

	t.Run("a submission of another lead reads empty", func(t *testing.T) {
		other := env.createLead(env.sales, models.LeadStatusNew)
		formID, submissionID := form.ID, spring.ID
		require.NoError(t, env.repo.RecordEvent(&models.AutomationEvent{
			EntityType: models.AutomationEntityLead, EntityID: other.ID, EventType: models.AutomationEventLeadFormSubmitted,
			Origin: models.AutomationOriginForm, FormID: &formID, SubmissionID: &submissionID, CreatedAt: time.Now().UTC(),
		}))
		assert.Equal(t, 1, env.dispatch())
		env.executeDue()
		runs := env.runsOf(rule.ID)
		require.Len(t, runs, 4)
		assert.Equal(t, other.ID, runs[3].EntityID)
		assert.False(t, conditionResult(t, env, runs[3]), "the spring submission belongs to another lead")
	})

	t.Run("an is_empty condition on a lead without a triggering submission", func(t *testing.T) {
		emptyRule := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{},
			conditionStep("referrer_host", "is_empty", nil, "", 0))
		other := env.createLead(env.sales, models.LeadStatusNew)
		env.recordEvent(models.AutomationEntityLead, other.ID, models.AutomationEventLeadFormSubmitted, "", "", models.AutomationOriginForm)
		env.dispatch()
		env.executeDue()
		runs := env.runsOf(emptyRule.ID)
		require.Len(t, runs, 1)
		assert.True(t, conditionResult(t, env, runs[0]), "no submission reads every attribution field empty")
	})
}

func TestAutomationDryRunAttributionEmpty(t *testing.T) {
	db := openAutomationSQLite(t)
	a := newAttributionFixture(t, db)
	env := a.env
	form := a.form("Dry run form", false, true)
	submission := a.submit(form, attributedRequest("dry@example.com", "https://www.example.com/?utm_campaign=Spring",
		map[string]string{"utm_source": "Newsletter"}))
	require.Equal(t, "spring", submission.UTMCampaign)
	rule := env.createRule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{FormID: form.ID},
		conditionStep("utm_campaign", "is_empty", nil, "", 0),
		conditionStep("utm_source", "eq", "newsletter", models.AutomationElseJump, 4),
		taskStep("", 0, 1),
		conditionStep("referrer_host", "is_empty", nil, "", 0))

	tables := []interface{}{
		&models.AutomationRun{}, &models.AutomationRunStep{}, &models.AutomationEvent{}, &models.Task{},
		&models.Notification{}, &models.FormSubmission{}, &models.Lead{}, &models.AutomationRule{},
	}
	snapshot := func() []int64 {
		out := make([]int64, 0, len(tables))
		for _, table := range tables {
			out = append(out, env.count(table))
		}
		return out
	}
	before := snapshot()
	storedBefore := attributionOf(a.submission(submission.ID))

	check := func(t *testing.T, report *models.AutomationDryRunReport) {
		t.Helper()
		require.Len(t, report.Steps, 3, "the jump skips the task")
		assert.Equal(t, true, report.Steps[0].Details["result"], "utm_campaign reads empty")
		assert.Equal(t, false, report.Steps[1].Details["result"], "utm_source reads empty")
		assert.Equal(t, models.AutomationElseJump, report.Steps[1].Details["branch"])
		assert.Equal(t, 4, report.Steps[2].Position)
		assert.Equal(t, true, report.Steps[2].Details["result"], "referrer_host reads empty")
	}

	report, err := env.svc.DryRun(rule.ID, models.AutomationEntityLead, *submission.LeadID)
	require.NoError(t, err)
	check(t, report)

	draft := &models.AutomationRule{
		Name: "Draft", EntityType: models.AutomationEntityLead, TriggerType: models.AutomationEventLeadFormSubmitted,
		Trigger: models.AutomationTrigger{FormID: form.ID}, Steps: rule.Steps,
	}
	for i := range draft.Steps {
		draft.Steps[i].ID, draft.Steps[i].RuleID = 0, 0
	}
	report, err = env.svc.DryRunDraft(draft, models.AutomationEntityLead, *submission.LeadID)
	require.NoError(t, err)
	check(t, report)

	assert.Equal(t, before, snapshot(), "the dry runs changed no row count")
	assert.Equal(t, storedBefore, attributionOf(a.submission(submission.ID)), "nor the submission")
	assert.Empty(t, env.runsOf(rule.ID))
}

// ------------------------------------------------------------------ AC-8 ---

func TestFormAttributionLogsOmitValues(t *testing.T) {
	db := openAutomationSQLite(t)
	a := newAttributionFixture(t, db)
	env := a.env

	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var output bytes.Buffer
	utils.Logger.SetOutput(&output)
	t.Cleanup(func() { utils.Logger = previous })

	received := a.form("Logs received", false, true)
	optIn := a.form("Logs opt-in", true, true)
	env.createRule(models.AutomationEntityLead, models.AutomationEventLeadFormSubmitted, models.AutomationTrigger{},
		conditionStep("utm_campaign", "eq", "canary-campaign-q2", "", 0), taskStep("", 0, 1))
	pageURL := "https://canary-host-q3.example/landing-canary?utm_medium=canary-medium-q4&utm_campaign=canary-campaign-q2"
	utm := map[string]string{"utm_source": "canary-source-q1", "utm_term": "canary-term-q5", "utm_content": "canary-content-q6"}

	first := a.submit(received, attributedRequest("logs@example.com", pageURL, utm))
	a.submit(received, attributedRequest("logs@example.com", pageURL, utm))
	a.submit(optIn, attributedRequest("logs-optin@example.com", pageURL, utm))
	a.confirm("logs-optin@example.com")
	spam := attributedRequest("logs-spam@example.com", pageURL, utm)
	spam.Honeypot = "bot"
	a.submit(received, spam)
	assert.Equal(t, 3, env.dispatch())
	env.executeDue()
	require.Equal(t, "canary-campaign-q2", first.UTMCampaign, "the canaries were stored")
	require.Len(t, env.tasksList(), 3, "every run branched on the attribution")

	text := output.String()
	require.NotEmpty(t, text)
	for _, needle := range []string{
		"canary-source-q1", "canary-medium-q4", "canary-campaign-q2", "canary-term-q5", "canary-content-q6",
		"canary-host-q3", "landing-canary", pageURL,
	} {
		assert.NotContainsf(t, text, needle, "the logs carry %q", needle)
	}

	// The existing id, status and code fields are still there.
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		msg, _ := entry["msg"].(string)
		switch msg {
		case "Form submission accepted":
			assert.Contains(t, entry, "submission_id")
			assert.Contains(t, entry, "status")
			assert.Contains(t, entry, "form_id")
		case "Form submission confirmed":
			assert.Contains(t, entry, "submission_id")
		case "Form submission rejected by a protection layer":
			assert.Equal(t, models.FormSpamReasonHoneypot, entry["spam_reason"])
		case "Automation runs started":
			assert.Contains(t, entry, "event_id")
			assert.Contains(t, entry, "runs")
		case "Automation step executed":
			assert.Contains(t, entry, "run_id")
			assert.Contains(t, entry, "outcome")
		}
		seen[msg] = true
	}
	for _, msg := range []string{
		"Form submission accepted", "Form submission confirmed", "Form submission rejected by a protection layer",
		"Automation runs started", "Automation step executed",
	} {
		assert.True(t, seen[msg], "the %q line was logged", msg)
	}
}
