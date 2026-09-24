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

// varcharSize matches an explicit `type:varchar(N)` column type.
var varcharSize = regexp.MustCompile(`(?i)^varchar\((\d+)\)$`)

// columnSize returns the declared character size of a sized string column, read
// from the parsed GORM schema rather than from a copy of the tag, so the test
// sees exactly what AutoMigrate creates.
func columnSize(t *testing.T, model interface{}, column string) int {
	t.Helper()

	parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	field := parsed.LookUpField(column)
	require.NotNilf(t, field, "column %q not found on %s", column, parsed.Name)

	if match := varcharSize.FindStringSubmatch(string(field.DataType)); match != nil {
		size, err := strconv.Atoi(match[1])
		require.NoError(t, err)
		return size
	}
	require.NotZerof(t, field.Size, "column %s.%s has no declared size", parsed.Table, column)
	return field.Size
}

// TestAEOLengthLimitsMatchTheColumns guards the length limits the AEO code
// validates and filters against. If a column is resized without updating the
// limit (or the other way round), MySQL starts rejecting inserts that the
// in-memory SQLite suite happily accepts.
func TestAEOLengthLimitsMatchTheColumns(t *testing.T) {
	cases := []struct {
		name   string
		model  interface{}
		column string
		limit  int
	}{
		{"citation url", &AEOCitation{}, "url", AEOCitationURLMaxLength},
		{"citation domain", &AEOCitation{}, "domain", AEOCitationDomainMaxLength},
		{"citation competitor name", &AEOCitation{}, "competitor_name", AEOCompetitorNameMaxLength},
		{"profile brand name", &AEOProfile{}, "brand_name", AEOBrandNameMaxLength},
		{"prompt text", &AEOPrompt{}, "text", AEOPromptTextMaxLength},
		{"answer provider", &AEOAnswer{}, "provider", AEOAnswerProviderMaxLength},
		{"answer model", &AEOAnswer{}, "model", AEOAnswerModelMaxLength},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, columnSize(t, tc.model, tc.column), tc.limit)
		})
	}
}
