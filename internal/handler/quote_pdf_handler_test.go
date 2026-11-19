package handler

// Quote PDF output at the HTTP boundary (#384): GET /quotes/:id/pdf with a
// mocked service, and a chain of the real service, the real routes and the
// real Auth middleware over SQLite with a renderer that, like the render
// page, redeems its credential through POST /quotes/public/render.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

var quotePDFHandlerBytes = []byte("%PDF-1.7\nhandler test document \x00\x01\xff\n%%EOF\n")

// doRaw sends one request and returns the recorder untouched, so two answers
// can be compared byte for byte.
func doRaw(router *gin.Engine, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

// withoutRequestID drops the one part of an envelope that differs between
// two requests by design.
func withoutRequestID(t *testing.T, body []byte) string {
	t.Helper()
	return regexp.MustCompile(`"request_id":"[^"]*"`).ReplaceAllString(string(body), `"request_id":"-"`)
}

func TestQuotePDFHandler(t *testing.T) {
	t.Run("the renderer's bytes with the PDF headers", func(t *testing.T) {
		svc := new(mocks.QuoteService)
		quote := ownedQuote(1, models.QuoteStatusSent)
		quote.ID = 42
		svc.On("GetByID", uint(42)).Return(quote, nil)
		svc.On("PDFAvailable").Return(true)
		svc.On("RenderPDF", uint(42)).Return(quotePDFHandlerBytes, nil).Once()
		router := newQuoteTestRouter(svc, 1, models.RoleAdmin)

		w := doRaw(router, http.MethodGet, "/quotes/42/pdf")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))
		assert.Equal(t, `attachment; filename="Quote-Q-000042.pdf"`, w.Header().Get("Content-Disposition"))
		assert.Regexp(t, `^attachment; filename="Quote-Q-\d{6}\.pdf"$`, w.Header().Get("Content-Disposition"))
		assert.Equal(t, quotePDFHandlerBytes, w.Body.Bytes(), "raw bytes, unchanged, no envelope")
		svc.AssertExpectations(t)

		// A sales user downloads its own quote.
		own := new(mocks.QuoteService)
		own.On("GetByID", uint(1)).Return(ownedQuote(7, models.QuoteStatusDraft), nil)
		own.On("PDFAvailable").Return(true)
		own.On("RenderPDF", uint(1)).Return(quotePDFHandlerBytes, nil).Once()
		w = doRaw(newQuoteTestRouter(own, 7, models.RoleSales), http.MethodGet, "/quotes/1/pdf")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, quotePDFHandlerBytes, w.Body.Bytes())
		own.AssertExpectations(t)
	})

	// The refusals are those of GET /quotes/:id, byte for byte, on a quote
	// that exists and with a renderer that would succeed; nothing is rendered.
	t.Run("sales non-owner is the 403 of GET /quotes/:id", func(t *testing.T) {
		svc := new(mocks.QuoteService)
		svc.On("GetByID", uint(1)).Return(ownedQuote(2, models.QuoteStatusSent), nil)
		svc.On("PDFAvailable").Return(true).Maybe()
		svc.On("RenderPDF", mock.Anything).Return(quotePDFHandlerBytes, nil).Maybe()
		router := newQuoteTestRouter(svc, 1, models.RoleSales)

		recorded := doRaw(router, http.MethodGet, "/quotes/1")
		got := doRaw(router, http.MethodGet, "/quotes/1/pdf")
		require.Equal(t, http.StatusForbidden, recorded.Code)
		assert.Equal(t, recorded.Code, got.Code)
		assert.Equal(t, withoutRequestID(t, recorded.Body.Bytes()), withoutRequestID(t, got.Body.Bytes()))
		assert.Equal(t, recorded.Header().Get("Content-Type"), got.Header().Get("Content-Type"))
		assert.Contains(t, got.Body.String(), `"code":"FORBIDDEN"`)
		assert.Contains(t, got.Body.String(), `"message":"You can only view your own quotes"`)
		assert.Empty(t, got.Header().Get("Content-Disposition"))
		t.Logf("GET /quotes/1     -> %d %s", recorded.Code, recorded.Body.String())
		t.Logf("GET /quotes/1/pdf -> %d %s", got.Code, got.Body.String())
		assertNoCalls(t, &svc.Mock, "RenderPDF", "PDFAvailable")
	})

	t.Run("unknown id is the 404 of GET /quotes/:id", func(t *testing.T) {
		svc := new(mocks.QuoteService)
		svc.On("GetByID", uint(999)).Return(nil, fmt.Errorf("quote 999 not found: %w", apperrors.ErrQuoteNotFound))
		svc.On("PDFAvailable").Return(true).Maybe()
		svc.On("RenderPDF", mock.Anything).Return(quotePDFHandlerBytes, nil).Maybe()
		router := newQuoteTestRouter(svc, 1, models.RoleAdmin)

		recorded := doRaw(router, http.MethodGet, "/quotes/999")
		got := doRaw(router, http.MethodGet, "/quotes/999/pdf")
		require.Equal(t, http.StatusNotFound, recorded.Code)
		assert.Equal(t, recorded.Code, got.Code)
		assert.Equal(t, withoutRequestID(t, recorded.Body.Bytes()), withoutRequestID(t, got.Body.Bytes()))
		assert.Contains(t, got.Body.String(), `"code":"NOT_FOUND"`)
		assert.Contains(t, got.Body.String(), `"message":"Quote not found"`)
		t.Logf("GET /quotes/999     -> %d %s", recorded.Code, recorded.Body.String())
		t.Logf("GET /quotes/999/pdf -> %d %s", got.Code, got.Body.String())
		assertNoCalls(t, &svc.Mock, "RenderPDF", "PDFAvailable")
	})

	t.Run("a malformed id is the 400 of GET /quotes/:id", func(t *testing.T) {
		svc := new(mocks.QuoteService)
		router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
		recorded := doRaw(router, http.MethodGet, "/quotes/abc")
		got := doRaw(router, http.MethodGet, "/quotes/abc/pdf")
		require.Equal(t, http.StatusBadRequest, got.Code)
		assert.Equal(t, withoutRequestID(t, recorded.Body.Bytes()), withoutRequestID(t, got.Body.Bytes()))
		assertNoCalls(t, &svc.Mock, "GetByID", "RenderPDF", "PDFAvailable")
	})

	t.Run("support and customer are stopped by the guard", func(t *testing.T) {
		for _, role := range []models.UserRole{models.RoleSupport, models.RoleCustomer} {
			svc := new(mocks.QuoteService)
			status, envelope := doJSON(t, newQuoteTestRouter(svc, 1, role), http.MethodGet, "/quotes/1/pdf", nil)
			assert.Equal(t, http.StatusForbidden, status, role)
			require.NotNil(t, envelope.Error)
			assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
			assertNoCalls(t, &svc.Mock, "GetByID", "RenderPDF", "PDFAvailable")
		}
	})

	t.Run("off is 503 PDF_UNAVAILABLE", func(t *testing.T) {
		svc := new(mocks.QuoteService)
		svc.On("GetByID", uint(1)).Return(ownedQuote(1, models.QuoteStatusSent), nil)
		svc.On("PDFAvailable").Return(false)
		status, envelope := doJSON(t, newQuoteTestRouter(svc, 1, models.RoleAdmin), http.MethodGet, "/quotes/1/pdf", nil)
		assert.Equal(t, http.StatusServiceUnavailable, status)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, "PDF_UNAVAILABLE", envelope.Error.Code)
		assert.Equal(t, "PDF output is switched off", envelope.Error.Message)
		assertNoCalls(t, &svc.Mock, "RenderPDF")

		// The service's own sentinel maps to the same answer.
		late := new(mocks.QuoteService)
		late.On("GetByID", uint(1)).Return(ownedQuote(1, models.QuoteStatusSent), nil)
		late.On("PDFAvailable").Return(true)
		late.On("RenderPDF", uint(1)).Return(nil, fmt.Errorf("quote 1: %w", apperrors.ErrQuotePDFUnavailable))
		status, envelope = doJSON(t, newQuoteTestRouter(late, 1, models.RoleAdmin), http.MethodGet, "/quotes/1/pdf", nil)
		assert.Equal(t, http.StatusServiceUnavailable, status)
		assert.Equal(t, "PDF_UNAVAILABLE", envelope.Error.Code)
	})

	t.Run("a renderer error is 503 PDF_RENDER_FAILED without its cause", func(t *testing.T) {
		svc := new(mocks.QuoteService)
		svc.On("GetByID", uint(1)).Return(ownedQuote(1, models.QuoteStatusSent), nil)
		svc.On("PDFAvailable").Return(true)
		svc.On("RenderPDF", uint(1)).Return(nil, fmt.Errorf("quote 1 canary-cause: %w", apperrors.ErrQuotePDFRenderFailed))
		router := newQuoteTestRouter(svc, 1, models.RoleAdmin)
		w := doRaw(router, http.MethodGet, "/quotes/1/pdf")
		assert.Equal(t, http.StatusServiceUnavailable, w.Code, "503, not 502: the body must survive a proxy")
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
		assert.Empty(t, w.Header().Get("Content-Disposition"))
		var envelope utils.APIResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
		require.NotNil(t, envelope.Error)
		assert.Equal(t, "PDF_RENDER_FAILED", envelope.Error.Code)
		assert.Equal(t, "The PDF could not be produced", envelope.Error.Message)
		assert.NotContains(t, w.Body.String(), "canary-cause")
	})
}

