package models

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

type ConfigurationType string

const (
	ConfigTypeString  ConfigurationType = "string"
	ConfigTypeBoolean ConfigurationType = "boolean"
	ConfigTypeInteger ConfigurationType = "integer"
	ConfigTypeFloat   ConfigurationType = "float"
	ConfigTypeJSON    ConfigurationType = "json"
	ConfigTypeArray   ConfigurationType = "array"
)

type ConfigurationCategory string

const (
	CategoryGeneral     ConfigurationCategory = "general"
	CategoryLeads       ConfigurationCategory = "leads"
	CategoryCustomers   ConfigurationCategory = "customers"
	CategoryTickets     ConfigurationCategory = "tickets"
	CategoryTasks       ConfigurationCategory = "tasks"
	CategorySecurity    ConfigurationCategory = "security"
	CategoryIntegration ConfigurationCategory = "integration"
	CategoryUI          ConfigurationCategory = "ui"
)

// Configuration represents a system configuration setting
type Configuration struct {
	BaseModel
	Key          string                `gorm:"uniqueIndex;not null;type:varchar(255);column:config_key" json:"key"`
	Value        string                `gorm:"type:text" json:"value"`
	Type         ConfigurationType     `gorm:"not null;type:varchar(20)" json:"type"`
	Category     ConfigurationCategory `gorm:"not null;type:varchar(50)" json:"category"`
	Description  string                `gorm:"type:varchar(500)" json:"description"`
	DefaultValue string                `gorm:"type:text" json:"default_value"`
	IsSystem     bool                  `gorm:"default:false" json:"is_system"`    // System configs cannot be deleted
	IsReadOnly   bool                  `gorm:"default:false" json:"is_read_only"` // Read-only configs cannot be modified via API
	ValidValues  string                `gorm:"type:text" json:"valid_values"`     // JSON array of valid values for validation
	// IsSensitive marks a secret: the value is encrypted at rest, is read
	// back only through the service's GetSecret, and is never included in an
	// API response — reads report whether it is set, not what it is.
	IsSensitive bool `gorm:"default:false" json:"is_sensitive"`
}

// GetValueAs returns the configuration value parsed as the specified type
func (c *Configuration) GetValueAs() interface{} {
	switch c.Type {
	case ConfigTypeBoolean:
		return c.Value == "true"
	case ConfigTypeInteger:
		var value int
		json.Unmarshal([]byte(c.Value), &value)
		return value
	case ConfigTypeFloat:
		var value float64
		json.Unmarshal([]byte(c.Value), &value)
		return value
	case ConfigTypeJSON, ConfigTypeArray:
		var value interface{}
		json.Unmarshal([]byte(c.Value), &value)
		return value
	default:
		return c.Value
	}
}

// SetValue stores the configuration value, rejecting a value whose type does not
// match the entry's declared type. It used to coerce instead — a non-bool became
// "false" and a non-string became "" — so a caller sending the wrong type was
// answered with a success and a corrupted value. On any error the stored value
// is left untouched.
//
// This is a type check only; the valid_values allowlist stays in IsValidValue,
// which the service applies first.
//
// Float, JSON and array entries keep their permissive JSON round trip: their
// stored form is whatever the value marshals to.
func (c *Configuration) SetValue(value interface{}) error {
	switch c.Type {
	case ConfigTypeString:
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected string value, got %T", value)
		}
		c.Value = str
	case ConfigTypeBoolean:
		val, ok := value.(bool)
		if !ok {
			return fmt.Errorf("expected boolean value, got %T", value)
		}
		if val {
			c.Value = "true"
		} else {
			c.Value = "false"
		}
	case ConfigTypeInteger:
		number, err := asInteger(value)
		if err != nil {
			return err
		}
		c.Value = strconv.FormatInt(number, 10)
	case ConfigTypeFloat, ConfigTypeJSON, ConfigTypeArray:
		bytes, err := json.Marshal(value)
		if err != nil {
			return err
		}
		c.Value = string(bytes)
	default:
		c.Value = ""
	}
	return nil
}

// asInteger narrows a value to an integer. A JSON number decodes to float64, so
// an integral float64 counts as an integer; a fractional one does not and is
// rejected rather than silently truncated.
func asInteger(value interface{}) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint:
		return unsignedToInteger(uint64(v))
	case uint64:
		return unsignedToInteger(v)
	case float32:
		return floatToInteger(float64(v))
	case float64:
		return floatToInteger(v)
	case json.Number:
		number, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("expected integer value, got %s", v.String())
		}
		return number, nil
	default:
		return 0, fmt.Errorf("expected integer value, got %T", value)
	}
}

func floatToInteger(v float64) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
		return 0, fmt.Errorf("expected integer value, got %v", v)
	}
	if v < float64(math.MinInt64) || v >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("integer value %v is out of range", v)
	}
	return int64(v), nil
}

func unsignedToInteger(v uint64) (int64, error) {
	if v > math.MaxInt64 {
		return 0, fmt.Errorf("integer value %d is out of range", v)
	}
	return int64(v), nil
}

