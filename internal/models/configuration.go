package models

import (
	"encoding/json"
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
	IsSystem     bool                  `gorm:"default:false" json:"is_system"`     // System configs cannot be deleted
	IsReadOnly   bool                  `gorm:"default:false" json:"is_read_only"` // Read-only configs cannot be modified via API
	ValidValues  string                `gorm:"type:text" json:"valid_values"`     // JSON array of valid values for validation
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

// SetValue sets the configuration value with proper type conversion
func (c *Configuration) SetValue(value interface{}) error {
	switch c.Type {
	case ConfigTypeString:
		if str, ok := value.(string); ok {
			c.Value = str
		} else {
			c.Value = ""
		}
	case ConfigTypeBoolean:
		if val, ok := value.(bool); ok {
			if val {
				c.Value = "true"
			} else {
				c.Value = "false"
			}
		} else {
			c.Value = "false"
		}
	case ConfigTypeInteger, ConfigTypeFloat, ConfigTypeJSON, ConfigTypeArray:
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

// IsValidValue checks if the provided value is valid according to ValidValues constraint
func (c *Configuration) IsValidValue(value interface{}) bool {
	if c.ValidValues == "" {
		return true
	}
	
	var validValues []interface{}
	if err := json.Unmarshal([]byte(c.ValidValues), &validValues); err != nil {
		return true // If we can't parse valid values, allow anything
	}
	
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
			Value:        "GoCRM",
			Type:         ConfigTypeString,
			Category:     CategoryGeneral,
			Description:  "Company name displayed in the application",
			DefaultValue: "GoCRM",
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
	}
}