package handler

// The client response link of a quote email (#382), at the HTTP boundary:
// status, code and message per case with the service mocked, the body keys
// of the public view, the validation that runs before any token lookup, and
// logs that never carry the token.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

const quotePublicTestToken = "tok-CANARY-0123456789abcdefghijklmnopqrstuvwxyzABCDE"

// ensureHandlerTestLogger gives the handlers a logger when a root runs alone.
func ensureHandlerTestLogger(t *testing.T) {
	t.Helper()
	if utils.Logger == nil {
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	}
}

// captureQuotePublicLogs swaps the process logger for one that writes every
// level into a buffer.
func captureQuotePublicLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	old := utils.Logger
	logger := logrus.New()
	var output bytes.Buffer
	logger.SetOutput(&output)
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.TraceLevel)
	utils.Logger = logger
	t.Cleanup(func() { utils.Logger = old })
	return &output
}

// newQuotePublicRouter mounts the two routes the way cmd/main.go does, behind
// the request id and access log middleware, with no authentication.
func newQuotePublicRouter(svc *mocks.QuoteService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID(), middleware.Logger())
	SetupQuotePublicRoutes(router.Group("/api/v1"), NewQuotePublicHandler(svc))
	return router
}

func quotePublicTestView(status models.QuoteStatus) *models.QuotePublicView {
	validUntil := "2026-12-31"
	return &models.QuotePublicView{
		Number: "Q-000042", Status: status, Currency: "EUR", ValidUntil: &validUntil,
		Lines: []models.QuotePublicLine{{
			Description: "Widget", Quantity: models.QuoteQuantityFromUnits(2), UnitPriceCents: 1000,
			DiscountBP: 1250, LineTotalCents: 1750, TaxRateBP: 2100, TaxCents: 368,
		}},
		SubtotalCents: 2000, DiscountCents: 250, TotalCents: 1750, TaxCents: 368, GrossTotalCents: 2118,
	}
}

