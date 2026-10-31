package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/forms"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// ErrInvalidConfirmationToken covers every rejection of a double-opt-in
// confirmation token: unknown, already spent, expired, or pointing at a
// submission or form that no longer exists. It is deliberately generic, like
// ErrInvalidResetToken: whoever clicks a confirmation link must not be able to
// tell those cases apart.
var ErrInvalidConfirmationToken = errors.New("invalid or expired confirmation token")

const (
	// formHoneypotField is the name of the decoy input the renderer hides from
	// human visitors. It is part of the public contract: the definition
	// response names it and the submission carries it back.
	formHoneypotField = "website_url_confirm"

	// A form cannot be filled in honestly in under a few seconds, and a
	// definition handed out yesterday is not what a real visitor is looking at.
	formChallengeMinAge = 3 * time.Second
	formChallengeMaxAge = 24 * time.Hour

	// formConfirmationTTL is how long a confirmation link stays clickable.
	// Longer than a password reset by design: the visitor is not waiting at the
	// keyboard, and an expired link costs them the content they asked for.
	formConfirmationTTL = 48 * time.Hour

	// formPublicIDBytes is the entropy of a form's public identifier (128 bits,
	// 22 characters once base64url-encoded), and formPublicIDAttempts bounds
	// the search for an unused one.
	formPublicIDBytes    = 16
	formPublicIDAttempts = 5

	// formLeadSourceMaxLength mirrors the varchar(100) leads.source column.
	formLeadSourceMaxLength = 100

	// Placeholders a form's mail bodies may use.
	formConfirmationLinkPlaceholder = "{confirmation_link}"
	formContentLinkPlaceholder      = "{content_link}"
)

// Default copy, used whenever a form leaves the corresponding field empty.
const (
	formDefaultThankYouMessage = "Thank you. Your submission has been received."
	formDefaultPendingMessage  = "Thank you. Please check your inbox and confirm your email address to complete your submission."

	formDefaultConfirmationSubject = "Confirm your email address"
	formDefaultConfirmationBody    = "Hello,\n\n" +
		"Please confirm your email address to complete your submission:\n\n" +
		formConfirmationLinkPlaceholder + "\n\n" +
		"If you did not fill in this form, you can ignore this message.\n"
	formDefaultFollowUpBody = "Hello,\n\n" +
		"Thank you for confirming your email address. Here is the link you asked for:\n\n" +
		formContentLinkPlaceholder + "\n"
)

// formEmailPattern validates a submitted address. It is the same permissive
// shape the form definition applies to notification addresses: anything
// stricter rejects more valid addresses than it catches typos, and the double
// opt-in flow is what actually proves an address exists.
var formEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// leadNameMaxLength is the width of leads.first_name and leads.last_name,
// which the split "name" field and the stand-in surname are clipped to.
var leadNameMaxLength = mustFormColumnLimit("last_name")

// mustFormColumnLimit reads a column width that the service depends on; a
// missing entry is a programming error that must stop start-up rather than
// clip every name to nothing.
func mustFormColumnLimit(name string) int {
	limit, ok := models.FormFieldColumnLimit(name)
	if !ok || limit <= 0 {
		panic(fmt.Sprintf("form service: no column limit for %q", name))
	}
	return limit
}

// formSortColumns is what a caller may sort the form list by. The repository
// keeps the authoritative allowlist — it is the guard that stands between a
// query string and an ORDER BY clause — and this copy exists so a rejected
// column comes back as a bad request instead of an unclassifiable error.
var formSortColumns = map[string]bool{
	"id":         true,
	"name":       true,
	"status":     true,
	"created_at": true,
	"updated_at": true,
}

// formLeadFields are the submitted field names that map onto lead columns.
// Everything else a form collects ends up in the lead's notes. A lone "name"
// field is split into the two name columns, so it belongs here too.
var formLeadFields = map[string]bool{
	"name":       true,
	"first_name": true,
	"last_name":  true,
	"email":      true,
	"phone":      true,
	"company":    true,
	"position":   true,
}

// PublicFormDefinition is everything a public visitor is told about a form.
// It is built field by field rather than by marshalling the model, so a column
// added to Form later — notification addresses, mail bodies, the lead owner —
// cannot leak by default.
type PublicFormDefinition struct {
	// Form name, for the heading the renderer shows.
	Name string `json:"name" example:"Website contact form"`
	// Public identifier of the form (the {key} of the public URLs).
	PublicID string `json:"public_id" example:"q3VZr0n8TjK1bWcY5xL2aQ"`
	// Ordered field definitions; max_length is the limit the submission
	// validator enforces. Always an array.
	Fields []models.FormFieldDef `json:"fields"`
	// Consent statement to show with a checkbox; when present the submission
	// must carry consent=true. Omitted when the form has none.
	ConsentText string `json:"consent_text,omitempty" example:"I agree to be contacted about my enquiry."`
	// What to do after submitting: show a message or redirect. Defaults to
	// message.
	SubmitAction string `json:"submit_action" enums:"message,redirect" example:"message"`

	// RecaptchaSiteKey is present only when the form asks for the check and
	// the server has the key pair to run it.

	// reCAPTCHA v3 site key the renderer loads; present only when the form
	// enables reCAPTCHA and the server has both keys configured.
	RecaptchaSiteKey string `json:"recaptcha_site_key,omitempty" example:"6LcExampleSiteKey000000000000000000000000"`

	// Challenge pins the moment this definition was handed out; the submission
	// carries it back so the server can tell a filled-in form from an instant
	// replay.

	// Signed time-trap token minted for this response. Send it back unchanged
	// as challenge with the submission; a submission sent less than 3 seconds
	// or more than 24 hours after it was issued is filed as spam.
	Challenge string `json:"challenge" example:"MTc2MTgxNTYwMA.4e3b9a0c5d7f21e8b6a4c3d2f1e0a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2"`

	// HoneypotField is the name of the decoy input the renderer must include.

	// Name of the decoy input the renderer must include and hide from people;
	// always "website_url_confirm". A non-empty value in the submission files
	// it as spam.
	HoneypotField string `json:"honeypot_field" example:"website_url_confirm"`
}

