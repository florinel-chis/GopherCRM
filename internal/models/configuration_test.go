package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfigurationSetValue_TypeStrictness pins the contract that SetValue
// refuses a value whose Go type does not match the entry's declared type.
// It used to coerce instead: a non-bool became "false" and a non-string became
// "", so a caller sending the wrong type got a success and a corrupted value.
func TestConfigurationSetValue_TypeStrictness(t *testing.T) {
	tests := []struct {
		name      string
		typ       ConfigurationType
		value     interface{}
		wantValue string
		wantErr   string
	}{
		// boolean
		{name: "boolean accepts true", typ: ConfigTypeBoolean, value: true, wantValue: "true"},
		{name: "boolean accepts false", typ: ConfigTypeBoolean, value: false, wantValue: "false"},
		{name: "boolean rejects string", typ: ConfigTypeBoolean, value: "yes", wantErr: "expected boolean value, got string"},
		{name: "boolean rejects number", typ: ConfigTypeBoolean, value: float64(1), wantErr: "expected boolean value, got float64"},

		// integer
		{name: "integer accepts integral float64", typ: ConfigTypeInteger, value: float64(5), wantValue: "5"},
		{name: "integer accepts zero", typ: ConfigTypeInteger, value: float64(0), wantValue: "0"},
		{name: "integer accepts negative", typ: ConfigTypeInteger, value: float64(-7), wantValue: "-7"},
		{name: "integer accepts native int", typ: ConfigTypeInteger, value: 24, wantValue: "24"},
		{name: "integer rejects fractional float64", typ: ConfigTypeInteger, value: float64(3.5), wantErr: "expected integer value"},
		{name: "integer rejects numeric string", typ: ConfigTypeInteger, value: "10", wantErr: "expected integer value, got string"},
		{name: "integer rejects bool", typ: ConfigTypeInteger, value: true, wantErr: "expected integer value, got bool"},

		// string
		{name: "string accepts text", typ: ConfigTypeString, value: "x", wantValue: "x"},
		{name: "string accepts empty", typ: ConfigTypeString, value: "", wantValue: ""},
		{name: "string rejects number", typ: ConfigTypeString, value: float64(5), wantErr: "expected string value, got float64"},
		{name: "string rejects bool", typ: ConfigTypeString, value: false, wantErr: "expected string value, got bool"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Configuration{Key: "test.key", Type: tt.typ, Value: "sentinel"}

			err := config.SetValue(tt.value)

			if tt.wantErr != "" {
				require.Error(t, err, "expected a type mismatch to be reported")
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Equal(t, "sentinel", config.Value, "a rejected value must leave the stored value untouched")
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantValue, config.Value)
		})
	}
}

// TestConfigurationSetValue_PermissiveTypes documents the variants that keep
// their JSON round trip: float, json and array accept whatever marshals.
func TestConfigurationSetValue_PermissiveTypes(t *testing.T) {
	tests := []struct {
		name      string
		typ       ConfigurationType
		value     interface{}
		wantValue string
	}{
		{name: "float keeps fractional value", typ: ConfigTypeFloat, value: float64(3.5), wantValue: "3.5"},
		{name: "float keeps integral value", typ: ConfigTypeFloat, value: float64(2), wantValue: "2"},
		{name: "array keeps element list", typ: ConfigTypeArray, value: []interface{}{"qualified", "new"}, wantValue: `["qualified","new"]`},
		{name: "json keeps object", typ: ConfigTypeJSON, value: map[string]interface{}{"a": float64(1)}, wantValue: `{"a":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Configuration{Key: "test.key", Type: tt.typ}

			require.NoError(t, config.SetValue(tt.value))
			assert.Equal(t, tt.wantValue, config.Value)
		})
	}
}

// TestConfigurationSetValue_RoundTripsThroughGetValueAs proves the stored form
// still parses back to the declared type, so strictness did not change the
// storage format.
func TestConfigurationSetValue_RoundTripsThroughGetValueAs(t *testing.T) {
	boolConfig := &Configuration{Type: ConfigTypeBoolean}
	require.NoError(t, boolConfig.SetValue(false))
	assert.Equal(t, false, boolConfig.GetValueAs())

	intConfig := &Configuration{Type: ConfigTypeInteger}
	require.NoError(t, intConfig.SetValue(float64(24)))
	assert.Equal(t, 24, intConfig.GetValueAs())

	stringConfig := &Configuration{Type: ConfigTypeString}
	require.NoError(t, stringConfig.SetValue(""))
	assert.Equal(t, "", stringConfig.GetValueAs())
}

// The answer-engine keys ship as empty, system-owned, sensitive string entries:
// empty so an engine keeps using its environment key until an administrator
// stores one, system so they cannot be deleted, sensitive so the value is
// encrypted at rest and never returned by the API.
func TestDefaultConfigurations_SeedsAnswerEngineKeys(t *testing.T) {
	byKey := map[string]Configuration{}
	for _, config := range DefaultConfigurations() {
		byKey[config.Key] = config
	}

	for _, key := range []string{
		"integration.aeo.anthropic_api_key",
		"integration.aeo.openai_api_key",
		"integration.aeo.gemini_api_key",
		"integration.aeo.moonshot_api_key",
		"integration.aeo.perplexity_api_key",
	} {
		config, ok := byKey[key]
		require.True(t, ok, "default configuration %q is missing", key)
		assert.True(t, config.IsSensitive, "%s must be sensitive", key)
		assert.True(t, config.IsSystem, "%s must be a system configuration", key)
		assert.False(t, config.IsReadOnly, "%s must stay writable from the API", key)
		assert.Equal(t, ConfigTypeString, config.Type)
		assert.Equal(t, CategoryIntegration, config.Category)
		assert.Equal(t, "", config.Value)
		assert.Equal(t, "", config.DefaultValue)
		assert.NotEmpty(t, config.Description)
	}
}

// Nothing else becomes sensitive by accident: the flag changes how a value is
// stored, so it must be a deliberate per-entry decision.
func TestDefaultConfigurations_OnlyAnswerEngineKeysAreSensitive(t *testing.T) {
	for _, config := range DefaultConfigurations() {
		if config.IsSensitive {
			assert.Contains(t, config.Key, "integration.aeo.", "unexpected sensitive default %q", config.Key)
		}
	}
}
