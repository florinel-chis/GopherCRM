package service

import (
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/forms"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The forms tests run against real repositories on an in-memory database: the
// submission pipeline is mostly about what ends up stored, and a mocked
// repository would only be able to confirm that the service called the methods
// the test already expected it to call.

const (
	formTestSecret     = "form-service-test-secret-32-chars-min"
	formTestPublicBase = "https://crm.example.test"
	formTestAPIPrefix  = "/api/v1"
	formTestConfirmURL = formTestPublicBase + formTestAPIPrefix + "/forms/public/confirm"
)

type sentMail struct {
	To      string
	Subject string
	Body    string
}

// fakeFormMailer records deliveries instead of performing them. It is
// mutex-guarded because the service is free to send from any goroutine.
type fakeFormMailer struct {
	mu   sync.Mutex
	sent []sentMail
	err  error
}

func (m *fakeFormMailer) SendPasswordReset(to, resetURL string) error {
	return m.Send(to, "password reset", resetURL)
}

func (m *fakeFormMailer) Send(to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMail{To: to, Subject: subject, Body: body})
	return m.err
}

func (m *fakeFormMailer) messages() []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]sentMail, len(m.sent))
	copy(out, m.sent)
	return out
}

func (m *fakeFormMailer) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = nil
}

// to returns the recorded messages sent to one address.
func (m *fakeFormMailer) to(address string) []sentMail {
	var out []sentMail
	for _, msg := range m.messages() {
		if msg.To == address {
			out = append(out, msg)
		}
	}
	return out
}

type formFixture struct {
	db       *gorm.DB
	repo     repository.FormRepository
	leadRepo repository.LeadRepository
	userRepo repository.UserRepository
	mailer   *fakeFormMailer
	service  FormService
	owner    *models.User
}

func newFormFixture(t *testing.T, cfg config.FormsConfig) *formFixture {
	t.Helper()

	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})

	// The challenge signature and the confirmation-token hashes are keyed the
	// same way the session tokens are: API_KEY_SECRET when set, JWT_SECRET
	// otherwise. The first is blanked so a developer's environment cannot
	// change which key the service picks.
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", formTestSecret)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&models.User{},
		&models.Customer{},
		&models.Lead{},
		&models.Form{},
		&models.FormSubmission{},
		&models.FormConfirmationToken{},
	))

	f := &formFixture{
		db:       db,
		repo:     repository.NewFormRepository(db),
		leadRepo: repository.NewLeadRepository(db),
		userRepo: repository.NewUserRepository(db),
		mailer:   &fakeFormMailer{},
	}
	f.owner = f.createUser(t, "owner@example.com", models.RoleSales, true)
	f.service = NewFormService(f.repo, f.leadRepo, f.userRepo, f.mailer,
		utils.NewTransactionManager(db), cfg, formTestAPIPrefix)
	return f
}

func newDefaultFormFixture(t *testing.T) *formFixture {
	t.Helper()
	return newFormFixture(t, config.FormsConfig{PublicBaseURL: formTestPublicBase})
}

func (f *formFixture) createUser(t *testing.T, email string, role models.UserRole, active bool) *models.User {
	t.Helper()
	user := &models.User{
		Email:     email,
		Password:  "not-a-real-hash",
		FirstName: "Test",
		LastName:  "User",
		Role:      role,
		IsActive:  active,
	}
	require.NoError(t, f.db.Create(user).Error)
	if !active {
		// users.is_active carries a column default of true, and GORM
		// substitutes a column default for any field holding its zero value on
		// insert — so a deactivated account has to be written a second time.
		require.NoError(t, f.db.Model(user).Update("is_active", false).Error)
	}
	return user
}

