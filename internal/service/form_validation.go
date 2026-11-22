package service

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/florinel-chis/gophercrm/internal/models"
)

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
