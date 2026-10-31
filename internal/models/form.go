package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Forms are CRM-defined lead-capture forms that are rendered on external
// websites and whose submissions land back in the CRM.
//
// The field definitions, the notification recipients, the origin allowlist and
// the submitted values are all list- or map-shaped, and every one of them is
// persisted as serialized JSON in a TEXT column mirrored by a `gorm:"-"`
// decoded twin, exactly as the AEO models do it: BeforeSave encodes the twin
// into the column, AfterFind decodes it back. No database JSON function is
// involved, so MySQL 8 (production) and in-memory SQLite (tests) behave
// identically.

// FormStatus is the publication state of a form. Only a published form is
// served to the public endpoints.
type FormStatus string

const (
	FormStatusDraft     FormStatus = "draft"
	FormStatusPublished FormStatus = "published"
	FormStatusArchived  FormStatus = "archived"
)

// FormSubmissionStatus is the lifecycle state of a single submission.
//
// A submission is `received` when it is final on arrival, `pending` while a
// double-opt-in confirmation is outstanding, `confirmed` once that
// confirmation is spent, and `spam` when one of the protection layers rejected
// it. Spam rows are kept so admins can see what the filters catch.
type FormSubmissionStatus string

const (
	FormSubmissionReceived  FormSubmissionStatus = "received"
	FormSubmissionPending   FormSubmissionStatus = "pending"
	FormSubmissionConfirmed FormSubmissionStatus = "confirmed"
	FormSubmissionSpam      FormSubmissionStatus = "spam"
)

// The field types a form definition may use. `hidden` carries a prefilled
// value the visitor never sees, which is why it can never be required.
const (
	FormFieldText     = "text"
	FormFieldEmail    = "email"
	FormFieldPhone    = "phone"
	FormFieldTextarea = "textarea"
	FormFieldSelect   = "select"
	FormFieldCheckbox = "checkbox"
	FormFieldHidden   = "hidden"
)

// Submit actions. `message` renders a thank-you message in place, `redirect`
// sends the visitor to another page.
const (
	FormSubmitActionMessage  = "message"
	FormSubmitActionRedirect = "redirect"
)

// Spam reasons, one per protection layer.
const (
	FormSpamReasonHoneypot = "honeypot"
	FormSpamReasonTimeTrap = "time_trap"
	FormSpamReasonCaptcha  = "captcha"
	FormSpamReasonDomain   = "domain"
)

// Per-field value length limits. A field that declares no limit gets the
// default for its type; anything larger than the hard cap is clamped to it, so
// no definition can make a submission grow without bound.
const (
	FormDefaultMaxLength         = 1000
	FormTextareaDefaultMaxLength = 5000
	FormHardMaxLength            = 10000
)

// formFieldColumnLimits is the width of the varchar column each lead-mapped
// field's value is written to: the address goes into form_submissions.email and
// leads.email, the rest into the lead column of the same name. MySQL rejects an
// over-long value instead of clipping it, so these cap whatever max_length a
// definition declares. A lone "name" field is absent on purpose — it is split
// and clipped into the two name columns. The widths are held to the model tags
// by TestFormFieldColumnLimitsMatchTheModels.
var formFieldColumnLimits = map[string]int{
	FormFieldEmail: 255,
	"first_name":   100,
	"last_name":    100,
	"phone":        50,
	"company":      200,
	"position":     100,
}

// FormFieldColumnLimit reports the width of the column a submitted field's
// value is stored in, for the fields that are copied into sized columns.
func FormFieldColumnLimit(name string) (int, bool) {
	limit, ok := formFieldColumnLimits[name]
	return limit, ok
}

// FormMaxFields is the number of fields one form may declare, and doubles as
// the cap on the options of a select field.
const FormMaxFields = 50

// ErrInvalidFormDefinition marks every error ValidateDefinition returns, so a
// caller can classify a bad definition with errors.Is instead of reading the
// message. Callers in the service layer wrap it in their own validation
// sentinel before it reaches a handler.
var ErrInvalidFormDefinition = errors.New("invalid form definition")