// PublicSubmissionRequest is the body of a public submission. The validate
// tag on Challenge only marks it required in the API specification; the
// service enforces it (gin validates binding tags, never validate tags).
type PublicSubmissionRequest struct {
	// Submitted values keyed by field name. Only fields the definition
	// declares are allowed; missing optional fields count as empty. Values are
	// trimmed; checkbox values true, 1, on, yes or checked (any case) mean
	// ticked, anything else unticked.
	Values map[string]string `json:"values"`
	// Whether the visitor accepted the consent text; must be true when the
	// form has consent text, ignored otherwise.
	Consent bool `json:"consent" example:"true"`
	// The challenge from the definition response, unchanged. Required: a
	// missing or blank one is 400 VALIDATION_ERROR.
	Challenge string `json:"challenge" validate:"required" example:"MTc2MTgxNTYwMA.4e3b9a0c5d7f21e8b6a4c3d2f1e0a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2"`
	// Honeypot input; must be absent or empty. A non-empty value files the
	// submission as spam.
	Honeypot string `json:"website_url_confirm" example:""`
	// reCAPTCHA v3 token; needed only when the definition carried
	// recaptcha_site_key. Missing or failing tokens file the submission as
	// spam.
	CaptchaToken string `json:"captcha_token" example:""`
	// URL of the page the form was submitted from, stored as the submission's
	// referrer (cut to 512 characters). Not verified.
	PageURL string `json:"page_url" example:"https://www.example.com/pricing"`
}

// SubmissionMeta is what the transport knows about a submission and the
// service does not: who sent it and from where.
type SubmissionMeta struct {
	IP        string
	UserAgent string
	Origin    string
}

// SubmitOutcome tells the renderer what to do next. A submission rejected by a
// spam layer produces exactly the same outcome as a genuine one, so a bot
// learns nothing from the response.
type SubmitOutcome struct {
	// The form's submit action: show message in place, or redirect to
	// redirect_url.
	Action string `json:"action" enums:"message,redirect" example:"message"`
	// Text to show for the message action: the form's thank-you message, or a
	// default ("check your inbox" for double opt-in forms). Omitted for the
	// redirect action.
	Message string `json:"message,omitempty" example:"Thank you. Your submission has been received."`
	// Where to send the browser for the redirect action; omitted otherwise.
	RedirectURL string `json:"redirect_url,omitempty" example:"https://www.example.com/thank-you"`
	// True when the form uses double opt-in, so a confirmation mail has been
	// sent (also reported for submissions filed as spam, which get no mail).
	PendingConfirmation bool `json:"pending_confirmation" example:"false"`
}

// FieldErrors reports per-field validation failures of a submission, keyed by
// field name so the renderer can place each message next to its input. It
// unwraps to apperrors.ErrValidation, so a handler can classify it with
// errors.Is and pull the details out with errors.As.
type FieldErrors map[string]string

func (e FieldErrors) Error() string {
	if len(e) == 0 {
		return "validation failed"
	}
	names := make([]string, 0, len(e))
	for name := range e {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+": "+e[name])
	}
	return "validation failed - " + strings.Join(parts, "; ")
}

// LogMessage summarizes validation without custom field names or submitted values.
func (e FieldErrors) LogMessage() string {
	return fmt.Sprintf("validation failed (%d fields)", len(e))
}

func (e FieldErrors) Unwrap() error { return apperrors.ErrValidation }

type formService struct {
	repo      repository.FormRepository
	leadRepo  repository.LeadRepository
	userRepo  repository.UserRepository
	mailer    mailer.Mailer
	txManager *utils.TransactionManager
	cfg       config.FormsConfig

	// confirmURL is the public address of the confirmation page; the raw token
	// is appended as a query parameter when the link is mailed.
	confirmURL string
	// tokenSecret keys both the HMAC of the stored confirmation-token hashes
	// and the signature of the time-trap challenge.
	tokenSecret string
	// verifier is nil whenever the server has no reCAPTCHA key pair, which is
	// what makes a form's captcha_enabled flag a no-op instead of a wall.
	verifier *forms.RecaptchaVerifier

	// automation records the outbox events of the leads a submission creates
	// or updates (issue #164), in the submission's own transaction. Nil means
	// no automation.
	automation repository.AutomationRepository
}

// FormServiceOption configures NewFormService beyond its required
// dependencies.
type FormServiceOption func(*formService)

// WithFormAutomationEvents makes the submission paths record lead_created
// (origin form) for a new lead and lead_updated for an existing one.
func WithFormAutomationEvents(repo repository.AutomationRepository) FormServiceOption {
	return func(s *formService) { s.automation = repo }
}

// NewFormService wires the forms module. apiPrefix is the mount point of the
// API (e.g. "/api/v1"), which together with cfg.PublicBaseURL yields the
// confirmation link mailed to visitors.
func NewFormService(
	repo repository.FormRepository,
	leadRepo repository.LeadRepository,
	userRepo repository.UserRepository,
	m mailer.Mailer,
	txManager *utils.TransactionManager,
	cfg config.FormsConfig,
	apiPrefix string,
	opts ...FormServiceOption,
) FormService {
	s := &formService{
		repo:      repo,
		leadRepo:  leadRepo,
		userRepo:  userRepo,
		mailer:    m,
		txManager: txManager,
		cfg:       cfg,
		confirmURL: strings.TrimRight(cfg.PublicBaseURL, "/") +
			"/" + strings.Trim(apiPrefix, "/") + "/forms/public/confirm",
		tokenSecret: formTokenSecret(),
	}
	if cfg.RecaptchaActive() {
		s.verifier = forms.NewRecaptchaVerifier(cfg.RecaptchaSecret, cfg.RecaptchaMinScore)
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// formTokenSecret resolves the HMAC key for confirmation tokens and challenge
// signatures with the same precedence the session tokens use — the dedicated
// API key secret when configured, the JWT secret otherwise. It is read from the
// environment rather than taken as a parameter because the forms configuration
// group carries no secret of its own; both variables are already validated at
// startup, where a short or missing JWT secret aborts the boot.
func formTokenSecret() string {
	if secret := os.Getenv("API_KEY_SECRET"); secret != "" {
		return secret
	}
	return os.Getenv("JWT_SECRET")
}

// ---------------------------------------------------------------------------
// Administration
// ---------------------------------------------------------------------------

func (s *formService) Create(form *models.Form, actorID uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "form"), "FormService", "Create")

	form.CreatedByID = actorID
	if err := s.prepare(form); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Rejected form definition")
		return err
	}

	if err := s.assignPublicID(form); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	if err := s.repo.Create(form); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithField("form_id", form.ID).Info("Form created successfully")
	return nil
}

