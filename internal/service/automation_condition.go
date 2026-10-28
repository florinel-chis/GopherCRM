package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
)

// automationRecord is the lead or deal a run acts on, as read inside the step
// transaction (or, for a dry run, as read once and then simulated). ownerID
// is kept apart from the row so a simulated assign can feed a later
// condition without touching the loaded struct.
type automationRecord struct {
	entityType string
	id         uint
	ownerID    uint
	lead       *models.Lead
	deal       *models.Deal
}

func recordFromLead(lead *models.Lead) *automationRecord {
	return &automationRecord{entityType: models.AutomationEntityLead, id: lead.ID, ownerID: lead.OwnerID, lead: lead}
}

func recordFromDeal(deal *models.Deal) *automationRecord {
	return &automationRecord{entityType: models.AutomationEntityDeal, id: deal.ID, ownerID: deal.OwnerID, deal: deal}
}

// automationFieldKind is the type a condition field is compared as.
type automationFieldKind int

const (
	automationFieldString automationFieldKind = iota
	automationFieldNumber
	automationFieldMoney
)

// automationConditionFields is the allowlist of condition fields per entity
// type. Only workflow fields and ids are comparable; no name, address or note
// ever is.
var automationConditionFields = map[string]map[string]automationFieldKind{
	models.AutomationEntityLead: {
		"status":         automationFieldString,
		"source":         automationFieldString,
		"classification": automationFieldString,
		"company":        automationFieldString,
		"owner_id":       automationFieldNumber,
	},
	models.AutomationEntityDeal: {
		"stage":        automationFieldString,
		"source":       automationFieldString,
		"currency":     automationFieldString,
		"amount_cents": automationFieldMoney,
		"probability":  automationFieldNumber,
		"owner_id":     automationFieldNumber,
	},
}

// Condition operators per field kind.
var (
	automationStringOperators = map[string]bool{
		"eq": true, "neq": true, "in": true, "not_in": true, "contains": true, "is_empty": true, "is_not_empty": true,
	}
	automationNumberOperators = map[string]bool{
		"eq": true, "neq": true, "gt": true, "gte": true, "lt": true, "lte": true,
	}
)

// stringValue returns the record's value of a string field.
func (r *automationRecord) stringValue(field string) string {
	switch r.entityType {
	case models.AutomationEntityLead:
		if r.lead == nil {
			return ""
		}
		switch field {
		case "status":
			return string(r.lead.Status)
		case "source":
			return r.lead.Source
		case "classification":
			return string(r.lead.Classification)
		case "company":
			return r.lead.Company
		}
	case models.AutomationEntityDeal:
		if r.deal == nil {
			return ""
		}
		switch field {
		case "stage":
			return string(r.deal.Stage)
		case "source":
			return r.deal.Source
		case "currency":
			return r.deal.Currency
		}
	}
	return ""
}

// numberValue returns the record's value of a numeric field. amount_cents is
// a number too, guarded by the currency check in evaluateCondition.
func (r *automationRecord) numberValue(field string) float64 {
	switch field {
	case "owner_id":
		return float64(r.ownerID)
	case "amount_cents":
		if r.deal != nil {
			return float64(r.deal.AmountCents)
		}
	case "probability":
		if r.deal != nil {
			return float64(r.deal.Probability)
		}
	}
	return 0
}