type quotePublicEnvelope struct {
	Success bool                       `json:"success"`
	Data    map[string]json.RawMessage `json:"data"`
	Error   *struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

func doQuotePublic(t *testing.T, router *gin.Engine, method, target, body string) (*httptest.ResponseRecorder, quotePublicEnvelope) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var envelope quotePublicEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), w.Body.String())
	return w, envelope
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestQuotePublicView(t *testing.T) {
	ensureHandlerTestLogger(t)
	output := captureQuotePublicLogs(t)

	t.Run("a usable token shows exactly the public view", func(t *testing.T) {
		svc := new(mocks.QuoteService)
		svc.On("GetPublicResponse", quotePublicTestToken).Return(quotePublicTestView(models.QuoteStatusSent), nil).Once()
		w, envelope := doQuotePublic(t, newQuotePublicRouter(svc), http.MethodGet,
			"/api/v1/quotes/public/response?token="+quotePublicTestToken, "")

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.True(t, envelope.Success)
		assert.Equal(t, []string{"currency", "discount_cents", "gross_total_cents", "lines", "number", "status",
			"subtotal_cents", "tax_cents", "total_cents", "valid_until"}, sortedKeys(envelope.Data),
			"number, status, currency, valid_until, lines and the five totals, nothing else")
		var lines []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(envelope.Data["lines"], &lines))
		require.Len(t, lines, 1)
		assert.Equal(t, []string{"description", "discount_bp", "line_total_cents", "quantity", "tax_cents", "tax_rate_bp", "unit_price_cents"},
			sortedKeys(lines[0]), "a line carries no id and no product reference")
		assert.JSONEq(t, `"Q-000042"`, string(envelope.Data["number"]))
		assert.JSONEq(t, `"sent"`, string(envelope.Data["status"]))
		assert.JSONEq(t, `"2026-12-31"`, string(envelope.Data["valid_until"]))
		assert.JSONEq(t, `2`, string(lines[0]["quantity"]))
		svc.AssertExpectations(t)
	})

	t.Run("valid_until is omitted when the quote has none", func(t *testing.T) {
		svc := new(mocks.QuoteService)
		view := quotePublicTestView(models.QuoteStatusSent)
		view.ValidUntil, view.Lines = nil, []models.QuotePublicLine{}
		svc.On("GetPublicResponse", quotePublicTestToken).Return(view, nil).Once()
		w, envelope := doQuotePublic(t, newQuotePublicRouter(svc), http.MethodGet,
			"/api/v1/quotes/public/response?token="+quotePublicTestToken, "")
		require.Equal(t, http.StatusOK, w.Code)
		assert.NotContains(t, envelope.Data, "valid_until")
		assert.JSONEq(t, `[]`, string(envelope.Data["lines"]), "an empty array, never null")
	})

	t.Run("every refusal is the same generic 400", func(t *testing.T) {
		for name, target := range map[string]string{
			"unknown, used or expired token": "/api/v1/quotes/public/response?token=" + quotePublicTestToken,
			"empty token":                    "/api/v1/quotes/public/response?token=",
			"missing token":                  "/api/v1/quotes/public/response",
		} {
			svc := new(mocks.QuoteService)
			token := ""
			if strings.HasSuffix(target, quotePublicTestToken) {
				token = quotePublicTestToken
			}
			svc.On("GetPublicResponse", token).
				Return(nil, fmt.Errorf("no usable token: %w", apperrors.ErrQuoteResponseRejected)).Once()
			w, envelope := doQuotePublic(t, newQuotePublicRouter(svc), http.MethodGet, target, "")
			require.Equal(t, http.StatusBadRequest, w.Code, name)
			require.NotNil(t, envelope.Error, name)
			assert.Equal(t, "BAD_REQUEST", envelope.Error.Code, name)
			assert.Equal(t, "Invalid or expired quote link", envelope.Error.Message, name)
			assert.False(t, envelope.Success, name)
			svc.AssertExpectations(t)
		}
	})

	t.Run("an unexpected failure is a 500 without its cause", func(t *testing.T) {
		svc := new(mocks.QuoteService)
		svc.On("GetPublicResponse", quotePublicTestToken).Return(nil, errors.New("database is gone")).Once()
		w, envelope := doQuotePublic(t, newQuotePublicRouter(svc), http.MethodGet,
			"/api/v1/quotes/public/response?token="+quotePublicTestToken, "")
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, "INTERNAL_ERROR", envelope.Error.Code)
		assert.NotContains(t, w.Body.String(), "database is gone")
	})

	require.NotEmpty(t, output.String(), "the requests were logged")
	assert.NotContains(t, output.String(), quotePublicTestToken, "no log line carries the token of the query string")
}

