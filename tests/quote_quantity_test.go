package tests

// Fractional quote line quantities (#343) over HTTP on SQLite: the quote
// routes behind the real RequestID, ErrorHandler and JWT Auth middleware, the
// real repositories and services and the full schema of
// models.MigrateDatabase (the fixture of quote_tax_test.go).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// quoteQuantityLines decodes the raw lines of a response, keeping each
// quantity as the literal the server wrote.
func quoteQuantityLines(t *testing.T, raw string) []map[string]json.RawMessage {
	t.Helper()
	var envelope struct {
		Data struct {
			Lines []map[string]json.RawMessage `json:"lines"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &envelope), raw)
	return envelope.Data.Lines
}

func TestQuoteFractionalQuantityAPISQLite(t *testing.T) {
	f := newQuoteTaxAPIFixture(t)

	status, envelope, raw := f.do(t, http.MethodPost, "/quotes", map[string]interface{}{"title": "Hours", "customer_id": f.custID, "currency": "EUR"})
	require.Equal(t, http.StatusCreated, status, raw)
	quoteID := uint(quoteTaxNumber(t, envelope.Data, "id"))

	// 1.5 x 9.99 at 12.5 percent off, 21 percent tax.
	status, envelope, raw = f.do(t, http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), map[string]interface{}{
		"product_id": f.product.ID, "quantity": 1.5, "unit_price_cents": 999, "discount_bp": 1250, "tax_rate_bp": 2100,
	})
	require.Equal(t, http.StatusCreated, status, raw)
	lines := quoteQuantityLines(t, raw)
	require.Len(t, lines, 1)
	assert.Equal(t, "1.5", string(lines[0]["quantity"]), "the quantity is a JSON number")
	assert.Equal(t, "1311", string(lines[0]["line_total_cents"]))
	assert.Equal(t, "275", string(lines[0]["tax_cents"]))
	var firstID uint
	require.NoError(t, json.Unmarshal(lines[0]["id"], &firstID))

	// 0.25 x 3.33, untaxed.
	status, _, raw = f.do(t, http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), map[string]interface{}{
		"product_id": f.product.ID, "quantity": 0.25, "unit_price_cents": 333,
	})
	require.Equal(t, http.StatusCreated, status, raw)
	lines = quoteQuantityLines(t, raw)
	require.Len(t, lines, 2)
	assert.Equal(t, "0.25", string(lines[1]["quantity"]))
	assert.Equal(t, "83", string(lines[1]["line_total_cents"]))

	// Edit the first line to 2.
	status, envelope, raw = f.do(t, http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, firstID), map[string]interface{}{"quantity": 2})
	require.Equal(t, http.StatusOK, status, raw)
	lines = quoteQuantityLines(t, raw)
	assert.Equal(t, "2", string(lines[0]["quantity"]))
	assert.Equal(t, "1748", string(lines[0]["line_total_cents"]), "2 x 999 x 0.875 = 1748.25")
	assert.Equal(t, "367", string(lines[0]["tax_cents"]))
	assert.Equal(t, int64(1998+83), quoteTaxNumber(t, envelope.Data, "subtotal_cents"))

	// A following read returns the same numbers and the half-up totals.
	status, envelope, raw = f.do(t, http.MethodGet, fmt.Sprintf("/quotes/%d", quoteID), nil)
	require.Equal(t, http.StatusOK, status, raw)
	lines = quoteQuantityLines(t, raw)
	require.Len(t, lines, 2)
	assert.Equal(t, "2", string(lines[0]["quantity"]))
	assert.Equal(t, "0.25", string(lines[1]["quantity"]))
	assert.NotContains(t, raw, `"quantity":"`, "never a string")
	assert.Equal(t, int64(1998+83), quoteTaxNumber(t, envelope.Data, "subtotal_cents"))
	assert.Equal(t, int64(1748+83), quoteTaxNumber(t, envelope.Data, "total_cents"))
	assert.Equal(t, int64(1998+83-(1748+83)), quoteTaxNumber(t, envelope.Data, "discount_cents"))
	assert.Equal(t, int64(367), quoteTaxNumber(t, envelope.Data, "tax_cents"))
	assert.Equal(t, int64(1748+83+367), quoteTaxNumber(t, envelope.Data, "gross_total_cents"))

	var stored []int64
	require.NoError(t, f.db.Raw("SELECT quantity_milli FROM quote_lines WHERE quote_id = ? ORDER BY id", quoteID).Scan(&stored).Error)
	assert.Equal(t, []int64{2000, 250}, stored)

	// 1.0005 is refused before anything is read or written, on both routes.
	before := f.snapshot(t)
	for _, request := range []struct {
		method, path string
		body         map[string]interface{}
	}{
		{http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), map[string]interface{}{"product_id": f.product.ID, "quantity": 1.0005}},
		{http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, firstID), map[string]interface{}{"quantity": 1.0005}},
	} {
		status, envelope, raw = f.do(t, request.method, request.path, request.body)
		assert.Equal(t, http.StatusBadRequest, status, raw)
		require.NotNil(t, envelope.Error, raw)
		assert.Equal(t, "VALIDATION_ERROR", envelope.Error.Code)
		var detailed struct {
			Error struct {
				Message string            `json:"message"`
				Details map[string]string `json:"details"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &detailed))
		assert.Equal(t, "Validation failed", detailed.Error.Message)
		assert.Equal(t, models.QuoteQuantityBoundsMessage, detailed.Error.Details["quantity"])
		assert.Equal(t, before, f.snapshot(t), "%s %s leaves the lines and totals unchanged", request.method, request.path)
	}
}