// validateCondition refuses an unknown field, an operator the field's kind
// does not support, a value of the wrong JSON type, or a money comparison
// without a currency. It is the rule-time half of evaluateCondition.
func validateCondition(entityType string, cfg models.AutomationStepConfig) error {
	kind, ok := automationConditionFields[entityType][cfg.Field]
	if !ok {
		return fmt.Errorf("unknown condition field %q for %s rules", cfg.Field, entityType)
	}
	raw := strings.TrimSpace(string(cfg.Value))
	hasValue := raw != "" && raw != "null"
	switch kind {
	case automationFieldString:
		if !automationStringOperators[cfg.Operator] {
			return fmt.Errorf("operator %q is not allowed on field %q", cfg.Operator, cfg.Field)
		}
		switch cfg.Operator {
		case "is_empty", "is_not_empty":
			if hasValue {
				return fmt.Errorf("operator %q takes no value", cfg.Operator)
			}
		case "in", "not_in":
			var values []string
			if !hasValue || json.Unmarshal(cfg.Value, &values) != nil || len(values) == 0 {
				return fmt.Errorf("operator %q needs a non-empty array of strings", cfg.Operator)
			}
		default:
			var value string
			if !hasValue || json.Unmarshal(cfg.Value, &value) != nil {
				return fmt.Errorf("operator %q needs a string value", cfg.Operator)
			}
		}
	case automationFieldNumber, automationFieldMoney:
		if !automationNumberOperators[cfg.Operator] {
			return fmt.Errorf("operator %q is not allowed on field %q", cfg.Operator, cfg.Field)
		}
		var value float64
		if !hasValue || json.Unmarshal(cfg.Value, &value) != nil {
			return fmt.Errorf("operator %q needs a numeric value", cfg.Operator)
		}
		if kind == automationFieldMoney && !dealCurrencyPattern.MatchString(cfg.Currency) {
			return fmt.Errorf("a condition on %q needs a three-letter currency", cfg.Field)
		}
	}
	if cfg.Currency != "" && kind != automationFieldMoney {
		return fmt.Errorf("currency only applies to a condition on amount_cents")
	}
	return nil
}

// evaluateCondition evaluates a validated condition against the record. A
// condition on amount_cents is true only when the deal's currency equals the
// condition's currency: amounts in different currencies are never compared.
func evaluateCondition(cfg models.AutomationStepConfig, rec *automationRecord) (bool, error) {
	kind, ok := automationConditionFields[rec.entityType][cfg.Field]
	if !ok {
		return false, fmt.Errorf("unknown condition field %q: %w", cfg.Field, apperrors.ErrValidation)
	}
	switch kind {
	case automationFieldString:
		actual := rec.stringValue(cfg.Field)
		switch cfg.Operator {
		case "is_empty":
			return strings.TrimSpace(actual) == "", nil
		case "is_not_empty":
			return strings.TrimSpace(actual) != "", nil
		case "in", "not_in":
			var values []string
			if err := json.Unmarshal(cfg.Value, &values); err != nil {
				return false, fmt.Errorf("condition value: %w", apperrors.ErrValidation)
			}
			found := false
			for _, value := range values {
				if value == actual {
					found = true
					break
				}
			}
			if cfg.Operator == "in" {
				return found, nil
			}
			return !found, nil
		default:
			var value string
			if err := json.Unmarshal(cfg.Value, &value); err != nil {
				return false, fmt.Errorf("condition value: %w", apperrors.ErrValidation)
			}
			switch cfg.Operator {
			case "eq":
				return actual == value, nil
			case "neq":
				return actual != value, nil
			case "contains":
				return strings.Contains(strings.ToLower(actual), strings.ToLower(value)), nil
			}
		}
	case automationFieldNumber, automationFieldMoney:
		if kind == automationFieldMoney && (rec.deal == nil || rec.deal.Currency != cfg.Currency) {
			return false, nil
		}
		var value float64
		if err := json.Unmarshal(cfg.Value, &value); err != nil {
			return false, fmt.Errorf("condition value: %w", apperrors.ErrValidation)
		}
		actual := rec.numberValue(cfg.Field)
		switch cfg.Operator {
		case "eq":
			return actual == value, nil
		case "neq":
			return actual != value, nil
		case "gt":
			return actual > value, nil
		case "gte":
			return actual >= value, nil
		case "lt":
			return actual < value, nil
		case "lte":
			return actual <= value, nil
		}
	}
	return false, fmt.Errorf("unknown condition operator %q: %w", cfg.Operator, apperrors.ErrValidation)
}

// --- Placeholders --------------------------------------------------------------