func (s *formService) GetByID(id uint) (*models.Form, error) {
	form, err := s.repo.GetByID(id)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, fmt.Errorf("form %d not found: %w", id, apperrors.ErrNotFound)
		}
		return nil, err
	}
	return form, nil
}

// List returns one page of forms, the submission count of each form on that
// page, and the total number of forms matching the status filter. An unknown
// sort column is a bad request rather than a silent fallback, so a typo in a
// query string is visible instead of quietly reordering the list.
func (s *formService) List(offset, limit int, status, sortBy, sortOrder string) ([]models.Form, map[uint]int64, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "FormService", "List")

	if sortBy != "" && !formSortColumns[sortBy] {
		return nil, nil, 0, FieldErrors{"sort_by": fmt.Sprintf("cannot sort forms by %q", sortBy)}
	}
	if status != "" {
		switch models.FormStatus(status) {
		case models.FormStatusDraft, models.FormStatusPublished, models.FormStatusArchived:
		default:
			return nil, nil, 0, FieldErrors{"status": fmt.Sprintf("unknown form status %q", status)}
		}
	}

	logger = logger.WithField("status", status)
	formList, total, err := s.repo.List(offset, limit, status, sortBy, sortOrder)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, nil, 0, err
	}

	ids := make([]uint, 0, len(formList))
	for i := range formList {
		ids = append(ids, formList[i].ID)
	}

	counts, err := s.repo.SubmissionCounts(ids)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, nil, 0, err
	}

	return formList, counts, total, nil
}

// Update replaces a form's definition and settings wholesale. The public
// identifier and the author are copied from the stored row: the first is a
// published address that must keep working, the second is an audit fact.
func (s *formService) Update(id uint, form *models.Form) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("form_id", id), "FormService", "Update")

	existing, err := s.repo.GetByID(id)
	if err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Form not found")
			return fmt.Errorf("form %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	form.ID = existing.ID
	form.CreatedAt = existing.CreatedAt
	form.DeletedAt = existing.DeletedAt
	form.PublicID = existing.PublicID
	form.CreatedByID = existing.CreatedByID

	if err := s.prepare(form); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Rejected form definition")
		return err
	}

	if err := s.repo.Update(form); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("Form updated successfully")
	return nil
}

func (s *formService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("form_id", id), "FormService", "Delete")

	if err := s.repo.Delete(id); err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Form not found")
			return fmt.Errorf("form %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("Form deleted successfully")
	return nil
}

func (s *formService) ListSubmissions(formID uint, offset, limit int, status string) ([]models.FormSubmission, int64, error) {
	if _, err := s.GetByID(formID); err != nil {
		return nil, 0, err
	}
	return s.repo.ListSubmissions(formID, offset, limit, status)
}

func (s *formService) GetSubmission(id uint) (*models.FormSubmission, error) {
	submission, err := s.repo.GetSubmissionByID(id)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, fmt.Errorf("form submission %d not found: %w", id, apperrors.ErrNotFound)
		}
		return nil, err
	}
	return submission, nil
}

// prepare normalises and validates a form for storage: it fills in the
// defaults the model does not carry as column defaults, runs the definition
// rules, and checks the lead owner.
func (s *formService) prepare(form *models.Form) error {
	form.Name = strings.TrimSpace(form.Name)
	if form.Status == "" {
		form.Status = models.FormStatusDraft
	}
	if form.SubmitAction == "" {
		form.SubmitAction = models.FormSubmitActionMessage
	}

	switch form.Status {
	case models.FormStatusDraft, models.FormStatusPublished, models.FormStatusArchived:
	default:
		return FieldErrors{"status": fmt.Sprintf("unknown form status %q", form.Status)}
	}

	// ValidateDefinition wraps models.ErrInvalidFormDefinition, which is the
	// models package's own sentinel; the service boundary is where it becomes
	// the application-wide validation sentinel a handler answers with 400.
	if err := form.ValidateDefinition(); err != nil {
		return fmt.Errorf("%w: %w", err, apperrors.ErrValidation)
	}

	return s.checkLeadOwner(form)
}

// checkLeadOwner makes sure created leads land with someone who can work them:
// an existing, active user holding a role that owns leads.
func (s *formService) checkLeadOwner(form *models.Form) error {
	if form.DefaultOwnerID == 0 {
		return nil
	}

	owner, err := s.userRepo.GetByID(form.DefaultOwnerID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return FieldErrors{"default_owner_id": fmt.Sprintf("user %d does not exist", form.DefaultOwnerID)}
		}
		return err
	}
	if !owner.IsActive {
		return FieldErrors{"default_owner_id": "the lead owner is deactivated"}
	}
	if owner.Role != models.RoleAdmin && owner.Role != models.RoleSales {
		return FieldErrors{"default_owner_id": "leads can only be owned by an admin or sales user"}
	}
	return nil
}

// assignPublicID picks an identifier no live form is using. A 128-bit value
// makes a genuine collision a theoretical concern only; the lookup is what
// turns that theory into a retry instead of a failed insert, since the
// repository's unique-constraint classifier is package-private and a create
// error the service cannot classify must never be retried blindly.
func (s *formService) assignPublicID(form *models.Form) error {
	for attempt := 0; attempt < formPublicIDAttempts; attempt++ {
		candidate, err := newFormPublicID()
		if err != nil {
			return err
		}

		_, err = s.repo.GetByPublicID(candidate)
		if err == nil {
			continue
		}
		if !apperrors.IsNotFound(err) {
			return err
		}

		form.PublicID = candidate
		return nil
	}
	return errors.New("failed to allocate a unique public form id")
}

