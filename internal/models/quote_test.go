package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Line arithmetic on minor units: the gross is quantity × unit price and the
// line total rounds half up after the basis-point discount.
func TestQuoteLineTotalRoundsHalfUp(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		quantity, price, bp      int64
		wantGross, wantLineTotal int64
	}{
		{"no discount", 3, 999, 0, 2997, 2997},
		{"half a cent rounds up", 1, 1, 5000, 1, 1},
		{"a third rounds to the nearest cent", 1, 3, 3333, 3, 2},
		{"a full discount is zero", 7, 1000, 10000, 7000, 0},
		{"12.5 percent off 10.00", 1, 1000, 1250, 1000, 875},
		{"zero quantity is zero", 0, 999, 0, 0, 0},
		{"free item", 5, 0, 1250, 0, 0},
		{"the bound itself fits", 1, QuoteAmountCentsMax, 1, QuoteAmountCentsMax, QuoteAmountCentsMax - 100_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gross, lineTotal := ComputeQuoteLineTotal(tc.quantity, tc.price, tc.bp)
			assert.Equal(t, tc.wantGross, gross, "gross")
			assert.Equal(t, tc.wantLineTotal, lineTotal, "line total")
		})
	}
}

// The quote totals are sums over the lines: subtotal of the gross amounts,
// total of the stored line totals, and the discount is the difference.
func TestQuoteTotalsSumLines(t *testing.T) {
	subtotal, discount, total := ComputeQuoteTotals(nil)
	assert.Zero(t, subtotal)
	assert.Zero(t, discount)
	assert.Zero(t, total)

	lines := []QuoteLine{
		{Quantity: 3, UnitPriceCents: 999, DiscountBP: 0, LineTotalCents: 2997},
		{Quantity: 1, UnitPriceCents: 1000, DiscountBP: 1250, LineTotalCents: 875},
		{Quantity: 7, UnitPriceCents: 1000, DiscountBP: 10000, LineTotalCents: 0},
	}
	for i := range lines {
		_, lines[i].LineTotalCents = ComputeQuoteLineTotal(lines[i].Quantity, lines[i].UnitPriceCents, lines[i].DiscountBP)
	}
	subtotal, discount, total = ComputeQuoteTotals(lines)
	assert.Equal(t, int64(2997+1000+7000), subtotal)
	assert.Equal(t, int64(2997+875+0), total)
	assert.Equal(t, subtotal-total, discount)
	assert.Equal(t, int64(125+7000), discount)
}

// The handler and the service enforce the Quote* constants; the migration and
// AutoMigrate enforce the `type:...` tags. This test is what keeps the two in
// step, and pins the bounds of the arithmetic to the deal cap.
func TestQuoteColumnWidthsMatchConstants(t *testing.T) {
	assert.Equal(t, QuoteTitleMaxLength, columnSize(t, &Quote{}, "Title"))
	assert.Equal(t, QuoteStatusMaxLength, columnSize(t, &Quote{}, "Status"))
	assert.Equal(t, "char(3)", columnType(t, &Quote{}, "Currency"))
	assert.Equal(t, 3, QuoteCurrencyLength)
	assert.Equal(t, QuoteLineDescriptionMaxLength, columnSize(t, &QuoteLine{}, "Description"))
	for _, field := range []string{"FromStatus", "ToStatus"} {
		assert.Equal(t, QuoteStatusMaxLength, columnSize(t, &QuoteStatusChange{}, field), "QuoteStatusChange.%s", field)
	}

	assert.Equal(t, 200, QuoteTitleMaxLength)
	assert.Equal(t, 20, QuoteStatusMaxLength)
	assert.Equal(t, 255, QuoteLineDescriptionMaxLength)
	assert.Equal(t, DealAmountCentsMax, QuoteAmountCentsMax)
	assert.Equal(t, int64(1_000_000_000_000), QuoteAmountCentsMax)
	assert.Equal(t, int64(1_000_000), QuoteLineQuantityMax)
	assert.Equal(t, int64(10000), QuoteDiscountBPMax)
	assert.Equal(t, 100, QuoteLinesMax)
	assert.Equal(t, LeadNotesMaxBytes, QuoteNotesMaxBytes)

	// The totals and the line's bound columns carry no default, so an explicit
	// 0 survives a create.
	for _, field := range []string{"SubtotalCents", "DiscountCents", "TotalCents"} {
		assert.Empty(t, columnDefault(t, &Quote{}, field), "Quote.%s", field)
	}
	for _, field := range []string{"Position", "Quantity", "UnitPriceCents", "DiscountBP", "LineTotalCents"} {
		assert.Empty(t, columnDefault(t, &QuoteLine{}, field), "QuoteLine.%s", field)
	}
	assert.Equal(t, "'draft'", columnDefault(t, &Quote{}, "Status"))
}

