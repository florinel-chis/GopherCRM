package service

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/forms"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
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

	// formPendingSubmissionRetries is how many times a double-opt-in submission
	// that lost a deadlock or timed out on a lock is replayed.
	formPendingSubmissionRetries = 5

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
	// Optional idempotency key of an API client (#213): 1 to 128 characters of
	// A-Z, a-z, 0-9, '.', '_', ':' and '-', not starting with "submission-",
	// and never personal data. On a form with create_ticket, confirmations
	// carrying the same key open at most one ticket; elsewhere it is ignored.
	// An invalid value (an explicit empty string included) is 400
	// VALIDATION_ERROR with error.details.idempotency_key; the value is never
	// echoed. The hosted view renders one random key per page load
	// (data-idempotency-key), and the embed script sends that key, or one it
	// generates per render, on every submission of the render.
	IdempotencyKey *string `json:"idempotency_key,omitempty" example:"order-2026-1187" maxLength:"128"`
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

	// Mail delivery records (#216, form_mail_delivery.go); zero values select
	// time.Now and the 5-second bound of each post-commit claim and stamp.
	mailClock        func() time.Time
	mailStampTimeout time.Duration

	// Ticket intake (#213, form_ticket_intake.go). ticketRepo is nil when the
	// service was built without WithFormTicketIntake: a create_ticket
	// confirmation then records the outcome unavailable.
	ticketRepo repository.FormTicketRepository
	ticketTx   *utils.TransactionManager
	// ticketIntakeSeam is a test-only, instance-local hook called at the
	// named stages of a ticket confirmation inside its transaction; an error
	// it returns rolls the confirmation back. Nil in production.
	ticketIntakeSeam func(stage string) error

	// webhooks is the outbound webhook emitter (#345): a submission that
	// creates a new lead emits lead.created, and a confirmation whose ticket
	// intake outcome is created emits ticket.created, each in the
	// submission's own transaction. NewFormService sets it to webhook.Nop,
	// so it is never nil.
	webhooks webhook.Emitter
}

// FormServiceOption configures NewFormService beyond its required
// dependencies.
type FormServiceOption func(*formService)

// WithFormWebhooks makes the form paths emit lead.created through e for a new
// lead (never for a repeat enquiry) and ticket.created for a ticket intake
// outcome created (never for a duplicate), in the submission's transaction
// (#345). A nil e means webhook.Nop: no webhooks.
func WithFormWebhooks(e webhook.Emitter) FormServiceOption {
	return func(s *formService) {
		if e == nil {
			e = webhook.Nop
		}
		s.webhooks = e
	}
}

// WithFormAutomationEvents makes the submission paths record lead_created or
// lead_updated (origin form), then lead_form_submitted (#220).
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
		webhooks:  webhook.Nop,
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