// automationPlaceholderPattern matches {{ name }} in a step's text fields.
var automationPlaceholderPattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.]*)\s*\}\}`)

// automationPlaceholders is the allowlist of placeholders per entity type for
// the text of a send_email and a notify step. The rendered text goes into a
// staff email, which is not stored, or into a notification, which is
// hard-deleted with the lead it refers to and with its recipient; it never
// goes into a log line.
var automationPlaceholders = map[string]map[string]bool{
	models.AutomationEntityLead: {
		"lead.id": true, "lead.first_name": true, "lead.last_name": true, "lead.email": true,
		"lead.phone": true, "lead.company": true, "lead.status": true, "lead.source": true,
		"owner.first_name": true, "owner.last_name": true, "owner.email": true,
	},
	models.AutomationEntityDeal: {
		"deal.id": true, "deal.title": true, "deal.stage": true, "deal.amount": true,
		"deal.currency": true, "deal.source": true,
		"owner.first_name": true, "owner.last_name": true, "owner.email": true,
	},
}

// automationTaskPlaceholders is the narrower allowlist for the title and
// description of a create_task step. A task is a business record that
// survives the erasure of the lead it links to (the link resolves, the person
// does not), so its text may carry only what the erasure keeps: ids, the
// lead's status and source, and the deal's fields, which hold no personal
// data. The lead's name, email, phone and company — scrubbed by the erasure —
// and the owner's name and address are not available here, so an erased
// person cannot outlive the erasure through a task the rule created.
var automationTaskPlaceholders = map[string]map[string]bool{
	models.AutomationEntityLead: {
		"lead.id": true, "lead.status": true, "lead.source": true,
	},
	models.AutomationEntityDeal: {
		"deal.id": true, "deal.title": true, "deal.stage": true, "deal.amount": true,
		"deal.currency": true, "deal.source": true,
	},
}

// unknownPlaceholders returns every placeholder of an email or notification
// text that the entity type does not know, in order of appearance.
func unknownPlaceholders(entityType, text string) []string {
	return unknownPlaceholdersIn(automationPlaceholders[entityType], text)
}

// unknownTaskPlaceholders is unknownPlaceholders for the text of a
// create_task step, against the narrower task allowlist.
func unknownTaskPlaceholders(entityType, text string) []string {
	return unknownPlaceholdersIn(automationTaskPlaceholders[entityType], text)
}

func unknownPlaceholdersIn(allowed map[string]bool, text string) []string {
	var unknown []string
	for _, match := range automationPlaceholderPattern.FindAllStringSubmatch(text, -1) {
		if !allowed[match[1]] {
			unknown = append(unknown, match[1])
		}
	}
	return unknown
}

// renderPlaceholders substitutes the record's and its owner's values into an
// email or notification text. An unknown placeholder (impossible for a
// validated rule) renders empty.
func renderPlaceholders(text string, rec *automationRecord, owner *models.User) string {
	return automationPlaceholderPattern.ReplaceAllStringFunc(text, func(match string) string {
		name := automationPlaceholderPattern.FindStringSubmatch(match)[1]
		return placeholderValue(name, rec, owner)
	})
}

// renderTaskPlaceholders is renderPlaceholders for the text of a task: only
// the task allowlist is substituted, so a personal placeholder renders empty
// even in a rule stored before the allowlist was narrowed.
func renderTaskPlaceholders(text string, rec *automationRecord) string {
	allowed := automationTaskPlaceholders[rec.entityType]
	return automationPlaceholderPattern.ReplaceAllStringFunc(text, func(match string) string {
		name := automationPlaceholderPattern.FindStringSubmatch(match)[1]
		if !allowed[name] {
			return ""
		}
		return placeholderValue(name, rec, nil)
	})
}

func placeholderValue(name string, rec *automationRecord, owner *models.User) string {
	switch name {
	case "owner.first_name":
		if owner != nil {
			return owner.FirstName
		}
	case "owner.last_name":
		if owner != nil {
			return owner.LastName
		}
	case "owner.email":
		if owner != nil {
			return owner.Email
		}
	}
	if rec.lead != nil {
		switch name {
		case "lead.id":
			return strconv.FormatUint(uint64(rec.lead.ID), 10)
		case "lead.first_name":
			return rec.lead.FirstName
		case "lead.last_name":
			return rec.lead.LastName
		case "lead.email":
			return rec.lead.Email
		case "lead.phone":
			return rec.lead.Phone
		case "lead.company":
			return rec.lead.Company
		case "lead.status":
			return string(rec.lead.Status)
		case "lead.source":
			return rec.lead.Source
		}
	}
	if rec.deal != nil {
		switch name {
		case "deal.id":
			return strconv.FormatUint(uint64(rec.deal.ID), 10)
		case "deal.title":
			return rec.deal.Title
		case "deal.stage":
			return string(rec.deal.Stage)
		case "deal.amount":
			return FormatMinorUnits(rec.deal.AmountCents, rec.deal.Currency)
		case "deal.currency":
			return rec.deal.Currency
		case "deal.source":
			return rec.deal.Source
		}
	}
	return ""
}