func newFormPublicID() (string, error) {
	buf := make([]byte, formPublicIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate public form id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ---------------------------------------------------------------------------
// Public rendering
// ---------------------------------------------------------------------------

// PublicDefinition returns what a visitor's browser needs to render the form.
// An unknown key, an unpublished form and an origin the form does not allow are
// all reported as not-found: the public surface never explains why a form is
// unavailable.
func (s *formService) PublicDefinition(publicID, origin string) (*PublicFormDefinition, error) {
	form, err := s.publishedForm(s.repo, publicID)
	if err != nil {
		return nil, err
	}

	if !originAllowed(form.AllowedDomains, origin) {
		utils.Logger.WithField("form_id", form.ID).
			Info("Form definition requested from a disallowed origin")
		return nil, fmt.Errorf("form %q not found: %w", publicID, apperrors.ErrNotFound)
	}

	// Definitions saved before the column limits existed may still declare a
	// wider max_length; publish the limit the validator enforces.
	fields := make([]models.FormFieldDef, len(form.Fields))
	for i, field := range form.Fields {
		field.MaxLength = effectiveMaxLength(field)
		fields[i] = field
	}

	definition := &PublicFormDefinition{
		Name:          form.Name,
		PublicID:      form.PublicID,
		Fields:        fields,
		ConsentText:   form.ConsentText,
		SubmitAction:  form.SubmitAction,
		Challenge:     forms.NewChallenge([]byte(s.tokenSecret), time.Now()),
		HoneypotField: formHoneypotField,
	}
	if definition.SubmitAction == "" {
		definition.SubmitAction = models.FormSubmitActionMessage
	}
	if form.CaptchaEnabled && s.cfg.RecaptchaActive() {
		definition.RecaptchaSiteKey = s.cfg.RecaptchaSiteKey
	}
	return definition, nil
}

func (s *formService) publishedForm(repo repository.FormRepository, publicID string) (*models.Form, error) {
	form, err := repo.GetByPublicID(publicID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, fmt.Errorf("form %q not found: %w", publicID, apperrors.ErrNotFound)
		}
		return nil, err
	}
	if form.Status != models.FormStatusPublished {
		return nil, fmt.Errorf("form %q not found: %w", publicID, apperrors.ErrNotFound)
	}
	return form, nil
}

// originAllowed matches a request origin against a form's allowlist.
//
// The match is EXACT on host[:port], case-insensitively: an entry of
// "example.com" matches an origin of "https://example.com" (browsers omit the
// default port) but neither "www.example.com" nor "example.com:8443", and an
// entry of "localhost:5173" matches only that host and port. Subdomains are
// therefore listed individually — an allowlist that silently covered every
// subdomain would not be an allowlist. An empty list allows every origin; a
// request with no origin at all matches nothing once a list exists.
func originAllowed(allowed []string, origin string) bool {
	if len(allowed) == 0 {
		return true
	}

	host := originHost(origin)
	if host == "" {
		return false
	}
	for _, entry := range allowed {
		if strings.ToLower(strings.TrimSpace(entry)) == host {
			return true
		}
	}
	return false
}

// originHost reduces an Origin (or Referer) value to its host[:port]. A bare
// host is accepted as it stands, since not every caller sends a full URL.
func originHost(origin string) string {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return ""
	}

	parsed, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	if parsed.Host != "" {
		return strings.ToLower(parsed.Host)
	}
	if strings.ContainsAny(origin, "/ ") {
		return ""
	}
	return strings.ToLower(origin)
}

// ---------------------------------------------------------------------------
// Submission pipeline
// ---------------------------------------------------------------------------

// SubmitPublic runs a public submission through validation and every spam
// layer, then stores it and triggers whatever the form is configured to do.
//
// The layers run in a fixed order — validation, challenge, origin, honeypot,
// captcha — and the first one that trips decides the recorded reason. Only
// validation is reported to the client; every spam layer produces the same
// success-shaped outcome a genuine submission gets, with the submission stored
// as spam so an admin can see what the filters caught.
//
// A submission is a transaction root: it mails after its own commit, so a ctx
// that already carries a transaction is refused with
// utils.ErrTransactionRootRequired before anything is read or written.
func (s *formService) SubmitPublic(ctx context.Context, publicID string, req *PublicSubmissionRequest, meta SubmissionMeta) (*SubmitOutcome, error) {
	if utils.InTransaction(ctx) {
		return nil, utils.ErrTransactionRootRequired
	}
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "form"), "FormService", "SubmitPublic")

	// Every statement of a submission is bound to the visitor's request: a
	// request that is cancelled or times out stops waiting for a connection,
	// abandons the captcha call and stores nothing.
	form, err := s.publishedForm(s.repo.WithTx(s.txManager.DB(ctx)), publicID)
	if err != nil {
		return nil, err
	}

	logger = logger.WithField("form_id", form.ID)
	values, err := s.validateValues(form, req)
	if err != nil {
		return nil, err
	}

	spamReason, err := s.checkChallenge(req.Challenge)
	if err != nil {
		return nil, err
	}
	if spamReason != "" {
		return s.recordSpam(ctx, form, values, meta, req, spamReason)
	}

	if !originAllowed(form.AllowedDomains, meta.Origin) {
		return s.recordSpam(ctx, form, values, meta, req, models.FormSpamReasonDomain)
	}

	if strings.TrimSpace(req.Honeypot) != "" {
		return s.recordSpam(ctx, form, values, meta, req, models.FormSpamReasonHoneypot)
	}

	passed, err := s.checkCaptcha(ctx, form, req, meta)
	if err != nil {
		return nil, err
	}
	if !passed {
		return s.recordSpam(ctx, form, values, meta, req, models.FormSpamReasonCaptcha)
	}

	submission := s.newSubmission(form, values, meta, req)
	if form.DoubleOptIn {
		submission.Status = models.FormSubmissionPending
		if err := s.storePendingSubmission(ctx, form, submission); err != nil {
			utils.LogServiceResponse(logger, err)
			return nil, err
		}
	} else {
		submission.Status = models.FormSubmissionReceived
		if err := s.storeReceivedSubmission(ctx, form, submission); err != nil {
			utils.LogServiceResponse(logger, err)
			return nil, err
		}
	}

	logger.WithField("submission_id", submission.ID).
		WithField("status", submission.Status).
		Info("Form submission accepted")
	return submitOutcome(form), nil
}

