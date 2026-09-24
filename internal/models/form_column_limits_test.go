package models

import (
	"regexp"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

// formFieldColumns names every column a submitted field's value is written to.
// It mirrors the mapping in the form service (newSubmission and the lead
// creation path); the test below holds formFieldColumnLimits to it.
var formFieldColumns = map[string][]struct {
	model interface{}
	field string
}{
	FormFieldEmail: {{&FormSubmission{}, "Email"}, {&Lead{}, "Email"}},
	"first_name":   {{&Lead{}, "FirstName"}},
	"last_name":    {{&Lead{}, "LastName"}},
	"phone":        {{&Lead{}, "Phone"}},
	"company":      {{&Lead{}, "Company"}},
	"position":     {{&Lead{}, "Position"}},
}

var varcharPattern = regexp.MustCompile(`(?i)^varchar\((\d+)\)$`)

// columnSize reads the width GORM migrates a field with. The models declare
// most widths as an explicit `type:varchar(N)`, which GORM keeps as the data
// type rather than as Size, so both spellings are understood.
func columnSize(t *testing.T, model interface{}, fieldName string) int {
	t.Helper()

	parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	field := parsed.LookUpField(fieldName)
	require.NotNil(t, field, "%s has no field %s", parsed.Name, fieldName)

	if field.Size > 0 {
		return field.Size
	}
	match := varcharPattern.FindStringSubmatch(string(field.DataType))
	require.NotNil(t, match, "%s.%s declares no width (data type %q)", parsed.Name, fieldName, field.DataType)
	size, err := strconv.Atoi(match[1])
	require.NoError(t, err)
	return size
}

func TestFormFieldColumnLimitsMatchTheModels(t *testing.T) {
	for name := range formFieldColumnLimits {
		assert.Contains(t, formFieldColumns, name, "limit for %q is not checked against a column", name)
	}

	for name, columns := range formFieldColumns {
		narrowest := 0
		for _, column := range columns {
			size := columnSize(t, column.model, column.field)
			if narrowest == 0 || size < narrowest {
				narrowest = size
			}
		}

		limit, ok := FormFieldColumnLimit(name)
		require.True(t, ok, "field %q is written to a sized column but has no limit", name)
		assert.Equal(t, narrowest, limit, "limit for %q disagrees with its column width", name)
	}
}

func TestFormFieldColumnLimitIgnoresUnmappedFields(t *testing.T) {
	for _, name := range []string{"message", "budget", "name", ""} {
		_, ok := FormFieldColumnLimit(name)
		assert.False(t, ok, name)
	}
}