// ---------------------------------------------------------------------------
// The real chain.

const quotePDFChainBaseURL = "https://crm.example.test"

// chainQuotePDFRenderer stands in for the browser only: what it does with
// the page URL is what the render page does.
type chainQuotePDFRenderer struct {
	during func(token string)
	urls   []string
}

func (r *chainQuotePDFRenderer) Render(_ context.Context, pageURL string) ([]byte, error) {
	r.urls = append(r.urls, pageURL)
	u, err := url.Parse(pageURL)
	if err != nil {
		return nil, errors.New("unparsable page URL")
	}
	if r.during != nil {
		r.during(u.Fragment)
	}
	return quotePDFHandlerBytes, nil
}

// quotePDFChain is the real quote service with PDF output on, the public and
// the protected quote routes mounted as cmd/main.go mounts them, and the real
// Auth middleware in front of the protected ones.
type quotePDFChain struct {
	db       *gorm.DB
	svc      service.QuoteService
	router   *gin.Engine
	renderer *chainQuotePDFRenderer
	quoteA   *models.Quote
	quoteB   *models.Quote
}

func newQuotePDFChain(t *testing.T) *quotePDFChain {
	t.Helper()
	ensureHandlerTestLogger(t)
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard, DisableForeignKeyConstraintWhenMigrating: true})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.Company{}, &models.Lead{}, &models.Customer{}, &models.Deal{},
		&models.Product{}, &models.Quote{}, &models.QuoteLine{}, &models.QuoteStatusChange{},
		&models.QuoteResponseToken{}, &models.APIKey{},
	))

	owner := &models.User{Email: "owner@quote-pdf.test", Password: "x", FirstName: "Own", LastName: "Er", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(owner).Error)
	customer := &models.Customer{FirstName: "Cus", LastName: "Tomer", Email: "customer@quote-pdf.test"}
	require.NoError(t, db.Create(customer).Error)
	product := &models.Product{Name: "Widget", UnitPriceCents: 1000, Currency: "EUR", IsActive: true}
	require.NoError(t, db.Create(product).Error)

	chain := &quotePDFChain{db: db, renderer: &chainQuotePDFRenderer{}}
	newQuote := func(title, line string) *models.Quote {
		quote := &models.Quote{Title: title, Status: models.QuoteStatusSent, Currency: "EUR", OwnerID: owner.ID, CustomerID: &customer.ID,
			SubtotalCents: 1000, TotalCents: 1000, GrossTotalCents: 1000}
		require.NoError(t, db.Create(quote).Error)
		require.NoError(t, db.Create(&models.QuoteLine{QuoteID: quote.ID, ProductID: product.ID, Position: 1, Description: line,
			Quantity: models.QuoteQuantityFromUnits(1), UnitPriceCents: 1000, LineTotalCents: 1000}).Error)
		return quote
	}
	chain.quoteA = newQuote("Quote A", "Line of quote A")
	chain.quoteB = newQuote("Quote B", "Line of quote B")

	users := repository.NewUserRepository(db)
	chain.svc = service.WithQuotePDF(service.NewQuoteServiceWithResponseLinks(
		repository.NewQuoteRepository(db), repository.NewProductRepository(db), repository.NewDealRepository(db),
		repository.NewCustomerRepository(db), users, nil, nil, utils.NewTransactionManager(db),
		repository.NewQuoteResponseTokenRepository(db), quotePDFChainBaseURL, "quote-pdf-chain-secret-0123456789abcdef"),
		chain.renderer)
	require.True(t, chain.svc.PDFAvailable())

	auth := service.NewAuthService(users, repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: "quote-pdf-chain-jwt-secret-0123456789abcdef", ExpiryHours: 1})
	chain.router = gin.New()
	chain.router.Use(middleware.RequestID(), middleware.ErrorHandler())
	api := chain.router.Group("/api/v1")
	SetupQuotePublicRoutes(api.Group(""), NewQuotePublicHandler(chain.svc))
	SetupQuoteRoutes(api.Group("", middleware.Auth(auth)), NewQuoteHandler(chain.svc))
	return chain
}

// post sends a JSON body to the public render route.
func (c *quotePDFChain) post(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/quotes/public/render", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c.router.ServeHTTP(w, req)
	return w
}

// bearer sends a request with the given value as a Bearer token.
func (c *quotePDFChain) bearer(method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	c.router.ServeHTTP(w, req)
	return w
}

// quoteRenderEnvelope is the answer of POST /quotes/public/render.
type quoteRenderEnvelope struct {
	Success bool `json:"success"`
	Data    struct {
		Quote struct {
			ID     uint   `json:"id"`
			Number string `json:"number"`
			Title  string `json:"title"`
			Lines  []struct {
				Description string `json:"description"`
			} `json:"lines"`
		} `json:"quote"`
		ProductName *string `json:"product_name"`
	} `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeQuoteRender(t *testing.T, w *httptest.ResponseRecorder) quoteRenderEnvelope {
	t.Helper()
	var envelope quoteRenderEnvelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), w.Body.String())
	return envelope
}