// validateValues checks the submitted values against the field definitions and
// returns the normalised map that gets stored: every defined field is present,
// trimmed, with checkboxes reduced to "true"/"false" and the address
// lowercased so deduplication behaves the same on MySQL and SQLite.
func (s *formService) validateValues(form *models.Form, req *PublicSubmissionRequest) (map[string]string, error) {
	fieldErrors := FieldErrors{}
	defined := make(map[string]bool, len(form.Fields))
	for i := range form.Fields {
		defined[form.Fields[i].Name] = true
	}

	// An unknown key is a broken or hostile client, not a typo by a visitor:
	// the renderer only ever sends what the definition declared.
	for name := range req.Values {
		if !defined[name] {
			fieldErrors[name] = "unknown field"
		}
	}

	values := make(map[string]string, len(form.Fields))
	for i := range form.Fields {
		field := form.Fields[i]
		value := strings.TrimSpace(req.Values[field.Name])

		if field.Type == models.FormFieldCheckbox {
			value = normaliseCheckboxValue(value)
		}
		values[field.Name] = value

		if field.Required {
			if value == "" || (field.Type == models.FormFieldCheckbox && value != "true") {
				fieldErrors[field.Name] = fieldLabel(field) + " is required"
				continue
			}
		}
		if value == "" {
			continue
		}

		if max := effectiveMaxLength(field); len([]rune(value)) > max {
			fieldErrors[field.Name] = fmt.Sprintf("%s must be at most %d characters long", fieldLabel(field), max)
			continue
		}

		switch field.Type {
		case models.FormFieldEmail:
			if !formEmailPattern.MatchString(value) {
				fieldErrors[field.Name] = fieldLabel(field) + " must be a valid email address"
			}
		case models.FormFieldSelect:
			if !containsOption(field.Options, value) {
				fieldErrors[field.Name] = fieldLabel(field) + " must be one of the offered options"
			}
		}
	}

	if strings.TrimSpace(form.ConsentText) != "" && !req.Consent {
		fieldErrors["consent"] = "consent is required"
	}

	if len(fieldErrors) > 0 {
		return nil, fieldErrors
	}

	values[models.FormFieldEmail] = strings.ToLower(values[models.FormFieldEmail])
	return values, nil
}

// checkChallenge inspects the time-trap challenge. A missing or forged
// challenge is a client fault and returns an error; a challenge that is too
// young to have been filled in by a human, or old enough to have been
// harvested, returns the spam reason instead.
func (s *formService) checkChallenge(challenge string) (string, error) {
	if strings.TrimSpace(challenge) == "" {
		return "", FieldErrors{"challenge": "the form session is missing; reload the page and try again"}
	}

	age, err := forms.ChallengeAge([]byte(s.tokenSecret), challenge, time.Now())
	if err != nil {
		return "", FieldErrors{"challenge": "the form session is invalid; reload the page and try again"}
	}
	if age < formChallengeMinAge || age > formChallengeMaxAge {
		return models.FormSpamReasonTimeTrap, nil
	}
	return "", nil
}

// checkCaptcha reports whether the captcha layer passes. A form that asks for
// the check on a server without keys skips it — documented behaviour, so a
// missing key pair degrades protection rather than blocking every submission.
// An unreachable verification service counts as a failure: it must never be
// possible to pass the check by breaking it.
//
// The call runs on the visitor's request context. When that request is
// cancelled or times out the check is abandoned and the context's error is
// returned instead of a verdict: nobody is waiting for the answer, and filing
// the submission as captcha spam would record a verdict that was never made.
func (s *formService) checkCaptcha(ctx context.Context, form *models.Form, req *PublicSubmissionRequest, meta SubmissionMeta) (bool, error) {
	if !form.CaptchaEnabled || s.verifier == nil {
		return true, nil
	}
	if strings.TrimSpace(req.CaptchaToken) == "" {
		return false, nil
	}

	ok, err := s.verifier.Verify(ctx, req.CaptchaToken, meta.IP)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, fmt.Errorf("captcha verification abandoned: %w", ctxErr)
		}
		utils.Logger.WithError(utils.LogError(err)).WithField("form_id", form.ID).
			Warn("Captcha verification failed to complete; treating the submission as spam")
		return false, nil
	}
	return ok, nil
}

// recordSpam stores a rejected submission and answers with the outcome a
// genuine submission would have produced. No mail is sent and no lead is
// created — the row exists purely so an admin can audit the filters.
func (s *formService) recordSpam(ctx context.Context, form *models.Form, values map[string]string, meta SubmissionMeta, req *PublicSubmissionRequest, reason string) (*SubmitOutcome, error) {
	submission := s.newSubmission(form, values, meta, req)
	submission.Status = models.FormSubmissionSpam
	submission.SpamReason = reason

	if err := s.repo.WithTx(s.txManager.DB(ctx)).CreateSubmission(submission); err != nil {
		return nil, err
	}

	utils.Logger.WithField("form_id", form.ID).WithField("spam_reason", reason).
		Info("Form submission rejected by a protection layer")
	return submitOutcome(form), nil
}

func (s *formService) newSubmission(form *models.Form, values map[string]string, meta SubmissionMeta, req *PublicSubmissionRequest) *models.FormSubmission {
	return &models.FormSubmission{
		FormID:    form.ID,
		Data:      values,
		Email:     values[models.FormFieldEmail],
		IPAddress: truncate(meta.IP, 45),
		UserAgent: truncate(meta.UserAgent, 255),
		Referrer:  truncate(req.PageURL, 512),
	}
}