func TestQuotePublicRespond(t *testing.T) {
	ensureHandlerTestLogger(t)
	output := captureQuotePublicLogs(t)
	body := func(token, decision string) string {
		return fmt.Sprintf(`{"token":%q,"decision":%q}`, token, decision)
	}

	t.Run("accept and decline answer the view with the new status", func(t *testing.T) {
		for decision, status := range map[string]models.QuoteStatus{
			"accept":  models.QuoteStatusAccepted,
			"decline": models.QuoteStatusRejected,
		} {
			svc := new(mocks.QuoteService)
			svc.On("RespondPublic", quotePublicTestToken, decision).Return(quotePublicTestView(status), nil).Once()
			w, envelope := doQuotePublic(t, newQuotePublicRouter(svc), http.MethodPost,
				"/api/v1/quotes/public/response", body(quotePublicTestToken, decision))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.JSONEq(t, fmt.Sprintf("%q", status), string(envelope.Data["status"]))
			assert.Len(t, envelope.Data, 10, "the same ten keys as the view")
			svc.AssertExpectations(t)
		}
	})

	t.Run("an invalid body is a validation error decided before any token lookup", func(t *testing.T) {
		for name, raw := range map[string]string{
			"decision maybe with a valid token": body(quotePublicTestToken, "maybe"),
			"decision missing":                  fmt.Sprintf(`{"token":%q}`, quotePublicTestToken),
			"decision in another case":          body(quotePublicTestToken, "Accept"),
			"token missing":                     `{"decision":"accept"}`,
			"token empty":                       body("", "accept"),
			"malformed JSON":                    `{"token":`,
			"not an object":                     `"accept"`,
			"empty body":                        ``,
		} {
			// No expectation: any call to the service fails the test.
			svc := new(mocks.QuoteService)
			w, envelope := doQuotePublic(t, newQuotePublicRouter(svc), http.MethodPost, "/api/v1/quotes/public/response", raw)
			require.Equal(t, http.StatusBadRequest, w.Code, name)
			require.NotNil(t, envelope.Error, name)
			assert.Equal(t, "VALIDATION_ERROR", envelope.Error.Code, name)
			svc.AssertNotCalled(t, "RespondPublic")
			svc.AssertNotCalled(t, "GetPublicResponse")
		}
	})

	t.Run("a spent, unknown or expired token is the generic 400", func(t *testing.T) {
		svc := new(mocks.QuoteService)
		svc.On("RespondPublic", quotePublicTestToken, "accept").
			Return(nil, fmt.Errorf("the token of quote 9 is no longer usable: %w", apperrors.ErrQuoteResponseRejected)).Once()
		w, envelope := doQuotePublic(t, newQuotePublicRouter(svc), http.MethodPost,
			"/api/v1/quotes/public/response", body(quotePublicTestToken, "accept"))
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, "BAD_REQUEST", envelope.Error.Code)
		assert.Equal(t, "Invalid or expired quote link", envelope.Error.Message)
		assert.NotContains(t, w.Body.String(), "quote 9", "the cause is not disclosed")
	})

	t.Run("an unexpected failure is a 500 without its cause", func(t *testing.T) {
		svc := new(mocks.QuoteService)
		svc.On("RespondPublic", quotePublicTestToken, "decline").Return(nil, errors.New("commit failed")).Once()
		w, envelope := doQuotePublic(t, newQuotePublicRouter(svc), http.MethodPost,
			"/api/v1/quotes/public/response", body(quotePublicTestToken, "decline"))
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, "INTERNAL_ERROR", envelope.Error.Code)
		assert.NotContains(t, w.Body.String(), "commit failed")
	})

	require.NotEmpty(t, output.String(), "the requests were logged")
	assert.NotContains(t, output.String(), quotePublicTestToken, "no log line carries the token of the body")
}