// Every status has to fit the column it is stored in, and the list is the five
// fixed values in lifecycle order.
func TestQuoteStatusesAreFixedAndFitTheColumn(t *testing.T) {
	assert.Equal(t, []QuoteStatus{"draft", "sent", "accepted", "rejected", "expired"}, QuoteStatuses)
	for _, status := range QuoteStatuses {
		assert.LessOrEqual(t, len(status), QuoteStatusMaxLength, status)
		assert.True(t, status.IsValid(), status)
	}
	for _, bad := range []QuoteStatus{"", "Sent", "closed", "draft "} {
		assert.False(t, bad.IsValid(), "%q must not be a status", bad)
	}
	assert.False(t, QuoteStatusDraft.IsClosed())
	assert.False(t, QuoteStatusSent.IsClosed())
	for _, closed := range []QuoteStatus{QuoteStatusAccepted, QuoteStatusRejected, QuoteStatusExpired} {
		assert.True(t, closed.IsClosed(), closed)
	}
}

// Exactly five moves are allowed; every other pair, the same status included,
// is refused.
func TestQuoteTransitionsAreExactlyTheFiveAllowedMoves(t *testing.T) {
	allowed := map[[2]QuoteStatus]bool{
		{QuoteStatusDraft, QuoteStatusSent}:    true,
		{QuoteStatusSent, QuoteStatusAccepted}: true,
		{QuoteStatusSent, QuoteStatusRejected}: true,
		{QuoteStatusSent, QuoteStatusExpired}:  true,
		{QuoteStatusSent, QuoteStatusDraft}:    true,
	}
	count := 0
	for _, from := range QuoteStatuses {
		for _, to := range QuoteStatuses {
			got := from.CanTransitionTo(to)
			assert.Equalf(t, allowed[[2]QuoteStatus{from, to}], got, "%s -> %s", from, to)
			if got {
				count++
			}
		}
	}
	assert.Equal(t, 5, count)
}

// The JSON of a quote adds the padded number, renders valid_until as a date and
// discloses only summaries of its associations.
func TestQuoteJSONAddsTheNumberAndSummaries(t *testing.T) {
	assert.Equal(t, "Q-000042", QuoteNumber(42))
	assert.Equal(t, "Q-1234567", QuoteNumber(1234567), "wider ids are not truncated")

	date, err := ParseDealDate("2026-12-31")
	require.NoError(t, err)
	quote := Quote{
		BaseModel: BaseModel{ID: 42}, Title: "Licences", Status: QuoteStatusDraft, Currency: "EUR", ValidUntil: &date,
		Customer: &Customer{BaseModel: BaseModel{ID: 7}, FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.com", Company: "Analytical"},
		Deal:     &Deal{BaseModel: BaseModel{ID: 3}, Title: "Engine", Stage: DealStageProposal},
		Owner:    &User{BaseModel: BaseModel{ID: 9}, FirstName: "Sales", LastName: "Rep", Email: "rep@example.com"},
	}
	encoded, err := json.Marshal(quote)
	require.NoError(t, err)
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, "Q-000042", decoded["number"])
	assert.Equal(t, "2026-12-31", decoded["valid_until"])
	assert.Equal(t, []interface{}{}, decoded["lines"], "no lines is an empty array, not null")
	customer := decoded["customer"].(map[string]interface{})
	assert.Equal(t, "Ada", customer["first_name"])
	assert.Equal(t, "Analytical", customer["company"])
	assert.NotContains(t, string(encoded), "ada@example.com", "the customer's contact details are not disclosed")
	assert.NotContains(t, string(encoded), "rep@example.com")
	deal := decoded["deal"].(map[string]interface{})
	assert.Equal(t, "Engine", deal["title"])
	assert.Equal(t, "proposal", deal["stage"])
	owner := decoded["owner"].(map[string]interface{})
	assert.Equal(t, float64(9), owner["id"])

	encoded, err = json.Marshal(Quote{BaseModel: BaseModel{ID: 1}, Title: "Bare"})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "valid_until", "a nil date is omitted")
	assert.NotContains(t, string(encoded), `"customer"`)

	changedBy := QuoteStatusChange{ID: 1, QuoteID: 42, ToStatus: QuoteStatusDraft, ChangedByID: 9, ChangedAt: time.Now(),
		ChangedBy: &User{BaseModel: BaseModel{ID: 9}, FirstName: "Sales", LastName: "Rep", Email: "rep@example.com"}}
	encoded, err = json.Marshal(changedBy)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"changed_by":{"id":9,"first_name":"Sales","last_name":"Rep"}`)
	assert.Contains(t, string(encoded), `"from_status":null`)
	assert.NotContains(t, string(encoded), "rep@example.com")
}