// storeReceivedSubmission persists a final submission together with the lead it
// feeds, then sends whatever mail the form configures. The lead and the
// submission's link to it are written in one transaction so a submission can
// never point at a lead that was rolled back.
func (s *formService) storeReceivedSubmission(ctx context.Context, form *models.Form, submission *models.FormSubmission) error {
	err := s.txManager.WithTransaction(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		txFormRepo := s.repo.WithTx(tx)

		if err := txFormRepo.CreateSubmission(submission); err != nil {
			return err
		}
		if !form.CreateLead {
			return nil
		}
		if err := s.applySubmissionLead(s.leadRepo.WithTx(tx), automationTx(s.automation, tx), form, submission); err != nil {
			return err
		}
		return txFormRepo.UpdateSubmission(submission)
	})
	if err != nil {
		return err
	}

	s.notify(form, submission)
	s.sendFollowUpMail(form, submission)
	return nil
}

// storePendingSubmission persists a submission awaiting confirmation and mints
// its single-use token. Prior outstanding tokens for the same form and address
// are spent first, so re-submitting leaves exactly one working link. No lead is
// created and no notification is sent until the address is confirmed.
func (s *formService) storePendingSubmission(ctx context.Context, form *models.Form, submission *models.FormSubmission) error {
	var rawToken string

	err := s.txManager.WithTransaction(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		txFormRepo := s.repo.WithTx(tx)

		if err := txFormRepo.CreateSubmission(submission); err != nil {
			return err
		}
		if err := txFormRepo.InvalidatePendingTokens(form.ID, submission.Email); err != nil {
			return err
		}

		raw, err := generateOpaqueToken()
		if err != nil {
			return err
		}
		token := &models.FormConfirmationToken{
			SubmissionID: submission.ID,
			TokenHash:    hashOpaqueToken(raw, s.tokenSecret),
			ExpiresAt:    time.Now().Add(formConfirmationTTL),
		}
		if err := txFormRepo.CreateConfirmationToken(token); err != nil {
			return err
		}

		rawToken = raw
		return nil
	})
	if err != nil {
		return err
	}

	s.sendConfirmationMail(form, submission, rawToken)
	return nil
}

// ConfirmSubmission spends a confirmation token: the submission becomes
// confirmed, the lead is created or attached, and the notification and
// follow-up mail go out. Every rejection maps to ErrInvalidConfirmationToken.
//
// The token lookup, its claim, the pending check of the submission, the lead
// write and the submission update are one transaction. The claim is a
// conditional UPDATE that stamps the token only while it is still unused and
// unexpired and must affect exactly one row, so two requests that both read the
// token as spendable cannot both succeed: the second claim finds nothing to
// stamp and is rejected. A failure after the claim rolls the whole unit back,
// token included, so the link stays usable for a retry instead of being spent
// on a confirmation that never happened. Mail goes out only once the
// transaction has committed; a notification about a confirmation that was then
// rolled back would announce a lead that does not exist.
//
// The transaction runs on the visitor's request context: a request cancelled
// while it waits for a connection or for the token's row lock rolls back
// before anything is claimed, so the link stays usable. Like a submission it
// is a transaction root and refuses a ctx that already carries a transaction
// (utils.ErrTransactionRootRequired): its mail could not be taken back if an
// enclosing transaction rolled back.
func (s *formService) ConfirmSubmission(ctx context.Context, rawToken string) error {
	if utils.InTransaction(ctx) {
		return utils.ErrTransactionRootRequired
	}
	logger := utils.LogServiceCall(utils.Logger.WithField("handler", "confirm"), "FormService", "ConfirmSubmission")

	if strings.TrimSpace(rawToken) == "" {
		return fmt.Errorf("confirmation token missing: %w", ErrInvalidConfirmationToken)
	}
	hash := hashOpaqueToken(rawToken, s.tokenSecret)

	var (
		form       *models.Form
		submission *models.FormSubmission
	)
	err := s.txManager.WithTransaction(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		form, submission, err = s.confirmInTransaction(logger, s.repo.WithTx(tx), s.leadRepo.WithTx(tx), automationTx(s.automation, tx), hash)
		return err
	})
	if err != nil {
		// A rejected link is the visitor's problem and is logged where it is
		// detected; anything else is a failure of ours.
		if !errors.Is(err, ErrInvalidConfirmationToken) {
			utils.LogServiceResponse(logger, err)
		}
		return err
	}

	// Only committed state is announced: the team is told about a lead that
	// exists and the visitor gets the content for a confirmation that stuck.
	s.notify(form, submission)
	s.sendFollowUpMail(form, submission)

	logger.WithField("submission_id", submission.ID).Info("Form submission confirmed")
	return nil
}

