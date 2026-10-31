package service

import (
	"regexp"
	"strconv"
	"sync"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

// TestLeadExternalIDMaxLengthMatchesTheColumn keeps the external_id lookup
// limit equal to the width AutoMigrate gives leads.external_id, read from the
// parsed GORM schema.
func TestLeadExternalIDMaxLengthMatchesTheColumn(t *testing.T) {
	parsed, err := schema.Parse(&models.Lead{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	field := parsed.LookUpField("external_id")
	require.NotNil(t, field)

	size := field.Size
	if size == 0 {
		match := regexp.MustCompile(`(?i)^varchar\((\d+)\)$`).FindStringSubmatch(string(field.DataType))
		require.NotNil(t, match, "external_id declares no width (data type %q)", field.DataType)
		size, err = strconv.Atoi(match[1])
		require.NoError(t, err)
	}
	assert.Equal(t, LeadExternalIDMaxLength, size)
}