// contactFormFields is the field set of the archetypal contact form: two lead
// columns, the mandatory address, a select and a free-text answer that has no
// column of its own.
func contactFormFields() []models.FormFieldDef {
	return []models.FormFieldDef{
		{Name: "first_name", Label: "First name", Type: models.FormFieldText},
		{Name: "last_name", Label: "Last name", Type: models.FormFieldText},
		{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
		{Name: "budget", Label: "Budget", Type: models.FormFieldSelect, Options: []string{"small", "large"}},
		{Name: "message", Label: "Message", Type: models.FormFieldTextarea, Required: true},
	}
}

func (f *formFixture) newForm() *models.Form {
	return &models.Form{
		Name:           "Contact us",
		Status:         models.FormStatusPublished,
		Fields:         contactFormFields(),
		NotifyEmails:   []string{"sales@example.com", "support@example.com"},
		CreateLead:     true,
		DefaultOwnerID: f.owner.ID,
	}
}

// publish stores a form through the service, so every stored form has been
// through the same validation and normalisation the API applies.
func (f *formFixture) publish(t *testing.T, form *models.Form) *models.Form {
	t.Helper()
	require.NoError(t, f.service.Create(form, f.owner.ID))
	return form
}

// challengeAged mints a challenge that was issued `age` ago.
func challengeAged(age time.Duration) string {
	return forms.NewChallenge([]byte(formTestSecret), time.Now().Add(-age))
}

func validSubmission() *PublicSubmissionRequest {
	return &PublicSubmissionRequest{
		Values: map[string]string{
			"first_name": "Ada",
			"last_name":  "Lovelace",
			"email":      "ada@example.com",
			"budget":     "large",
			"message":    "Please call me back.",
		},
		Challenge: challengeAged(30 * time.Second),
		PageURL:   "https://customer.example/contact",
	}
}

func submissionMeta() SubmissionMeta {
	return SubmissionMeta{IP: "203.0.113.7", UserAgent: "Mozilla/5.0", Origin: "https://customer.example"}
}

func (f *formFixture) submissions(t *testing.T, formID uint) []models.FormSubmission {
	t.Helper()
	list, _, err := f.repo.ListSubmissions(formID, 0, 100, "")
	require.NoError(t, err)
	return list
}

func (f *formFixture) leads(t *testing.T) []models.Lead {
	t.Helper()
	var leads []models.Lead
	require.NoError(t, f.db.Order("id asc").Find(&leads).Error)
	return leads
}

// tokenFromLink pulls the raw confirmation token out of a mailed link.
func tokenFromLink(t *testing.T, body string) string {
	t.Helper()
	index := strings.Index(body, formTestConfirmURL)
	require.NotEqual(t, -1, index, "confirmation mail must carry the confirmation link")

	link := body[index:]
	if end := strings.IndexAny(link, " \n\t"); end != -1 {
		link = link[:end]
	}
	parsed, err := url.Parse(link)
	require.NoError(t, err)

	token := parsed.Query().Get("token")
	require.NotEmpty(t, token)
	return token
}

// ------------------------------------------------------------------- CRUD ---

func TestFormServiceCreateAssignsPublicIdentity(t *testing.T) {
	f := newDefaultFormFixture(t)

	form := f.newForm()
	require.NoError(t, f.service.Create(form, f.owner.ID))

	assert.GreaterOrEqual(t, len(form.PublicID), 22, "the public id carries 128 bits of entropy")
	assert.NotContains(t, form.PublicID, "=", "the public id is URL-safe and unpadded")
	assert.Equal(t, f.owner.ID, form.CreatedByID)

	stored, err := f.service.GetByID(form.ID)
	require.NoError(t, err)
	assert.Equal(t, form.PublicID, stored.PublicID)
	assert.Len(t, stored.Fields, len(contactFormFields()))
	assert.Equal(t, models.FormSubmitActionMessage, stored.SubmitAction, "the submit action defaults to a message")
}

func TestFormServiceCreateAssignsDistinctPublicIDs(t *testing.T) {
	f := newDefaultFormFixture(t)

	first := f.publish(t, f.newForm())
	second := f.newForm()
	second.Name = "Second form"
	f.publish(t, second)

	assert.NotEqual(t, first.PublicID, second.PublicID)
}

func TestFormServiceCreateDefaultsStatusToDraft(t *testing.T) {
	f := newDefaultFormFixture(t)

	form := f.newForm()
	form.Status = ""
	require.NoError(t, f.service.Create(form, f.owner.ID))

	assert.Equal(t, models.FormStatusDraft, form.Status)
}

func TestFormServiceCreateRejectsInvalidDefinition(t *testing.T) {
	f := newDefaultFormFixture(t)

	cases := map[string]func(form *models.Form){
		"no fields": func(form *models.Form) { form.Fields = nil },
		"no email field": func(form *models.Form) {
			form.Fields = []models.FormFieldDef{{Name: "message", Label: "Message", Type: models.FormFieldTextarea}}
		},
		"duplicate field names": func(form *models.Form) {
			form.Fields = append(form.Fields, models.FormFieldDef{Name: "message", Label: "Again", Type: models.FormFieldText})
		},
		"select without options": func(form *models.Form) {
			form.Fields[3].Options = nil
		},
		"redirect without url": func(form *models.Form) {
			form.SubmitAction = models.FormSubmitActionRedirect
		},
		"lead without owner": func(form *models.Form) {
			form.DefaultOwnerID = 0
		},
		"unknown status": func(form *models.Form) {
			form.Status = models.FormStatus("live")
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			form := f.newForm()
			mutate(form)

			err := f.service.Create(form, f.owner.ID)

			require.Error(t, err)
			assert.ErrorIs(t, err, apperrors.ErrValidation)
			assert.Zero(t, form.ID, "a rejected definition is never stored")
		})
	}
}

