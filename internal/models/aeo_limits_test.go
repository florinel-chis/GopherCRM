package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The columns are measured with columnSize (form_column_limits_test.go), which
// reads the width from the parsed GORM schema, so the test sees exactly what
// AutoMigrate creates.

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