// IsValidValue checks if the provided value is valid according to ValidValues constraint
func (c *Configuration) IsValidValue(value interface{}) bool {
	if c.ValidValues == "" {
		return true
	}

	var validValues []interface{}
	if err := json.Unmarshal([]byte(c.ValidValues), &validValues); err != nil {
		return true // If we can't parse valid values, allow anything
	}

	// For array/JSON types, check each element against valid values
	if c.Type == ConfigTypeArray {
		var elements []interface{}
		switch v := value.(type) {
		case []interface{}:
			elements = v
		case []string:
			for _, s := range v {
				elements = append(elements, s)
			}
		default:
			// Try to marshal and unmarshal to get a slice
			bytes, err := json.Marshal(value)
			if err != nil {
				return false
			}
			if err := json.Unmarshal(bytes, &elements); err != nil {
				return false
			}
		}

		for _, elem := range elements {
			if !isValueInList(elem, validValues) {
				return false
			}
		}
		return true
	}

	// For scalar types, check the value directly
	return isValueInList(value, validValues)
}

// isValueInList checks if a value matches any element in the list
func isValueInList(value interface{}, validValues []interface{}) bool {
	valueStr := ""
	if str, ok := value.(string); ok {
		valueStr = str
	} else {
		bytes, _ := json.Marshal(value)
		valueStr = string(bytes)
	}

	for _, valid := range validValues {
		validStr := ""
		if str, ok := valid.(string); ok {
			validStr = str
		} else {
			bytes, _ := json.Marshal(valid)
			validStr = string(bytes)
		}

		if valueStr == validStr {
			return true
		}
	}

	return false
}

// DefaultConfigurations returns the default system configurations
func DefaultConfigurations() []Configuration {
	return []Configuration{
		{
			Key:          "leads.conversion.allowed_statuses",
			Value:        `["qualified", "contacted"]`,
			Type:         ConfigTypeArray,
			Category:     CategoryLeads,
			Description:  "Lead statuses that allow conversion to customer",
			DefaultValue: `["qualified"]`,
			IsSystem:     true,
			IsReadOnly:   false,
			ValidValues:  `["new", "contacted", "qualified", "converted", "lost"]`,
		},
		{
			Key:          "leads.conversion.require_notes",
			Value:        "false",
			Type:         ConfigTypeBoolean,
			Category:     CategoryLeads,
			Description:  "Whether conversion notes are required when converting leads",
			DefaultValue: "false",
			IsSystem:     true,
			IsReadOnly:   false,
		},
		{
			Key:          "leads.conversion.auto_assign_owner",
			Value:        "true",
			Type:         ConfigTypeBoolean,
			Category:     CategoryLeads,
			Description:  "Whether to automatically assign the lead owner as customer owner",
			DefaultValue: "true",
			IsSystem:     true,
			IsReadOnly:   false,
		},
		{
			Key:          "ui.theme.primary_color",
			Value:        "#1976d2",
			Type:         ConfigTypeString,
			Category:     CategoryUI,
			Description:  "Primary theme color for the application",
			DefaultValue: "#1976d2",
			IsSystem:     false,
			IsReadOnly:   false,
		},
		{
			Key:          "general.company_name",
			Value:        "GopherCRM",
			Type:         ConfigTypeString,
			Category:     CategoryGeneral,
			Description:  "Company name displayed in the application",
			DefaultValue: "GopherCRM",
			IsSystem:     false,
			IsReadOnly:   false,
		},
		{
			Key:          "security.session_timeout_hours",
			Value:        "24",
			Type:         ConfigTypeInteger,
			Category:     CategorySecurity,
			Description:  "Session timeout in hours",
			DefaultValue: "24",
			IsSystem:     true,
			IsReadOnly:   false,
			ValidValues:  `[1, 8, 24, 48, 72, 168]`,
		},
		{
			Key:          "tickets.auto_assign_support",
			Value:        "true",
			Type:         ConfigTypeBoolean,
			Category:     CategoryTickets,
			Description:  "Whether to automatically assign tickets to available support users",
			DefaultValue: "false",
			IsSystem:     false,
			IsReadOnly:   false,
		},
		// Answer-engine credentials. They ship empty: an engine stays on its
		// environment key until an administrator stores one here, and a stored
		// key wins from the next run onwards without a restart.
		{
			Key:         "integration.aeo.anthropic_api_key",
			Type:        ConfigTypeString,
			Category:    CategoryIntegration,
			Description: "Anthropic API key for the answer-engine module (also used for prompt generation)",
			IsSystem:    true,
			IsSensitive: true,
		},
		{
			Key:         "integration.aeo.openai_api_key",
			Type:        ConfigTypeString,
			Category:    CategoryIntegration,
			Description: "OpenAI API key for the answer-engine module",
			IsSystem:    true,
			IsSensitive: true,
		},
		{
			Key:         "integration.aeo.gemini_api_key",
			Type:        ConfigTypeString,
			Category:    CategoryIntegration,
			Description: "Gemini API key for the answer-engine module",
			IsSystem:    true,
			IsSensitive: true,
		},
		{
			Key:         "integration.aeo.moonshot_api_key",
			Type:        ConfigTypeString,
			Category:    CategoryIntegration,
			Description: "Moonshot API key for the Kimi answer engine",
			IsSystem:    true,
			IsSensitive: true,
		},
		{
			Key:         "integration.aeo.perplexity_api_key",
			Type:        ConfigTypeString,
			Category:    CategoryIntegration,
			Description: "Perplexity API key for the answer-engine module",
			IsSystem:    true,
			IsSensitive: true,
		},
	}
}