func TestFormServiceCreateRejectsUnusableLeadOwner(t *testing.T) {
	f := newDefaultFormFixture(t)
	customer := f.createUser(t, "customer@example.com", models.RoleCustomer, true)
	inactive := f.createUser(t, "inactive@example.com", models.RoleSales, false)

	cases := map[string]uint{
		"customer role": customer.ID,
		"deactivated":   inactive.ID,
		"unknown user":  9999,
	}

	for name, ownerID := range cases {
		t.Run(name, func(t *testing.T) {
			form := f.newForm()
			form.DefaultOwnerID = ownerID

			err := f.service.Create(form, f.owner.ID)

			require.Error(t, err)
			assert.ErrorIs(t, err, apperrors.ErrValidation)

			var fieldErrors FieldErrors
			require.ErrorAs(t, err, &fieldErrors)
			assert.Contains(t, fieldErrors, "default_owner_id")
		})
	}
}

func TestFormServiceUpdateKeepsPublicIdentity(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.publish(t, f.newForm())

	update := f.newForm()
	update.Name = "Renamed"
	update.PublicID = "attacker-supplied"
	update.CreatedByID = 4242
	update.Status = models.FormStatusArchived

	require.NoError(t, f.service.Update(form.ID, update))

	stored, err := f.service.GetByID(form.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed", stored.Name)
	assert.Equal(t, models.FormStatusArchived, stored.Status)
	assert.Equal(t, form.PublicID, stored.PublicID, "the published address must keep working")
	assert.Equal(t, f.owner.ID, stored.CreatedByID, "authorship is an audit fact, not an input")
}

func TestFormServiceMissingFormIsNotFound(t *testing.T) {
	f := newDefaultFormFixture(t)

	_, err := f.service.GetByID(404)
	assert.True(t, apperrors.IsNotFound(err))

	assert.True(t, apperrors.IsNotFound(f.service.Update(404, f.newForm())))
	assert.True(t, apperrors.IsNotFound(f.service.Delete(404)))

	_, err = f.service.GetSubmission(404)
	assert.True(t, apperrors.IsNotFound(err))

	_, _, err = f.service.ListSubmissions(404, 0, 20, "")
	assert.True(t, apperrors.IsNotFound(err))
}

func TestFormServiceDeleteHidesTheFormFromThePublic(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.publish(t, f.newForm())

	require.NoError(t, f.service.Delete(form.ID))

	_, err := f.service.PublicDefinition(form.PublicID, "")
	assert.True(t, apperrors.IsNotFound(err))
}

func TestFormServiceListReportsSubmissionCounts(t *testing.T) {
	f := newDefaultFormFixture(t)
	busy := f.publish(t, f.newForm())
	quiet := f.newForm()
	quiet.Name = "Quiet form"
	f.publish(t, quiet)

	for i := 0; i < 2; i++ {
		_, err := f.service.SubmitPublic(busy.PublicID, validSubmission(), submissionMeta())
		require.NoError(t, err)
	}

	list, counts, total, err := f.service.List(0, 20, "", "name", "asc")
	require.NoError(t, err)
	assert.Len(t, list, 2)
	assert.EqualValues(t, 2, total)
	assert.EqualValues(t, 2, counts[busy.ID])
	assert.NotContains(t, counts, quiet.ID, "a form without submissions is absent from the map")

	list, _, total, err = f.service.List(0, 20, string(models.FormStatusDraft), "", "")
	require.NoError(t, err)
	assert.Empty(t, list)
	assert.EqualValues(t, 0, total)
}

func TestFormServiceListRejectsUnknownSortAndStatus(t *testing.T) {
	f := newDefaultFormFixture(t)

	_, _, _, err := f.service.List(0, 20, "", "name; DROP TABLE forms", "asc")
	assert.ErrorIs(t, err, apperrors.ErrValidation)

	_, _, _, err = f.service.List(0, 20, "everything", "", "")
	assert.ErrorIs(t, err, apperrors.ErrValidation)
}

// ------------------------------------------------------- public definition ---

func TestFormServicePublicDefinitionServesPublishedFormsOnly(t *testing.T) {
	f := newDefaultFormFixture(t)

	draft := f.newForm()
	draft.Status = models.FormStatusDraft
	f.publish(t, draft)

	_, err := f.service.PublicDefinition(draft.PublicID, "")
	assert.True(t, apperrors.IsNotFound(err))

	_, err = f.service.PublicDefinition("no-such-form", "")
	assert.True(t, apperrors.IsNotFound(err))
}

func TestFormServicePublicDefinitionCarriesRenderingContract(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.publish(t, f.newForm())

	definition, err := f.service.PublicDefinition(form.PublicID, "https://customer.example")
	require.NoError(t, err)

	assert.Equal(t, form.Name, definition.Name)
	assert.Equal(t, form.PublicID, definition.PublicID)
	assert.Len(t, definition.Fields, len(contactFormFields()))
	assert.Equal(t, models.FormSubmitActionMessage, definition.SubmitAction)
	assert.Equal(t, "website_url_confirm", definition.HoneypotField)
	assert.NotEmpty(t, definition.Challenge)
	assert.Empty(t, definition.RecaptchaSiteKey, "no site key is configured")

	age, err := forms.ChallengeAge([]byte(formTestSecret), definition.Challenge, time.Now())
	require.NoError(t, err)
	assert.Less(t, age, time.Second, "the challenge is minted when the definition is handed out")
}

func TestFormServicePublicDefinitionLeaksNothingInternal(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.ConfirmationSubject = "internal confirmation subject"
	form.FollowUpBody = "internal follow-up body"
	form.ContentURL = "https://files.example/secret.pdf"
	form.AllowedDomains = []string{"customer.example"}
	f.publish(t, form)

	definition, err := f.service.PublicDefinition(form.PublicID, "https://customer.example")
	require.NoError(t, err)

	encoded, err := json.Marshal(definition)
	require.NoError(t, err)
	rendered := string(encoded)

	for _, secret := range []string{
		"sales@example.com", "support@example.com",
		"internal confirmation subject", "internal follow-up body",
		"https://files.example/secret.pdf", "customer.example",
		"default_owner_id", "created_by_id",
	} {
		assert.NotContains(t, rendered, secret)
	}
}

func TestFormServicePublicDefinitionOffersSiteKeyOnlyWhenUsable(t *testing.T) {
	withKeys := config.FormsConfig{
		PublicBaseURL:     formTestPublicBase,
		RecaptchaSiteKey:  "site-key",
		RecaptchaSecret:   "secret-key",
		RecaptchaMinScore: 0.5,
	}

	t.Run("form asks and server can", func(t *testing.T) {
		f := newFormFixture(t, withKeys)
		form := f.newForm()
		form.CaptchaEnabled = true
		f.publish(t, form)

		definition, err := f.service.PublicDefinition(form.PublicID, "")
		require.NoError(t, err)
		assert.Equal(t, "site-key", definition.RecaptchaSiteKey)
	})

	t.Run("form does not ask", func(t *testing.T) {
		f := newFormFixture(t, withKeys)
		form := f.publish(t, f.newForm())

		definition, err := f.service.PublicDefinition(form.PublicID, "")
		require.NoError(t, err)
		assert.Empty(t, definition.RecaptchaSiteKey)
	})

	t.Run("server has no keys", func(t *testing.T) {
		f := newDefaultFormFixture(t)
		form := f.newForm()
		form.CaptchaEnabled = true
		f.publish(t, form)

		definition, err := f.service.PublicDefinition(form.PublicID, "")
		require.NoError(t, err)
		assert.Empty(t, definition.RecaptchaSiteKey)
	})
}

func TestFormServicePublicDefinitionHonoursTheOriginAllowlist(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.AllowedDomains = []string{"Customer.Example", "localhost:5173"}
	f.publish(t, form)

	allowed := []string{"https://customer.example", "http://CUSTOMER.EXAMPLE", "http://localhost:5173"}
	for _, origin := range allowed {
		_, err := f.service.PublicDefinition(form.PublicID, origin)
		assert.NoError(t, err, origin)
	}

	// The match is exact on host[:port]: a subdomain, a different port and a
	// missing Origin header are all misses.
	rejected := []string{"https://www.customer.example", "https://customer.example:8443", "https://elsewhere.example", ""}
	for _, origin := range rejected {
		_, err := f.service.PublicDefinition(form.PublicID, origin)
		assert.True(t, apperrors.IsNotFound(err), origin)
	}
}

// ------------------------------------------------------ submission values ---

func TestFormServiceSubmitRejectsInvalidValues(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.ConsentText = "I agree to be contacted."
	f.publish(t, form)

	cases := map[string]struct {
		mutate func(req *PublicSubmissionRequest)
		field  string
	}{
		"missing required": {
			mutate: func(req *PublicSubmissionRequest) { delete(req.Values, "message") },
			field:  "message",
		},
		"malformed address": {
			mutate: func(req *PublicSubmissionRequest) { req.Values["email"] = "ada(at)example.com" },
			field:  "email",
		},
		"value outside the offered options": {
			mutate: func(req *PublicSubmissionRequest) { req.Values["budget"] = "enormous" },
			field:  "budget",
		},
		"unknown field": {
			mutate: func(req *PublicSubmissionRequest) { req.Values["is_admin"] = "true" },
			field:  "is_admin",
		},
		"over-long value": {
			mutate: func(req *PublicSubmissionRequest) {
				req.Values["first_name"] = strings.Repeat("a", models.FormDefaultMaxLength+1)
			},
			field: "first_name",
		},
		"consent withheld": {
			mutate: func(req *PublicSubmissionRequest) { req.Consent = false },
			field:  "consent",
		},
		"missing challenge": {
			mutate: func(req *PublicSubmissionRequest) { req.Challenge = "" },
			field:  "challenge",
		},
		"forged challenge": {
			mutate: func(req *PublicSubmissionRequest) { req.Challenge = challengeAged(time.Minute) + "0" },
			field:  "challenge",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f.mailer.reset()
			req := validSubmission()
			req.Consent = true
			tc.mutate(req)

			outcome, err := f.service.SubmitPublic(form.PublicID, req, submissionMeta())

			require.Error(t, err)
			assert.Nil(t, outcome)
			assert.ErrorIs(t, err, apperrors.ErrValidation)

			var fieldErrors FieldErrors
			require.ErrorAs(t, err, &fieldErrors)
			assert.Contains(t, fieldErrors, tc.field)

			assert.Empty(t, f.submissions(t, form.ID), "a request that never passed validation is not stored")
			assert.Empty(t, f.mailer.messages())
		})
	}
}

