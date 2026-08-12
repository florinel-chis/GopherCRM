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
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Placeholder string   `json:"placeholder,omitempty"`
	HelpText    string   `json:"help_text,omitempty"`
	Options     []string `json:"options,omitempty"`
	MaxLength   int      `json:"max_length,omitempty"`
}

// Form is a lead-capture form definition plus everything that happens after a
// visitor submits it: notification mail, double opt-in, gated-content
// follow-up, spam protection and lead creation.
type Form struct {
	BaseModel
	Name        string `gorm:"not null;type:varchar(255)" json:"name"`
	Description string `gorm:"type:text" json:"description"`

	// PublicID is the only identifier ever exposed publicly. It is random
	// rather than the primary key so the public endpoints cannot be enumerated.
	PublicID string     `gorm:"not null;type:varchar(32);uniqueIndex" json:"public_id"`
	Status   FormStatus `gorm:"not null;type:varchar(20);default:'draft'" json:"status"`

	Fields     []FormFieldDef `gorm:"-" json:"fields"`
	FieldsJSON string         `gorm:"column:fields;type:text" json:"-"`

	SubmitAction    string `gorm:"not null;type:varchar(20);default:'message'" json:"submit_action"`
	ThankYouMessage string `gorm:"type:text" json:"thank_you_message"`
	RedirectURL     string `gorm:"type:varchar(512)" json:"redirect_url"`
	ConsentText     string `gorm:"type:text" json:"consent_text"`

	NotifyEmails     []string `gorm:"-" json:"notify_emails"`
	NotifyEmailsJSON string   `gorm:"column:notify_emails;type:text" json:"-"`

	DoubleOptIn         bool   `gorm:"not null;default:false" json:"double_opt_in"`
	ConfirmationSubject string `gorm:"type:varchar(255)" json:"confirmation_subject"`
	ConfirmationBody    string `gorm:"type:text" json:"confirmation_body"`
	FollowUpSubject     string `gorm:"type:varchar(255)" json:"follow_up_subject"`
	FollowUpBody        string `gorm:"type:text" json:"follow_up_body"`
	ContentURL          string `gorm:"type:varchar(512)" json:"content_url"`
	CaptchaEnabled      bool   `gorm:"not null;default:false" json:"captcha_enabled"`

	// CreateLead deliberately carries NO `default:true` column tag. GORM
	// substitutes a literal column default whenever the Go field holds its zero
	// value (callbacks.ConvertToCreateValues), so a form explicitly configured
	// not to create leads would be persisted with create_lead = true. The
	// "new forms create leads" default therefore belongs to the API layer,
	// which knows whether the client sent the flag at all.
	CreateLead     bool `gorm:"not null" json:"create_lead"`
	DefaultOwnerID uint `gorm:"index" json:"default_owner_id"`

	AllowedDomains     []string `gorm:"-" json:"allowed_domains"`
	AllowedDomainsJSON string   `gorm:"column:allowed_domains;type:text" json:"-"`

	CreatedByID uint `gorm:"index" json:"created_by_id"`
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

// normaliseFieldMaxLength fills in the per-type default and enforces the hard
// cap, so the submission validator can trust the number without re-deriving it.
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
	FormID uint `gorm:"not null;index" json:"form_id"`

	Data     map[string]string `gorm:"-" json:"data"`
	DataJSON string            `gorm:"column:data;type:text" json:"-"`

	Email       string               `gorm:"type:varchar(255);index" json:"email"`
	Status      FormSubmissionStatus `gorm:"not null;type:varchar(20);index" json:"status"`
	SpamReason  string               `gorm:"type:varchar(100)" json:"spam_reason"`
	LeadID      *uint                `gorm:"index" json:"lead_id"`
	IPAddress   string               `gorm:"type:varchar(45)" json:"ip_address"`
	UserAgent   string               `gorm:"type:varchar(255)" json:"user_agent"`
	Referrer    string               `gorm:"type:varchar(512)" json:"referrer"`
	ConfirmedAt *time.Time           `json:"confirmed_at"`
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