// POST /quotes/public/render (#384) with the real service: the credential of
// a running render returns that render's quote and nothing the body names;
// afterwards, and for every other token, it is one generic 400.
func TestQuotePublicRender(t *testing.T) {
	chain := newQuotePDFChain(t)
	numberA := fmt.Sprintf("Q-%06d", chain.quoteA.ID)

	var credential string
	t.Run("the credential of a running render returns its quote and the product name", func(t *testing.T) {
		var during *httptest.ResponseRecorder
		chain.renderer.during = func(token string) {
			credential = token
			during = chain.post(fmt.Sprintf(`{"token":%q}`, token))
		}
		pdf, err := chain.svc.RenderPDF(chain.quoteA.ID)
		require.NoError(t, err)
		assert.Equal(t, quotePDFHandlerBytes, pdf)
		require.NotEmpty(t, credential)

		require.NotNil(t, during)
		require.Equal(t, http.StatusOK, during.Code, during.Body.String())
		envelope := decodeQuoteRender(t, during)
		assert.True(t, envelope.Success)
		assert.Equal(t, chain.quoteA.ID, envelope.Data.Quote.ID)
		assert.Equal(t, numberA, envelope.Data.Quote.Number)
		require.Len(t, envelope.Data.Quote.Lines, 1)
		assert.Equal(t, "Line of quote A", envelope.Data.Quote.Lines[0].Description)
		require.NotNil(t, envelope.Data.ProductName, "product_name is always present")
		assert.Empty(t, *envelope.Data.ProductName, "empty without a stored company name")

		// The same JSON as GET /quotes/:id: the keys of the quote object.
		var raw struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(during.Body.Bytes(), &raw))
		assert.Equal(t, []string{"product_name", "quote"}, sortedKeys(raw.Data))
		stored, err := chain.svc.GetByID(chain.quoteA.ID)
		require.NoError(t, err)
		want, err := json.Marshal(stored)
		require.NoError(t, err)
		assert.JSONEq(t, string(want), string(raw.Data["quote"]))
	})

	t.Run("id and quote_id of another quote in the body still answer quote A", func(t *testing.T) {
		var during *httptest.ResponseRecorder
		chain.renderer.during = func(token string) {
			during = chain.post(fmt.Sprintf(`{"token":%q,"id":%d,"quote_id":%d,"number":"Q-%06d"}`,
				token, chain.quoteB.ID, chain.quoteB.ID, chain.quoteB.ID))
		}
		_, err := chain.svc.RenderPDF(chain.quoteA.ID)
		require.NoError(t, err)
		require.NotNil(t, during)
		require.Equal(t, http.StatusOK, during.Code, during.Body.String())
		envelope := decodeQuoteRender(t, during)
		assert.Equal(t, chain.quoteA.ID, envelope.Data.Quote.ID)
		assert.Equal(t, numberA, envelope.Data.Quote.Number)
		assert.Equal(t, "Line of quote A", envelope.Data.Quote.Lines[0].Description)
		assert.NotContains(t, during.Body.String(), "Quote B")
		assert.NotContains(t, during.Body.String(), "Line of quote B")
	})

	t.Run("after the render finished, and for an unknown token, it is the generic 400", func(t *testing.T) {
		for name, token := range map[string]string{"finished": credential, "unknown": "no-such-credential"} {
			w := chain.post(fmt.Sprintf(`{"token":%q}`, token))
			assert.Equal(t, http.StatusBadRequest, w.Code, name)
			envelope := decodeQuoteRender(t, w)
			require.NotNil(t, envelope.Error, name)
			assert.Equal(t, utils.ErrCodeBadRequest, envelope.Error.Code, name)
			assert.Equal(t, "Invalid or expired render link", envelope.Error.Message, name)
			assert.NotContains(t, w.Body.String(), token, name)
		}
	})

	t.Run("an empty token or a malformed body is a validation error", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"token":""}`, `{"token":123}`, `not json`, ``, `{"id":1}`} {
			w := chain.post(body)
			assert.Equal(t, http.StatusBadRequest, w.Code, body)
			envelope := decodeQuoteRender(t, w)
			require.NotNil(t, envelope.Error, body)
			assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code, body)
		}
	})

	t.Run("the route does not write and does not log the credential", func(t *testing.T) {
		output := captureQuotePublicLogs(t)
		before := chain.quoteA.UpdatedAt
		var token string
		chain.renderer.during = func(tok string) {
			token = tok
			require.Equal(t, http.StatusOK, chain.post(fmt.Sprintf(`{"token":%q}`, tok)).Code)
		}
		_, err := chain.svc.RenderPDF(chain.quoteA.ID)
		require.NoError(t, err)
		var stored models.Quote
		require.NoError(t, chain.db.First(&stored, chain.quoteA.ID).Error)
		assert.True(t, stored.UpdatedAt.Equal(before), "the quote row is untouched")
		assert.Equal(t, models.QuoteStatusSent, stored.Status)
		require.NotEmpty(t, token)
		assert.NotContains(t, output.String(), token)
		assert.NotContains(t, output.String(), "quote-render")
	})

	t.Run("the handler maps the service's rejection and hides an unexpected failure", func(t *testing.T) {
		ensureHandlerTestLogger(t)
		svc := new(mocks.QuoteService)
		svc.On("GetRenderView", "expired").Return(nil, fmt.Errorf("the credential of quote 7 has expired: %w", apperrors.ErrQuoteRenderRejected)).Once()
		svc.On("GetRenderView", "boom").Return(nil, errors.New("database canary exploded")).Once()
		router := newQuotePublicRouter(svc)

		w, envelope := doQuotePublic(t, router, http.MethodPost, "/api/v1/quotes/public/render", `{"token":"expired"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, "Invalid or expired render link", envelope.Error.Message)
		assert.NotContains(t, w.Body.String(), "quote 7")

		w, envelope = doQuotePublic(t, router, http.MethodPost, "/api/v1/quotes/public/render", `{"token":"boom"}`)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		require.NotNil(t, envelope.Error)
		assert.NotContains(t, w.Body.String(), "canary")
		svc.AssertExpectations(t)
	})
}