func TestFormServiceSubmitNormalisesStoredValues(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.Fields = append(form.Fields, models.FormFieldDef{
		Name: "newsletter", Label: "Newsletter", Type: models.FormFieldCheckbox,
	})
	f.publish(t, form)

	req := validSubmission()
	req.Values["email"] = "  Ada@Example.COM  "
	req.Values["newsletter"] = "on"

	_, err := f.service.SubmitPublic(form.PublicID, req, submissionMeta())
	require.NoError(t, err)

	stored := f.submissions(t, form.ID)
	require.Len(t, stored, 1)
	assert.Equal(t, "ada@example.com", stored[0].Email)
	assert.Equal(t, "ada@example.com", stored[0].Data["email"])
	assert.Equal(t, "true", stored[0].Data["newsletter"], "checkbox values are stored as true/false")
	assert.Equal(t, "203.0.113.7", stored[0].IPAddress)
	assert.Equal(t, "Mozilla/5.0", stored[0].UserAgent)
	assert.Equal(t, "https://customer.example/contact", stored[0].Referrer)
}

func TestFormServiceSubmitStoresUntickedCheckboxAsFalse(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.Fields = append(form.Fields, models.FormFieldDef{
		Name: "newsletter", Label: "Newsletter", Type: models.FormFieldCheckbox,
	})
	f.publish(t, form)

	_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)

	stored := f.submissions(t, form.ID)
	require.Len(t, stored, 1)
	assert.Equal(t, "false", stored[0].Data["newsletter"])
}

