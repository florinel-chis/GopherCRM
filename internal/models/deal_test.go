package models

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

// columnType reads the declared data type of a field, for columns that are not
// varchars.
func columnType(t *testing.T, model interface{}, fieldName string) string {
	t.Helper()
	parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	field := parsed.LookUpField(fieldName)
	require.NotNil(t, field, "%s has no field %s", parsed.Name, fieldName)
	return string(field.DataType)
}

// columnDefault reads the column default GORM would emit for a field; "" when
// the field declares none.
func columnDefault(t *testing.T, model interface{}, fieldName string) string {
	t.Helper()
	parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	field := parsed.LookUpField(fieldName)
	require.NotNil(t, field, "%s has no field %s", parsed.Name, fieldName)
	return field.DefaultValue
}

// The handler enforces the Deal* length constants; the migration and
// AutoMigrate enforce the `type:...` tags. This test is what keeps the two in
// step: widen a column and forget the constant, or the other way round, and
// it fails.
func TestDealColumnLimitsMatchTheModel(t *testing.T) {
	for _, tc := range []struct {
		field string
		limit int
	}{
		{"Title", DealTitleMaxLength},
		{"Stage", DealStageMaxLength},
		{"LostReason", DealLostReasonMaxLength},
		{"Source", DealSourceMaxLength},
	} {
		assert.Equal(t, columnSize(t, &Deal{}, tc.field), tc.limit, "limit for Deal.%s disagrees with its column width", tc.field)
	}
	// char(3) is not a varchar, so it is pinned by its declared type.
	assert.Equal(t, "char(3)", columnType(t, &Deal{}, "Currency"))
	assert.Equal(t, 3, DealCurrencyLength)
	for _, field := range []string{"FromStage", "ToStage"} {
		assert.Equal(t, DealStageMaxLength, columnSize(t, &DealStageChange{}, field), "DealStageChange.%s", field)
	}
}

// Every stage has to fit the column it is stored in, and the list is the five
// fixed values in pipeline order.
func TestDealStagesAreFixedAndFitTheColumn(t *testing.T) {
	assert.Equal(t, []DealStage{"qualification", "proposal", "negotiation", "won", "lost"}, DealStages)
	for _, stage := range DealStages {
		assert.LessOrEqual(t, len(stage), DealStageMaxLength, stage)
		assert.True(t, stage.IsValid(), stage)
	}
	for _, bad := range []DealStage{"", "Won", "closed", "qualification "} {
		assert.False(t, bad.IsValid(), "%q must not be a stage", bad)
	}
}

// The per-stage defaults from the spec, and which stages close a deal.
func TestDealStageDefaultsAndClosedness(t *testing.T) {
	for _, tc := range []struct {
		stage       DealStage
		probability int
		closed      bool
	}{
		{DealStageQualification, 10, false},
		{DealStageProposal, 40, false},
		{DealStageNegotiation, 70, false},
		{DealStageWon, 100, true},
		{DealStageLost, 0, true},
	} {
		assert.Equalf(t, tc.probability, tc.stage.DefaultProbability(), "%s default probability", tc.stage)
		assert.Equalf(t, tc.closed, tc.stage.IsClosed(), "%s closedness", tc.stage)
	}
}

// Probability deliberately has no column default: a GORM default would turn an
// explicit 0 (a lost deal) into the default on create.
func TestDealProbabilityHasNoColumnDefault(t *testing.T) {
	assert.Empty(t, columnDefault(t, &Deal{}, "Probability"))
}

// expected_close_date is a calendar date on the wire, whatever time.Time would
// print, and parses back to midnight of that day.
func TestDealJSONRendersTheExpectedCloseDateAsADate(t *testing.T) {
	date, err := ParseDealDate("2026-12-31")
	require.NoError(t, err)
	assert.Equal(t, 0, date.Hour())
	assert.Equal(t, "2026-12-31", date.Format(DealDateLayout))

	encoded, err := json.Marshal(&Deal{Title: "T", Currency: "EUR", ExpectedCloseDate: &date})
	require.NoError(t, err)
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, "2026-12-31", decoded["expected_close_date"])
	assert.Equal(t, "T", decoded["title"], "the other fields still render")

	encoded, err = json.Marshal(Deal{Title: "T", Currency: "EUR"})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "expected_close_date", "a nil date is omitted")

	for _, bad := range []string{"31/12/2026", "2026-13-01", "2026-12-31T00:00:00Z", "yesterday"} {
		_, err := ParseDealDate(bad)
		assert.Errorf(t, err, "%q must not parse", bad)
	}
	_ = time.Now
}
