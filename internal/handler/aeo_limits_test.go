package handler

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// bindingMax returns the `max=` bound that applies to a string field or, for a
// slice, to each of its elements: the last `max=` in the tag, since the one
// before `dive` bounds the slice length.
func bindingMax(t *testing.T, structType interface{}, fieldName string) int {
	t.Helper()

	field, ok := reflect.TypeOf(structType).FieldByName(fieldName)
	require.Truef(t, ok, "field %s not found", fieldName)

	limit := -1
	for _, rule := range strings.Split(field.Tag.Get("binding"), ",") {
		if value, found := strings.CutPrefix(rule, "max="); found {
			parsed, err := strconv.Atoi(value)
			require.NoError(t, err)
			limit = parsed
		}
	}
	require.NotEqualf(t, -1, limit, "field %s has no max= binding", fieldName)
	return limit
}

// Struct tags cannot reference constants, so the request bounds are literals.
// This keeps them tied to the limits in internal/models, which are in turn
// tested against the column sizes.
func TestAEORequestBoundsMatchTheColumnLimits(t *testing.T) {
	cases := []struct {
		name       string
		structType interface{}
		field      string
		limit      int
	}{
		{"competitor name", AEOCompetitorRequest{}, "Name", models.AEOCompetitorNameMaxLength},
		{"brand name", SaveAEOProfileRequest{}, "BrandName", models.AEOBrandNameMaxLength},
		{"created prompt text", CreateAEOPromptsRequest{}, "Prompts", models.AEOPromptTextMaxLength},
		{"updated prompt text", UpdateAEOPromptRequest{}, "Text", models.AEOPromptTextMaxLength},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.limit, bindingMax(t, tc.structType, tc.field))
		})
	}
}