func TestFormServiceSubmitRejectsUnpublishedForm(t *testing.T) {
	f := newDefaultFormFixture(t)
	draft := f.newForm()
	draft.Status = models.FormStatusDraft
	f.publish(t, draft)

	_, err := f.service.SubmitPublic(draft.PublicID, validSubmission(), submissionMeta())
	assert.True(t, apperrors.IsNotFound(err))
}

// ------------------------------------------------------------ spam layers ---

func TestFormServiceSpamLayersAreStoredAndAnsweredLikeSuccess(t *testing.T) {
	cases := map[string]struct {
		configure func(form *models.Form)
		mutate    func(req *PublicSubmissionRequest, meta *SubmissionMeta)
		reason    string
		withKeys  bool
	}{
		"submitted too fast": {
			mutate: func(req *PublicSubmissionRequest, _ *SubmissionMeta) {
				req.Challenge = challengeAged(time.Second)
			},
			reason: models.FormSpamReasonTimeTrap,
		},
		"challenge harvested a day ago": {
			mutate: func(req *PublicSubmissionRequest, _ *SubmissionMeta) {
				req.Challenge = challengeAged(25 * time.Hour)
			},
			reason: models.FormSpamReasonTimeTrap,
		},
		"origin outside the allowlist": {
			configure: func(form *models.Form) { form.AllowedDomains = []string{"customer.example"} },
			mutate: func(_ *PublicSubmissionRequest, meta *SubmissionMeta) {
				meta.Origin = "https://scraper.example"
			},
			reason: models.FormSpamReasonDomain,
		},
		"honeypot filled in": {
			mutate: func(req *PublicSubmissionRequest, _ *SubmissionMeta) {
				req.Honeypot = "https://buy-now.example"
			},
			reason: models.FormSpamReasonHoneypot,
		},
		"captcha token missing": {
			configure: func(form *models.Form) { form.CaptchaEnabled = true },
			mutate:    func(_ *PublicSubmissionRequest, _ *SubmissionMeta) {},
			reason:    models.FormSpamReasonCaptcha,
			withKeys:  true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := config.FormsConfig{PublicBaseURL: formTestPublicBase}
			if tc.withKeys {
				cfg.RecaptchaSiteKey = "site-key"
				cfg.RecaptchaSecret = "secret-key"
				cfg.RecaptchaMinScore = 0.5
			}
			f := newFormFixture(t, cfg)

			form := f.newForm()
			if tc.configure != nil {
				tc.configure(form)
			}
			f.publish(t, form)

			req := validSubmission()
			meta := submissionMeta()
			tc.mutate(req, &meta)

			outcome, err := f.service.SubmitPublic(form.PublicID, req, meta)

			require.NoError(t, err, "a spam submission is answered exactly like a genuine one")
			require.NotNil(t, outcome)
			assert.Equal(t, &SubmitOutcome{
				Action:  models.FormSubmitActionMessage,
				Message: formDefaultThankYouMessage,
			}, outcome)

			stored := f.submissions(t, form.ID)
			require.Len(t, stored, 1)
			assert.Equal(t, models.FormSubmissionSpam, stored[0].Status)
			assert.Equal(t, tc.reason, stored[0].SpamReason)
			assert.Equal(t, "ada@example.com", stored[0].Email)
			assert.Nil(t, stored[0].LeadID)

			assert.Empty(t, f.mailer.messages(), "spam never triggers mail")
			assert.Empty(t, f.leads(t), "spam never becomes a lead")
		})
	}
}