var (
	// Machine names are lowercase identifiers: they end up as HTML input names,
	// as JSON keys of the stored submission and as lead column names.
	formFieldNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,49}$`)

	// Deliberately permissive: this guards a notification recipient list an
	// admin typed, not user input, and a stricter grammar would reject valid
	// addresses far more often than it would catch a typo.
	formEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

	// An allowed domain is a bare host with an optional port — never a scheme
	// and never a path, both of which contain characters this rejects.
	formDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)
)

// FormFieldDef is one input of a form definition. It lives inside the
// serialized `fields` column of forms, never in a table of its own, and its
// order in the slice is the order it renders in.
type FormFieldDef struct {
	// Machine name: the input name, the key of the submitted value and, for
	// first_name, last_name, name, email, phone, company and position, the lead
	// column it fills. Must match ^[a-z][a-z0-9_]{0,49}$ (trimmed first) and be
	// unique within the form. The one email field must be named "email".
	Name string `json:"name" example:"email"`
	// Label shown to the visitor and used in validation messages and lead
	// notes; the name is used when it is empty.
	Label string `json:"label" example:"Work email"`
	// Input type. A form has exactly one field of type email. Only select may
	// have options; hidden carries a prefilled value and cannot be required.
	Type string `json:"type" enums:"text,email,phone,textarea,select,checkbox,hidden" example:"email"`
	// Whether a value must be submitted; for a checkbox, that it is ticked.
	Required bool `json:"required" example:"true"`
	// Placeholder text for the input; omitted when empty.
	Placeholder string `json:"placeholder,omitempty" example:"you@company.example"`
	// Help text rendered under the input; omitted when empty.
	HelpText string `json:"help_text,omitempty" example:"We only use this to reply to you."`
	// Choices of a select field (1 to 50, none blank); the submitted value must
	// equal one of them exactly. Omitted for other types, which may not have
	// options.
	Options []string `json:"options,omitempty" example:"1-10,11-50,51+" maxItems:"50"`
	// Longest accepted value in characters. On save, 0 or absent becomes 1000
	// (5000 for textarea), anything above 10000 becomes 10000, and lead-mapped
	// fields are narrowed to their column: email 255, first_name and last_name
	// 100, phone 50, company 200, position 100. Stored definitions therefore
	// always carry the enforced limit.
	MaxLength int `json:"max_length,omitempty" example:"255"`
}

// Form is a lead-capture form definition plus everything that happens after a
// visitor submits it: notification mail, double opt-in, gated-content
// follow-up, spam protection and lead creation.
type Form struct {
	BaseModel
	// Form name, trimmed; at most 255 characters. Shown to visitors, used as
	// the source of the leads the form creates (cut to 100 characters) and in
	// notification subjects.
	Name string `gorm:"not null;type:varchar(255)" json:"name" example:"Website contact form" maxLength:"255"`
	// Internal description for staff; never exposed by the public endpoints.
	Description string `gorm:"type:text" json:"description" example:"Contact form on the pricing page."`

	// PublicID is the only identifier ever exposed publicly. It is random
	// rather than the primary key so the public endpoints cannot be enumerated.

	// Public identifier used in every public URL (/forms/public/{key}): 22
	// random URL-safe base64 characters assigned at creation and never changed,
	// so embeds keep working across edits.
	PublicID string `gorm:"not null;type:varchar(32);uniqueIndex" json:"public_id" example:"q3VZr0n8TjK1bWcY5xL2aQ" readonly:"true"`
	// Publication state; only a published form is served by the public
	// endpoints. Defaults to draft.
	Status FormStatus `gorm:"not null;type:varchar(20);default:'draft'" json:"status" example:"published"`

	// Ordered field definitions (1 to 50), normalised on save (names trimmed,
	// max_length defaulted and clamped).
	Fields     []FormFieldDef `gorm:"-" json:"fields"`
	FieldsJSON string         `gorm:"column:fields;type:text" json:"-"`

	// What the visitor sees after submitting: message shows thank_you_message
	// in place, redirect sends the browser to redirect_url. Defaults to
	// message.
	SubmitAction string `gorm:"not null;type:varchar(20);default:'message'" json:"submit_action" enums:"message,redirect" example:"message"`
	// Message shown after a submission when submit_action is message. When
	// empty a default text is shown (a "check your inbox" text for double
	// opt-in forms).
	ThankYouMessage string `gorm:"type:text" json:"thank_you_message" example:"Thanks, we will be in touch within one business day."`
	// Page the visitor is sent to when submit_action is redirect: required
	// then, and must start with http:// or https://. At most 512 characters;
	// stored unchecked for message forms.
	RedirectURL string `gorm:"type:varchar(512)" json:"redirect_url" example:"https://www.example.com/thank-you" maxLength:"512"`
	// Consent statement shown with the form. When non-empty, a submission must
	// carry consent=true or it is rejected with a field error on "consent".
	ConsentText string `gorm:"type:text" json:"consent_text" example:"I agree to be contacted about my enquiry."`

	// Staff addresses that receive one notification mail per accepted
	// submission (on arrival, or on confirmation for double opt-in forms). At
	// most 20, each at most 255 characters and shaped like an address; trimmed.
	// Reads return [] when there are none; the POST /forms and PUT /forms/{id}
	// responses echo the request and carry null when the list was omitted or
	// sent as null.
	NotifyEmails     []string `gorm:"-" json:"notify_emails" example:"sales@example.com" extensions:"x-nullable"`
	NotifyEmailsJSON string   `gorm:"column:notify_emails;type:text" json:"-"`

	// Double opt-in: a submission is stored as pending and a confirmation
	// mail with a single-use link valid for 48 hours goes to the submitted
	// address; the lead, the notification and the follow-up mail wait until
	// the link is confirmed.
	DoubleOptIn bool `gorm:"not null;default:false" json:"double_opt_in" example:"false"`
	// Subject of the confirmation mail, at most 255 characters; empty means
	// "Confirm your email address".
	ConfirmationSubject string `gorm:"type:varchar(255)" json:"confirmation_subject" example:"Please confirm your email address" maxLength:"255"`
	// Plain-text body of the confirmation mail. The {confirmation_link}
	// placeholder is replaced by the link, which is appended when the
	// placeholder is missing; empty means a built-in text.
	ConfirmationBody string `gorm:"type:text" json:"confirmation_body" example:"Hello, please confirm your address: {confirmation_link}"`
	// Subject of the follow-up mail sent to the submitter once the submission
	// is accepted; empty means no follow-up mail is sent. At most 255
	// characters.
	FollowUpSubject string `gorm:"type:varchar(255)" json:"follow_up_subject" example:"Your download link" maxLength:"255"`
	// Plain-text body of the follow-up mail; {content_link} is replaced by
	// content_url. Empty means a built-in text containing the link.
	FollowUpBody string `gorm:"type:text" json:"follow_up_body" example:"Thanks for your interest. Download the guide here: {content_link}"`
	// Link inserted for {content_link} in the follow-up mail (for gated
	// content). At most 512 characters; not checked for a scheme.
	ContentURL string `gorm:"type:varchar(512)" json:"content_url" example:"https://www.example.com/files/guide.pdf" maxLength:"512"`
	// Require a reCAPTCHA v3 token with each submission. Takes effect only
	// when the server has both reCAPTCHA keys; otherwise it is ignored.
	CaptchaEnabled bool `gorm:"not null;default:false" json:"captcha_enabled" example:"false"`

	// CreateLead deliberately carries NO `default:true` column tag. GORM
	// substitutes a literal column default whenever the Go field holds its zero
	// value (callbacks.ConvertToCreateValues), so a form explicitly configured
	// not to create leads would be persisted with create_lead = true. The
	// "new forms create leads" default therefore belongs to the API layer,
	// which knows whether the client sent the flag at all.

	// Whether an accepted submission feeds a lead: the newest live lead with
	// the same address gains a note, otherwise a new lead (status new, owner
	// default_owner_id, source the form name) is created.
	CreateLead bool `gorm:"not null" json:"create_lead" example:"true"`
	// Owner of the leads the form creates; 0 means none. Required when
	// create_lead is true; any non-zero value names an active admin or sales
	// user (checked on save only).
	DefaultOwnerID uint `gorm:"index" json:"default_owner_id" example:"2"`

	// Origin allowlist: bare host names with an optional port, lower-cased,
	// matched exactly (subdomains must be listed one by one). Empty allows
	// every origin. A definition requested from another origin is a 404; a
	// submission from another origin is stored as spam (reason domain). At
	// most 20, each at most 255 characters. Reads return [] when there are
	// none; the POST /forms and PUT /forms/{id} responses echo the request and
	// carry null when the list was omitted or sent as null.
	AllowedDomains     []string `gorm:"-" json:"allowed_domains" example:"www.example.com,localhost:5173" extensions:"x-nullable"`
	AllowedDomainsJSON string   `gorm:"column:allowed_domains;type:text" json:"-"`

	// ID of the user who created the form; server-assigned and immutable.
	CreatedByID uint `gorm:"index" json:"created_by_id" example:"1" readonly:"true"`
}

// BeforeSave serializes the decoded twins into their TEXT columns.
func (f *Form) BeforeSave(tx *gorm.DB) error {
	var err error
	if f.FieldsJSON, err = encodeJSONSlice(f.Fields); err != nil {
		return fmt.Errorf("form fields: %w", err)
	}
	if f.NotifyEmailsJSON, err = encodeJSONSlice(f.NotifyEmails); err != nil {
		return fmt.Errorf("form notify_emails: %w", err)
	}
	if f.AllowedDomainsJSON, err = encodeJSONSlice(f.AllowedDomains); err != nil {
		return fmt.Errorf("form allowed_domains: %w", err)
	}
	return nil
}

// AfterFind restores the decoded twins from their TEXT columns.
func (f *Form) AfterFind(tx *gorm.DB) error {
	f.Fields = decodeJSONSlice[FormFieldDef](f.FieldsJSON)
	f.NotifyEmails = decodeJSONSlice[string](f.NotifyEmailsJSON)
	f.AllowedDomains = decodeJSONSlice[string](f.AllowedDomainsJSON)
	return nil
}

// ValidateDefinition checks everything about a form that must hold before it
// can be stored, and normalises what it can: field length limits are defaulted
// and clamped, allowed domains are lowercased, notification addresses and
// domains are trimmed.
//
// Every error wraps ErrInvalidFormDefinition.
func (f *Form) ValidateDefinition() error {
	if strings.TrimSpace(f.Name) == "" {
		return formDefinitionError("name is required")
	}

	if len(f.Fields) == 0 {
		return formDefinitionError("a form needs at least one field")
	}
	if len(f.Fields) > FormMaxFields {
		return formDefinitionError("a form cannot have more than %d fields", FormMaxFields)
	}

	seen := make(map[string]bool, len(f.Fields))
	emailFields := 0
	for i := range f.Fields {
		field := &f.Fields[i]
		field.Name = strings.TrimSpace(field.Name)

		if !formFieldNamePattern.MatchString(field.Name) {
			return formDefinitionError("field %q: name must start with a lowercase letter and contain only lowercase letters, digits and underscores", field.Name)
		}
		if seen[field.Name] {
			return formDefinitionError("field %q: duplicate field name", field.Name)
		}
		seen[field.Name] = true

		if err := validateFieldType(field); err != nil {
			return err
		}
		if field.Type == FormFieldEmail {
			emailFields++
			if field.Name != FormFieldEmail {
				return formDefinitionError("field %q: the email field must be named %q", field.Name, FormFieldEmail)
			}
		}

		normaliseFieldMaxLength(field)
	}

	// Every form is a lead-capture form, and a lead without an address is not
	// something the CRM can follow up on — so exactly one email field, no more
	// (two addresses would make the lead mapping ambiguous) and no fewer.
	if emailFields != 1 {
		return formDefinitionError("a form must have exactly one field of type %q, named %q", FormFieldEmail, FormFieldEmail)
	}

	if err := f.validateSubmitAction(); err != nil {
		return err
	}
	if f.CreateLead && f.DefaultOwnerID == 0 {
		return formDefinitionError("a form that creates leads needs a default owner")
	}
	if err := f.validateNotifyEmails(); err != nil {
		return err
	}
	return f.validateAllowedDomains()
}

func validateFieldType(field *FormFieldDef) error {
	switch field.Type {
	case FormFieldText, FormFieldEmail, FormFieldPhone, FormFieldTextarea, FormFieldCheckbox, FormFieldHidden:
		if len(field.Options) > 0 {
			return formDefinitionError("field %q: only a select field can have options", field.Name)
		}
		if field.Type == FormFieldHidden && field.Required {
			return formDefinitionError("field %q: a hidden field cannot be required", field.Name)
		}
	case FormFieldSelect:
		if len(field.Options) == 0 {
			return formDefinitionError("field %q: a select field needs at least one option", field.Name)
		}
		if len(field.Options) > FormMaxFields {
			return formDefinitionError("field %q: a select field cannot have more than %d options", field.Name, FormMaxFields)
		}
		for _, option := range field.Options {
			if strings.TrimSpace(option) == "" {
				return formDefinitionError("field %q: select options cannot be empty", field.Name)
			}
		}
	default:
		return formDefinitionError("field %q: unknown field type %q", field.Name, field.Type)
	}
	return nil
}

// normaliseFieldMaxLength fills in the per-type default, enforces the hard cap,
// and narrows a lead-mapped field to the column its value is stored in, so the
// saved definition (and the builder and embed script that read it) states the
// limit the submission validator actually enforces.
func normaliseFieldMaxLength(field *FormFieldDef) {
	if field.MaxLength <= 0 {
		if field.Type == FormFieldTextarea {
			field.MaxLength = FormTextareaDefaultMaxLength
		} else {
			field.MaxLength = FormDefaultMaxLength
		}
	}
	if field.MaxLength > FormHardMaxLength {
		field.MaxLength = FormHardMaxLength
	}
	if limit, ok := FormFieldColumnLimit(field.Name); ok && field.MaxLength > limit {
		field.MaxLength = limit
	}
}

func (f *Form) validateSubmitAction() error {
	switch f.SubmitAction {
	case "", FormSubmitActionMessage:
		return nil
	case FormSubmitActionRedirect:
		url := strings.TrimSpace(f.RedirectURL)
		if url == "" {
			return formDefinitionError("a redirecting form needs a redirect URL")
		}
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return formDefinitionError("the redirect URL must be an http(s) URL")
		}
		f.RedirectURL = url
		return nil
	default:
		return formDefinitionError("unknown submit action %q", f.SubmitAction)
	}
}

func (f *Form) validateNotifyEmails() error {
	for i, email := range f.NotifyEmails {
		email = strings.TrimSpace(email)
		if !formEmailPattern.MatchString(email) {
			return formDefinitionError("notification address %q is not a valid email address", email)
		}
		f.NotifyEmails[i] = email
	}
	return nil
}

// validateAllowedDomains keeps the list as bare hosts. An admin who pastes a
// full URL gets told rather than silently ending up with an allowlist that can
// never match an Origin header's host.
func (f *Form) validateAllowedDomains() error {
	for i, domain := range f.AllowedDomains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if !formDomainPattern.MatchString(domain) {
			return formDefinitionError("allowed domain %q must be a bare host name, without scheme or path", f.AllowedDomains[i])
		}
		f.AllowedDomains[i] = domain
	}
	return nil
}

func formDefinitionError(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrInvalidFormDefinition)
}

// FormSubmission is one filled-in form, spam included.
//
// The values are stored as a name → value map rather than as columns because
// the field set is defined per form at runtime; checkbox values are normalised
// to "true"/"false" by the writer.
type FormSubmission struct {
	BaseModel
	// ID of the form that was submitted.
	FormID uint `gorm:"not null;index" json:"form_id" example:"4" readonly:"true"`

	// Submitted values keyed by field name: every field of the definition is
	// present, values are trimmed, checkboxes are "true" or "false" and the
	// email is lower-cased. Emptied ({}) when the linked lead is erased.
	Data     map[string]string `gorm:"-" json:"data" readonly:"true"`
	DataJSON string            `gorm:"column:data;type:mediumtext" json:"-"`

	// Submitted address, lower-cased. After erasure of the linked lead it is
	// that lead's placeholder address.
	Email string `gorm:"type:varchar(255);index" json:"email" example:"jane@company.example" readonly:"true"`
	// Lifecycle state: received (final on arrival), pending (double opt-in
	// confirmation outstanding), confirmed (link confirmed) or spam (caught by
	// a protection layer).
	Status FormSubmissionStatus `gorm:"not null;type:varchar(20);index" json:"status" example:"received" readonly:"true"`
	// Protection layer that caught a spam submission: time_trap (submitted
	// under 3 seconds or over 24 hours after the definition was served),
	// domain (origin not in allowed_domains), honeypot (decoy input filled) or
	// captcha (token missing, rejected or unverifiable). Empty string for every
	// other status.
	SpamReason string `gorm:"type:varchar(100)" json:"spam_reason" enums:",honeypot,time_trap,captcha,domain" example:""`
	// Lead the submission created or added a note to; null for spam and
	// pending submissions and for forms with create_lead off.
	LeadID *uint `gorm:"index" json:"lead_id" example:"57" extensions:"x-nullable" readonly:"true"`
	// Client IP as resolved through the trusted proxies (TRUSTED_PROXIES).
	// Emptied when the linked lead is erased.
	IPAddress string `gorm:"type:varchar(45)" json:"ip_address" example:"203.0.113.24" readonly:"true"`
	// Browser User-Agent header, cut to 255 characters. Emptied when the
	// linked lead is erased.
	UserAgent string `gorm:"type:varchar(255)" json:"user_agent" example:"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5)" readonly:"true"`
	// Page the form was submitted from, as reported by the embed script
	// (page_url; client-supplied, not verified), cut to 512 characters.
	// Emptied when the linked lead is erased.
	Referrer string `gorm:"type:varchar(512)" json:"referrer" example:"https://www.example.com/pricing" readonly:"true"`
	// When the double opt-in link was confirmed (RFC 3339); null otherwise.
	ConfirmedAt *time.Time `json:"confirmed_at" example:"2026-10-30T09:15:00Z" extensions:"x-nullable" readonly:"true"`
}

// BeforeSave serializes the submitted values into their TEXT column.
func (s *FormSubmission) BeforeSave(tx *gorm.DB) error {
	encoded, err := encodeJSONStringMap(s.Data)
	if err != nil {
		return fmt.Errorf("form submission data: %w", err)
	}
	s.DataJSON = encoded
	return nil
}

// AfterFind restores the submitted values and guarantees the map marshals as
// `{}` rather than `null`.
func (s *FormSubmission) AfterFind(tx *gorm.DB) error {
	s.Data = decodeJSONStringMap(s.DataJSON)
	return nil
}

// FormConfirmationToken authorizes the double-opt-in confirmation of a single
// submission. Only the HMAC-SHA256 hash of the opaque token is stored; the raw
// value exists solely in the link mailed to the address that was submitted.
// A token is spendable only while UsedAt is nil and ExpiresAt is in the future.
type FormConfirmationToken struct {
	BaseModel
	SubmissionID uint       `gorm:"not null;index" json:"submission_id"`
	TokenHash    string     `gorm:"not null;type:varchar(64);uniqueIndex" json:"-"`
	ExpiresAt    time.Time  `gorm:"not null" json:"expires_at"`
	UsedAt       *time.Time `json:"used_at,omitempty"`
}

// encodeJSONStringMap serializes a submission's values for storage in a TEXT
// column, with the same empty-string convention as encodeJSONSlice.
func encodeJSONStringMap(values map[string]string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// decodeJSONStringMap is the inverse of encodeJSONStringMap. It never returns
// nil and never fails: a column that does not parse was written by something
// other than this application, and one such row must not fail a list query.
func decodeJSONStringMap(raw string) map[string]string {
	values := make(map[string]string)
	if strings.TrimSpace(raw) == "" {
		return values
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return make(map[string]string)
	}
	return values
}