// confirmInTransaction performs a confirmation on transaction-bound
// repositories and returns the form and the now-confirmed submission for the
// mail that follows the commit. Any error discards every write, the token claim
// included.
func (s *formService) confirmInTransaction(logger *logrus.Entry, formRepo repository.FormRepository, leadRepo repository.LeadRepository, events repository.AutomationRepository, hash string) (*models.Form, *models.FormSubmission, error) {
	// The lookup already excludes spent and expired tokens; the claim below
	// re-applies both predicates as part of its own UPDATE.
	stored, err := formRepo.GetConfirmationTokenByHash(hash)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("confirmation token rejected: %w", ErrInvalidConfirmationToken)
		}
		return nil, nil, fmt.Errorf("confirmation token lookup failed: %w", err)
	}

	// Defence in depth in case a repository implementation stops filtering.
	if stored.UsedAt != nil || !stored.ExpiresAt.After(time.Now()) {
		return nil, nil, fmt.Errorf("confirmation token rejected: %w", ErrInvalidConfirmationToken)
	}

	// Claim the token before reading anything else. The claim is the one
	// write competing requests contend on: whoever stamps the row first owns
	// the confirmation, and every other request finds no row to stamp — even
	// one that passed the lookup a moment ago, or one whose token expired
	// between the lookup and this statement.
	if err := formRepo.MarkConfirmationTokenUsed(stored.ID); err != nil {
		if apperrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("confirmation token rejected: %w", ErrInvalidConfirmationToken)
		}
		return nil, nil, fmt.Errorf("failed to mark confirmation token used: %w", err)
	}

	submission, err := formRepo.GetSubmissionByID(stored.SubmissionID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("confirmation token rejected: %w", ErrInvalidConfirmationToken)
		}
		return nil, nil, err
	}
	if submission.Status != models.FormSubmissionPending {
		return nil, nil, fmt.Errorf("confirmation token rejected: %w", ErrInvalidConfirmationToken)
	}

	form, err := formRepo.GetByID(submission.FormID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("confirmation token rejected: %w", ErrInvalidConfirmationToken)
		}
		return nil, nil, err
	}

	// A submission stored before values were limited to their columns may
	// hold one the lead insert would reject. Turn it away: the rollback hands
	// the token back, the visitor is told the link is no longer valid and
	// re-submits, where validation now reports the over-long field.
	if name, wide := overlongColumnValue(submission.Data); wide {
		logger.WithField("submission_id", submission.ID).WithField("field", name).
			Warn("Pending submission holds a value wider than its column; confirmation refused")
		return nil, nil, fmt.Errorf("pending submission field %q exceeds its column: %w", name, ErrInvalidConfirmationToken)
	}

	confirmedAt := time.Now()
	submission.Status = models.FormSubmissionConfirmed
	submission.ConfirmedAt = &confirmedAt

	if form.CreateLead {
		if err := s.applySubmissionLead(leadRepo, events, form, submission); err != nil {
			return nil, nil, err
		}
	}
	if err := formRepo.UpdateSubmission(submission); err != nil {
		return nil, nil, err
	}
	return form, submission, nil
}

// applySubmissionLead links the submission to a lead: the newest live lead with
// the same address gains a note recording this submission, and when there is
// none a lead is created from the mapped fields. Both repositories are
// transaction-scoped by the caller; events (nil without automation) records
// the lead's outbox event in the same transaction, origin form.
func (s *formService) applySubmissionLead(leadRepo repository.LeadRepository, events repository.AutomationRepository, form *models.Form, submission *models.FormSubmission) error {
	notes := submissionNotes(form, submission)

	existing, err := leadRepo.GetLatestByEmail(submission.Email)
	if err != nil && !apperrors.IsNotFound(err) {
		return err
	}

	if err == nil && existing != nil {
		existing.Notes = appendLeadNotes(existing.Notes, notes, submissionNotesPointer(form))
		if err := leadRepo.Update(existing); err != nil {
			return err
		}
		if err := automationLeadUpdated(events, existing.ID, existing.Status, existing.Status, models.AutomationOriginForm, nil); err != nil {
			return err
		}
		submission.LeadID = &existing.ID
		return nil
	}

	values := submission.Data
	firstName := values["first_name"]
	lastName := values["last_name"]
	if firstName == "" && lastName == "" {
		// Most forms collect one "name" field instead of a first/last pair;
		// split it on the first space so the lead carries the visitor's name.
		if parts := strings.Fields(values["name"]); len(parts) > 0 {
			firstName = truncate(parts[0], leadNameMaxLength)
			lastName = truncate(strings.Join(parts[1:], " "), leadNameMaxLength)
		}
	}
	// leads.first_name and leads.last_name are NOT NULL, and a form is free to
	// collect no name at all, so both fall back to something a salesperson can
	// still recognise on a list.
	if firstName == "" {
		firstName = "Form"
	}
	if lastName == "" {
		lastName = truncate(emailLocalPart(submission.Email), leadNameMaxLength)
	}

	lead := &models.Lead{
		FirstName: firstName,
		LastName:  lastName,
		Email:     submission.Email,
		Phone:     values["phone"],
		Company:   values["company"],
		Position:  values["position"],
		Source:    truncate(form.Name, formLeadSourceMaxLength),
		Status:    models.LeadStatusNew,
		OwnerID:   form.DefaultOwnerID,
		Notes:     appendLeadNotes("", notes, submissionNotesPointer(form)),
	}
	if err := leadRepo.Create(lead); err != nil {
		return err
	}
	if err := automationLeadCreated(events, lead.ID, models.AutomationOriginForm, nil); err != nil {
		return err
	}

	submission.LeadID = &lead.ID
	return nil
}

// submissionNotes renders the note block appended to the lead: a dated header
// naming the form, followed by the answers that have no column of their own.
func submissionNotes(form *models.Form, submission *models.FormSubmission) string {
	var b strings.Builder
	b.WriteString("\n\n--- Form submission: ")
	// The name is admin-chosen, but it must not forge a marker either.
	b.WriteString(neutraliseNotesHeader(form.Name))
	b.WriteString(" (")
	b.WriteString(time.Now().Format("2006-01-02"))
	b.WriteString(") ---")

	for _, line := range submissionLines(form, submission, true) {
		b.WriteString("\n")
		b.WriteString(neutraliseNotesHeader(line))
	}
	return b.String()
}

// submissionNotesPointer is what a lead's notes get instead of a submission
// block when staff notes leave no room for one.
func submissionNotesPointer(form *models.Form) string {
	return "\n\n[Form submission: " + neutraliseNotesHeader(form.Name) + " (" + time.Now().Format("2006-01-02") +
		") not copied here because the notes are full; it is kept with the form.]"
}

// submissionLines renders the submitted answers as "Label: value" lines in the
// order the form declares them. With skipLeadFields set, the fields that map
// onto lead columns are left out, since they are already stored as columns.
func submissionLines(form *models.Form, submission *models.FormSubmission, skipLeadFields bool) []string {
	lines := make([]string, 0, len(form.Fields))
	for i := range form.Fields {
		field := form.Fields[i]
		if skipLeadFields && formLeadFields[field.Name] {
			continue
		}
		value := submission.Data[field.Name]
		if value == "" {
			continue
		}
		lines = append(lines, fieldLabel(field)+": "+value)
	}
	return lines
}