func TestFormServiceSpamLayerOrderIsFixed(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.AllowedDomains = []string{"customer.example"}
	f.publish(t, form)

	// Every layer after the time trap trips as well; the first one still wins.
	req := validSubmission()
	req.Challenge = challengeAged(time.Second)
	req.Honeypot = "spam"
	meta := submissionMeta()
	meta.Origin = "https://scraper.example"

	_, err := f.service.SubmitPublic(form.PublicID, req, meta)
	require.NoError(t, err)

	stored := f.submissions(t, form.ID)
	require.Len(t, stored, 1)
	assert.Equal(t, models.FormSpamReasonTimeTrap, stored[0].SpamReason)
}

func TestFormServiceCaptchaLayerIsSkippedWithoutServerKeys(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.CaptchaEnabled = true
	f.publish(t, form)

	_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)

	stored := f.submissions(t, form.ID)
	require.Len(t, stored, 1)
	assert.Equal(t, models.FormSubmissionReceived, stored[0].Status, "a form cannot demand a check the server cannot run")
}

// ------------------------------------------------------- clean submissions ---

func TestFormServiceSubmitCreatesLeadAndNotifies(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.publish(t, f.newForm())

	outcome, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	assert.Equal(t, models.FormSubmitActionMessage, outcome.Action)
	assert.Equal(t, formDefaultThankYouMessage, outcome.Message)
	assert.False(t, outcome.PendingConfirmation)

	stored := f.submissions(t, form.ID)
	require.Len(t, stored, 1)
	assert.Equal(t, models.FormSubmissionReceived, stored[0].Status)
	require.NotNil(t, stored[0].LeadID)

	leads := f.leads(t)
	require.Len(t, leads, 1)
	lead := leads[0]
	assert.Equal(t, *stored[0].LeadID, lead.ID)
	assert.Equal(t, "Ada", lead.FirstName)
	assert.Equal(t, "Lovelace", lead.LastName)
	assert.Equal(t, "ada@example.com", lead.Email)
	assert.Equal(t, models.LeadStatusNew, lead.Status)
	assert.Equal(t, f.owner.ID, lead.OwnerID)
	assert.Equal(t, form.Name, lead.Source)
	assert.Contains(t, lead.Notes, "--- Form submission: Contact us (")
	assert.Contains(t, lead.Notes, "Message: Please call me back.")
	assert.Contains(t, lead.Notes, "Budget: large")
	assert.NotContains(t, lead.Notes, "Email: ada@example.com", "fields with a column of their own stay out of the notes")

	for _, recipient := range []string{"sales@example.com", "support@example.com"} {
		notifications := f.mailer.to(recipient)
		require.Len(t, notifications, 1, recipient)
		assert.Equal(t, "New submission: Contact us", notifications[0].Subject)
		assert.Contains(t, notifications[0].Body, "Email: ada@example.com")
		assert.Contains(t, notifications[0].Body, "Message: Please call me back.")
	}
	assert.Empty(t, f.mailer.to("ada@example.com"), "a form without a follow-up mails nobody else")
}

func TestFormServiceSubmitDeduplicatesLeadsByAddress(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.publish(t, f.newForm())

	_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)

	second := validSubmission()
	second.Values["message"] = "Following up on my earlier note."
	_, err = f.service.SubmitPublic(form.PublicID, second, submissionMeta())
	require.NoError(t, err)

	leads := f.leads(t)
	require.Len(t, leads, 1, "a second submission from the same address appends to the existing lead")
	assert.Contains(t, leads[0].Notes, "Message: Please call me back.")
	assert.Contains(t, leads[0].Notes, "Message: Following up on my earlier note.")

	stored := f.submissions(t, form.ID)
	require.Len(t, stored, 2)
	for _, submission := range stored {
		require.NotNil(t, submission.LeadID)
		assert.Equal(t, leads[0].ID, *submission.LeadID)
	}
}

func TestFormServiceSubmitWithoutLeadCreation(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.CreateLead = false
	form.DefaultOwnerID = 0
	f.publish(t, form)

	_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)

	assert.Empty(t, f.leads(t))
	stored := f.submissions(t, form.ID)
	require.Len(t, stored, 1)
	assert.Nil(t, stored[0].LeadID)
	assert.Equal(t, models.FormSubmissionReceived, stored[0].Status)
}

func TestFormServiceSubmitSplitsSingleNameField(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.Fields = []models.FormFieldDef{
		{Name: "name", Label: "Name", Type: models.FormFieldText, Required: true},
		{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
	}
	f.publish(t, form)

	_, err := f.service.SubmitPublic(form.PublicID, &PublicSubmissionRequest{
		Values:    map[string]string{"name": "Grace Brewster Hopper", "email": "grace@example.com"},
		Challenge: challengeAged(30 * time.Second),
	}, submissionMeta())
	require.NoError(t, err)

	leads := f.leads(t)
	require.Len(t, leads, 1)
	assert.Equal(t, "Grace", leads[0].FirstName)
	assert.Equal(t, "Brewster Hopper", leads[0].LastName)
	assert.NotContains(t, leads[0].Notes, "Name: Grace", "a name that became the lead's own stays out of the notes")
}

func TestFormServiceSubmitSplitsSingleWordName(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.Fields = []models.FormFieldDef{
		{Name: "name", Label: "Name", Type: models.FormFieldText, Required: true},
		{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
	}
	f.publish(t, form)

	_, err := f.service.SubmitPublic(form.PublicID, &PublicSubmissionRequest{
		Values:    map[string]string{"name": "Ada", "email": "lovelace@example.com"},
		Challenge: challengeAged(30 * time.Second),
	}, submissionMeta())
	require.NoError(t, err)

	leads := f.leads(t)
	require.Len(t, leads, 1)
	assert.Equal(t, "Ada", leads[0].FirstName)
	assert.Equal(t, "lovelace", leads[0].LastName, "the address local part stands in for a missing surname")
}

func TestFormServiceSubmitFallsBackToUsableLeadNames(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.Fields = []models.FormFieldDef{
		{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
	}
	f.publish(t, form)

	_, err := f.service.SubmitPublic(form.PublicID, &PublicSubmissionRequest{
		Values:    map[string]string{"email": "ada@example.com"},
		Challenge: challengeAged(30 * time.Second),
	}, submissionMeta())
	require.NoError(t, err)

	leads := f.leads(t)
	require.Len(t, leads, 1)
	assert.Equal(t, "Form", leads[0].FirstName)
	assert.Equal(t, "ada", leads[0].LastName, "the address local part stands in for a missing surname")
}

func TestFormServiceSubmitRedirectOutcome(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.SubmitAction = models.FormSubmitActionRedirect
	form.RedirectURL = "https://customer.example/thanks"
	f.publish(t, form)

	outcome, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	assert.Equal(t, models.FormSubmitActionRedirect, outcome.Action)
	assert.Equal(t, "https://customer.example/thanks", outcome.RedirectURL)
	assert.Empty(t, outcome.Message)
}

func TestFormServiceSubmitSendsImmediateFollowUpWhenNotOptIn(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.newForm()
	form.FollowUpSubject = "Your download"
	form.FollowUpBody = "Here you go: {content_link}"
	form.ContentURL = "https://files.example/guide.pdf"
	f.publish(t, form)

	_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)

	followUps := f.mailer.to("ada@example.com")
	require.Len(t, followUps, 1)
	assert.Equal(t, "Your download", followUps[0].Subject)
	assert.Equal(t, "Here you go: https://files.example/guide.pdf", followUps[0].Body)
}

func TestFormServiceSubmitSurvivesMailFailure(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.publish(t, f.newForm())
	f.mailer.err = assert.AnError

	_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())

	require.NoError(t, err, "mail is a side effect of a submission that is already stored")
	assert.Len(t, f.submissions(t, form.ID), 1)
}

// --------------------------------------------------------- double opt-in ---

func (f *formFixture) optInForm() *models.Form {
	form := f.newForm()
	form.DoubleOptIn = true
	form.FollowUpSubject = "Your guide"
	form.FollowUpBody = "Thanks for confirming. Download it here: {content_link}"
	form.ContentURL = "https://files.example/guide.pdf"
	return form
}

func TestFormServiceOptInDefersEverythingToConfirmation(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.publish(t, f.optInForm())

	outcome, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	assert.True(t, outcome.PendingConfirmation)
	assert.Equal(t, formDefaultPendingMessage, outcome.Message)

	stored := f.submissions(t, form.ID)
	require.Len(t, stored, 1)
	assert.Equal(t, models.FormSubmissionPending, stored[0].Status)
	assert.Nil(t, stored[0].LeadID)
	assert.Nil(t, stored[0].ConfirmedAt)
	assert.Empty(t, f.leads(t), "a pending submission is not a lead")

	messages := f.mailer.messages()
	require.Len(t, messages, 1, "only the confirmation mail goes out before the address is proven")
	assert.Equal(t, "ada@example.com", messages[0].To)
	assert.Equal(t, formDefaultConfirmationSubject, messages[0].Subject)
	assert.NotEmpty(t, tokenFromLink(t, messages[0].Body))
}

func TestFormServiceConfirmCompletesTheSubmission(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.publish(t, f.optInForm())

	_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	token := tokenFromLink(t, f.mailer.messages()[0].Body)
	f.mailer.reset()

	require.NoError(t, f.service.ConfirmSubmission(token))

	stored := f.submissions(t, form.ID)
	require.Len(t, stored, 1)
	assert.Equal(t, models.FormSubmissionConfirmed, stored[0].Status)
	require.NotNil(t, stored[0].ConfirmedAt)
	require.NotNil(t, stored[0].LeadID)

	leads := f.leads(t)
	require.Len(t, leads, 1)
	assert.Equal(t, "ada@example.com", leads[0].Email)
	assert.Equal(t, f.owner.ID, leads[0].OwnerID)

	followUps := f.mailer.to("ada@example.com")
	require.Len(t, followUps, 1)
	assert.Equal(t, "Your guide", followUps[0].Subject)
	assert.Contains(t, followUps[0].Body, "https://files.example/guide.pdf")
	assert.NotContains(t, followUps[0].Body, "{content_link}")

	assert.Len(t, f.mailer.to("sales@example.com"), 1, "the team is told once the address is proven")
}

func TestFormServiceConfirmIsSingleUse(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.publish(t, f.optInForm())

	_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	token := tokenFromLink(t, f.mailer.messages()[0].Body)

	require.NoError(t, f.service.ConfirmSubmission(token))

	err = f.service.ConfirmSubmission(token)
	assert.ErrorIs(t, err, ErrInvalidConfirmationToken)
	assert.Len(t, f.leads(t), 1, "a second click creates nothing")
}

func TestFormServiceConfirmRejectsUnusableTokens(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.publish(t, f.optInForm())

	_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	stored := f.submissions(t, form.ID)
	require.Len(t, stored, 1)

	expired := "expired-raw-token-value"
	require.NoError(t, f.repo.CreateConfirmationToken(&models.FormConfirmationToken{
		SubmissionID: stored[0].ID,
		TokenHash:    hashOpaqueToken(expired, formTestSecret),
		ExpiresAt:    time.Now().Add(-time.Minute),
	}))

	for name, token := range map[string]string{
		"empty":   "",
		"unknown": "never-issued",
		"expired": expired,
	} {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, f.service.ConfirmSubmission(token), ErrInvalidConfirmationToken)
		})
	}

	assert.Equal(t, models.FormSubmissionPending, f.submissions(t, form.ID)[0].Status)
}