// submitOutcome describes what the renderer should do after a submission. It
// is deliberately derived from the form alone: spam and genuine submissions
// must be answered identically.
func submitOutcome(form *models.Form) *SubmitOutcome {
	outcome := &SubmitOutcome{
		Action:              form.SubmitAction,
		PendingConfirmation: form.DoubleOptIn,
	}
	if outcome.Action == "" {
		outcome.Action = models.FormSubmitActionMessage
	}

	if outcome.Action == models.FormSubmitActionRedirect {
		outcome.RedirectURL = form.RedirectURL
		return outcome
	}

	switch {
	case strings.TrimSpace(form.ThankYouMessage) != "":
		outcome.Message = form.ThankYouMessage
	case form.DoubleOptIn:
		// Without a word about the mail that is on its way, a visitor of an
		// opt-in form would believe they were done.
		outcome.Message = formDefaultPendingMessage
	default:
		outcome.Message = formDefaultThankYouMessage
	}
	return outcome
}

// ---------------------------------------------------------------------------
// Mail
// ---------------------------------------------------------------------------

// sendConfirmationMail delivers the double-opt-in link. Delivery failures are
// logged and never returned: the submission is stored and the visitor can ask
// for a new link by submitting again.
func (s *formService) sendConfirmationMail(form *models.Form, submission *models.FormSubmission, rawToken string) {
	link := s.confirmURL + "?token=" + url.QueryEscape(rawToken)

	subject := form.ConfirmationSubject
	if strings.TrimSpace(subject) == "" {
		subject = formDefaultConfirmationSubject
	}
	body := form.ConfirmationBody
	if strings.TrimSpace(body) == "" {
		body = formDefaultConfirmationBody
	}
	// A body an admin wrote without the placeholder would be a dead end, so the
	// link is appended rather than dropped.
	if !strings.Contains(body, formConfirmationLinkPlaceholder) {
		body = strings.TrimRight(body, "\n") + "\n\n" + formConfirmationLinkPlaceholder + "\n"
	}
	body = strings.ReplaceAll(body, formConfirmationLinkPlaceholder, link)

	s.send(form, submission.Email, subject, body, "confirmation")
}

// sendFollowUpMail delivers the post-submission mail, which is what carries a
// gated-content link. It is sent only when the form defines a subject: an empty
// subject is how a form says it has no follow-up.
func (s *formService) sendFollowUpMail(form *models.Form, submission *models.FormSubmission) {
	if strings.TrimSpace(form.FollowUpSubject) == "" {
		return
	}

	body := form.FollowUpBody
	if strings.TrimSpace(body) == "" {
		body = formDefaultFollowUpBody
	}
	body = strings.ReplaceAll(body, formContentLinkPlaceholder, form.ContentURL)

	s.send(form, submission.Email, form.FollowUpSubject, body, "follow-up")
}

// notify tells the team about a submission, one message per configured
// recipient.
func (s *formService) notify(form *models.Form, submission *models.FormSubmission) {
	if len(form.NotifyEmails) == 0 {
		return
	}

	var b strings.Builder
	b.WriteString("A new submission arrived through the form \"")
	b.WriteString(form.Name)
	b.WriteString("\".\n\n")
	for _, line := range submissionLines(form, submission, false) {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\nSubmission ID: ")
	fmt.Fprintf(&b, "%d\n", submission.ID)

	subject := "New submission: " + form.Name
	body := b.String()
	for _, recipient := range form.NotifyEmails {
		s.send(form, recipient, subject, body, "notification")
	}
}

// send delivers one message. Mail is a side effect of a submission that has
// already been stored, so a delivery failure is logged and swallowed — the same
// posture the password reset flow takes.
func (s *formService) send(form *models.Form, to, subject, body, kind string) {
	if s.mailer == nil || strings.TrimSpace(to) == "" {
		return
	}
	if err := s.mailer.Send(to, subject, body); err != nil {
		utils.Logger.WithError(utils.LogError(err)).
			WithField("form_id", form.ID).
			WithField("mail", kind).
			Warn("Form mail delivery failed")
	}
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func fieldLabel(field models.FormFieldDef) string {
	if strings.TrimSpace(field.Label) != "" {
		return field.Label
	}
	return field.Name
}

// overlongColumnValue reports a submitted value that is wider than the column
// it would be written to.
func overlongColumnValue(values map[string]string) (string, bool) {
	for name, value := range values {
		if limit, ok := models.FormFieldColumnLimit(name); ok && utf8.RuneCountInString(value) > limit {
			return name, true
		}
	}
	return "", false
}

// effectiveMaxLength re-derives the per-type default for a definition stored
// before normalisation, so a zero never turns into a zero-length limit, and
// narrows it to the column a lead-mapped value is stored in — the database
// would otherwise reject the insert and the visitor would see a server error.
func effectiveMaxLength(field models.FormFieldDef) int {
	max := field.MaxLength
	if max <= 0 {
		if field.Type == models.FormFieldTextarea {
			max = models.FormTextareaDefaultMaxLength
		} else {
			max = models.FormDefaultMaxLength
		}
	}
	if limit, ok := models.FormFieldColumnLimit(field.Name); ok && limit < max {
		max = limit
	}
	return max
}

// normaliseCheckboxValue reduces the many ways a browser expresses a ticked box
// to the two the submission stores.
func normaliseCheckboxValue(value string) string {
	switch strings.ToLower(value) {
	case "true", "1", "on", "yes", "checked":
		return "true"
	case "":
		return "false"
	default:
		return "false"
	}
}

func containsOption(options []string, value string) bool {
	for _, option := range options {
		if option == value {
			return true
		}
	}
	return false
}

func emailLocalPart(email string) string {
	if local, _, found := strings.Cut(email, "@"); found && local != "" {
		return local
	}
	if email != "" {
		return email
	}
	return "Submission"
}

// truncate clips a value to what its column holds, counting runes so a
// multi-byte character is never cut in half.
func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