func TestFormServiceResubmitInvalidatesTheEarlierLink(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.publish(t, f.optInForm())

	_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	firstToken := tokenFromLink(t, f.mailer.messages()[0].Body)
	f.mailer.reset()

	_, err = f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)
	secondToken := tokenFromLink(t, f.mailer.messages()[0].Body)
	require.NotEqual(t, firstToken, secondToken)

	assert.ErrorIs(t, f.service.ConfirmSubmission(firstToken), ErrInvalidConfirmationToken)
	require.NoError(t, f.service.ConfirmSubmission(secondToken))
}

func TestFormServiceConfirmationMailUsesTheFormsOwnCopy(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.optInForm()
	form.ConfirmationSubject = "Please confirm your request"
	form.ConfirmationBody = "Hi there,\n\nConfirm here: {confirmation_link}\n"
	f.publish(t, form)

	_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)

	messages := f.mailer.messages()
	require.Len(t, messages, 1)
	assert.Equal(t, "Please confirm your request", messages[0].Subject)
	assert.Contains(t, messages[0].Body, "Hi there,")
	assert.NotContains(t, messages[0].Body, "{confirmation_link}")
	assert.Contains(t, messages[0].Body, formTestConfirmURL+"?token=")

	// The link must still be usable when it comes out of a hand-written body.
	require.NoError(t, f.service.ConfirmSubmission(tokenFromLink(t, messages[0].Body)))
}

func TestFormServiceConfirmationMailAppendsAMissingPlaceholder(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.optInForm()
	form.ConfirmationBody = "Someone forgot the link."
	f.publish(t, form)

	_, err := f.service.SubmitPublic(form.PublicID, validSubmission(), submissionMeta())
	require.NoError(t, err)

	messages := f.mailer.messages()
	require.Len(t, messages, 1)
	assert.Contains(t, messages[0].Body, "Someone forgot the link.")
	require.NoError(t, f.service.ConfirmSubmission(tokenFromLink(t, messages[0].Body)))
}

func TestFormServiceOptInSpamNeverMintsAToken(t *testing.T) {
	f := newDefaultFormFixture(t)
	form := f.publish(t, f.optInForm())

	req := validSubmission()
	req.Honeypot = "spam"

	outcome, err := f.service.SubmitPublic(form.PublicID, req, submissionMeta())
	require.NoError(t, err)
	assert.True(t, outcome.PendingConfirmation, "the spam answer is shaped exactly like the genuine one")

	assert.Empty(t, f.mailer.messages())

	var tokens int64
	require.NoError(t, f.db.Model(&models.FormConfirmationToken{}).Count(&tokens).Error)
	assert.Zero(t, tokens)
}
