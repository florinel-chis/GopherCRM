package tests

// Quote tax (#324) over HTTP on SQLite: the quote routes behind the real
// RequestID, ErrorHandler and JWT Auth middleware and the role guards, the
// real repositories and services, and the full schema of
// models.MigrateDatabase. A sales user adds and edits taxed lines on its own
// draft; every refusal (a rate out of range, a gross total over 10^12, a write
// on a sent quote) leaves the quotes and quote_lines tables unchanged.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// quoteTaxAPIMailer accepts every message; this suite sends none.
type quoteTaxAPIMailer struct{}

func (quoteTaxAPIMailer) SendPasswordReset(string, string, string) error { return nil }

func (quoteTaxAPIMailer) Send(string, string, string) error { return nil }

type quoteTaxAPIFixture struct {
	db      *gorm.DB
	router  *gin.Engine
	token   string
	product *models.Product
	custID  uint
}

func newQuoteTaxAPIFixture(t *testing.T) *quoteTaxAPIFixture {
	t.Helper()
	previousLogger := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previousLogger })

	dsn := filepath.Join(t.TempDir(), "quote-tax-api.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	previousDB := models.DB
	models.DB = db
	err = models.MigrateDatabase()
	models.DB = previousDB
	require.NoError(t, err)

	userRepo := repository.NewUserRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	txManager := utils.NewTransactionManager(db)
	mailer := quoteTaxAPIMailer{}
	jwtConfig := config.JWTConfig{Secret: "quote-tax-api-test-secret-0123456789abcdef", ExpiryHours: 1, RefreshTokenDays: 30}
	authService := service.NewAuthServiceWithSessions(userRepo, repository.NewAPIKeyRepository(db),
		repository.NewRefreshTokenRepository(db), repository.NewPasswordResetTokenRepository(db), txManager, mailer,
		jwtConfig, "http://localhost:5173", "")
	quoteService := service.NewQuoteService(repository.NewQuoteRepository(db), repository.NewProductRepository(db),
		repository.NewDealRepository(db), customerRepo, userRepo, nil, mailer, txManager)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupQuoteRoutes(protected, handler.NewQuoteHandler(quoteService))

	sales := &models.User{Email: "sales@quote-tax-api.test", Password: "x", FirstName: "Api", LastName: "Sales", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(sales).Error)
	token, err := authService.GenerateJWT(sales)
	require.NoError(t, err)
	customer := &models.Customer{FirstName: "Api", LastName: "Customer", Email: "customer@quote-tax-api.test"}
	require.NoError(t, db.Create(customer).Error)
	product := &models.Product{Name: "Widget", SKU: "W-TAX", UnitPriceCents: 1000, Currency: "EUR", IsActive: true}
	require.NoError(t, db.Create(product).Error)
	return &quoteTaxAPIFixture{db: db, router: router, token: token, product: product, custID: customer.ID}
}

type quoteTaxError struct {
	Code string `json:"code"`
}

type quoteTaxEnvelope struct {
	Success bool                   `json:"success"`
	Data    map[string]interface{} `json:"data"`
	Error   *quoteTaxError         `json:"error"`
}

func (f *quoteTaxAPIFixture) do(t *testing.T, method, path string, body interface{}) (int, quoteTaxEnvelope, string) {
	t.Helper()
	reader := bytes.NewReader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	var envelope quoteTaxEnvelope
	raw := rec.Body.String()
	if rec.Body.Len() > 0 {
		// A list answers data as an array; only object bodies are decoded here.
		_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	}
	return rec.Code, envelope, raw
}

// snapshot renders every row of quotes and quote_lines, soft-deleted included.
func (f *quoteTaxAPIFixture) snapshot(t *testing.T) string {
	t.Helper()
	var quotes, lines []map[string]interface{}
	require.NoError(t, f.db.Table("quotes").Order("id").Find(&quotes).Error)
	require.NoError(t, f.db.Table("quote_lines").Order("id").Find(&lines).Error)
	raw, err := json.Marshal(map[string]interface{}{"quotes": quotes, "lines": lines})
	require.NoError(t, err)
	return string(raw)
}

func quoteTaxNumber(t *testing.T, m map[string]interface{}, key string) int64 {
	t.Helper()
	value, ok := m[key].(float64)
	require.Truef(t, ok, "%s is a JSON number in %v", key, m)
	return int64(value)
}

func quoteTaxFirstLine(t *testing.T, data map[string]interface{}) map[string]interface{} {
	t.Helper()
	lines, ok := data["lines"].([]interface{})
	require.True(t, ok, "lines is an array")
	require.NotEmpty(t, lines)
	return lines[0].(map[string]interface{})
}

func TestQuoteTaxAPISQLite(t *testing.T) {
	f := newQuoteTaxAPIFixture(t)

	status, envelope, raw := f.do(t, http.MethodPost, "/quotes", map[string]interface{}{"title": "Taxed", "customer_id": f.custID, "currency": "EUR"})
	require.Equal(t, http.StatusCreated, status, raw)
	quoteID := uint(quoteTaxNumber(t, envelope.Data, "id"))
	assert.Zero(t, quoteTaxNumber(t, envelope.Data, "tax_cents"))
	assert.Zero(t, quoteTaxNumber(t, envelope.Data, "gross_total_cents"))

	// Add: quantity 3, unit 9.99, 12.5 percent off, 21 percent tax.
	status, envelope, raw = f.do(t, http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), map[string]interface{}{
		"product_id": f.product.ID, "quantity": 3, "unit_price_cents": 999, "discount_bp": 1250, "tax_rate_bp": 2100,
	})
	require.Equal(t, http.StatusCreated, status, raw)
	assert.Equal(t, int64(2622), quoteTaxNumber(t, envelope.Data, "total_cents"))
	assert.Equal(t, int64(551), quoteTaxNumber(t, envelope.Data, "tax_cents"))
	assert.Equal(t, int64(3173), quoteTaxNumber(t, envelope.Data, "gross_total_cents"))
	line := quoteTaxFirstLine(t, envelope.Data)
	lineID := uint(quoteTaxNumber(t, line, "id"))
	assert.Equal(t, int64(2100), quoteTaxNumber(t, line, "tax_rate_bp"))
	assert.Equal(t, int64(551), quoteTaxNumber(t, line, "tax_cents"))
	assert.Equal(t, int64(2622), quoteTaxNumber(t, line, "line_total_cents"))

	// Edit the rate alone.
	status, envelope, raw = f.do(t, http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, lineID), map[string]interface{}{"tax_rate_bp": 1500})
	require.Equal(t, http.StatusOK, status, raw)
	assert.Equal(t, int64(393), quoteTaxNumber(t, envelope.Data, "tax_cents"))
	assert.Equal(t, int64(2622+393), quoteTaxNumber(t, envelope.Data, "gross_total_cents"))
	assert.Equal(t, int64(1500), quoteTaxNumber(t, quoteTaxFirstLine(t, envelope.Data), "tax_rate_bp"))

	// A second line without a rate defaults to 0.
	status, envelope, raw = f.do(t, http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), map[string]interface{}{"product_id": f.product.ID, "quantity": 1})
	require.Equal(t, http.StatusCreated, status, raw)
	assert.Equal(t, int64(2622+1000), quoteTaxNumber(t, envelope.Data, "total_cents"))
	assert.Equal(t, int64(393), quoteTaxNumber(t, envelope.Data, "tax_cents"))
	assert.Equal(t, int64(2622+1000+393), quoteTaxNumber(t, envelope.Data, "gross_total_cents"))
	lines := envelope.Data["lines"].([]interface{})
	require.Len(t, lines, 2)
	assert.Zero(t, quoteTaxNumber(t, lines[1].(map[string]interface{}), "tax_rate_bp"))
	assert.Zero(t, quoteTaxNumber(t, lines[1].(map[string]interface{}), "tax_cents"))

	// The reads carry the same fields.
	status, envelope, raw = f.do(t, http.MethodGet, fmt.Sprintf("/quotes/%d", quoteID), nil)
	require.Equal(t, http.StatusOK, status, raw)
	assert.Equal(t, int64(393), quoteTaxNumber(t, envelope.Data, "tax_cents"))
	assert.Equal(t, int64(4015), quoteTaxNumber(t, envelope.Data, "gross_total_cents"))
	assert.Equal(t, int64(1500), quoteTaxNumber(t, quoteTaxFirstLine(t, envelope.Data), "tax_rate_bp"))
	assert.Equal(t, int64(393), quoteTaxNumber(t, quoteTaxFirstLine(t, envelope.Data), "tax_cents"))

	status, _, raw = f.do(t, http.MethodGet, "/quotes", nil)
	require.Equal(t, http.StatusOK, status, raw)
	var list struct {
		Data []map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &list))
	require.Len(t, list.Data, 1)
	assert.Equal(t, int64(393), quoteTaxNumber(t, list.Data[0], "tax_cents"))
	assert.Equal(t, int64(4015), quoteTaxNumber(t, list.Data[0], "gross_total_cents"))
	assert.Equal(t, int64(3622), quoteTaxNumber(t, list.Data[0], "total_cents"), "total_cents stays the net total")

	// Out-of-range rates are 400 VALIDATION_ERROR and write nothing.
	before := f.snapshot(t)
	for _, tc := range []struct {
		method string
		path   string
		body   map[string]interface{}
	}{
		{http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), map[string]interface{}{"product_id": f.product.ID, "quantity": 1, "tax_rate_bp": 10001}},
		{http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), map[string]interface{}{"product_id": f.product.ID, "quantity": 1, "tax_rate_bp": -1}},
		{http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, lineID), map[string]interface{}{"tax_rate_bp": 10001}},
		{http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, lineID), map[string]interface{}{"tax_rate_bp": -1}},
	} {
		status, envelope, raw = f.do(t, tc.method, tc.path, tc.body)
		require.Equal(t, http.StatusBadRequest, status, raw)
		require.NotNil(t, envelope.Error, raw)
		assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
		assert.Equal(t, before, f.snapshot(t), "%s %s wrote nothing", tc.method, tc.path)
	}

	// A gross total over 10^12 is 400 VALIDATION_ERROR and writes nothing.
	status, envelope, raw = f.do(t, http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, lineID), map[string]interface{}{
		"quantity": 1, "unit_price_cents": 800_000_000_001, "discount_bp": 0, "tax_rate_bp": 2500,
	})
	require.Equal(t, http.StatusBadRequest, status, raw)
	require.NotNil(t, envelope.Error, raw)
	assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
	assert.Equal(t, before, f.snapshot(t), "the gross-cap refusal wrote nothing")

	// Once sent, every tax write is 409 CONFLICT and writes nothing.
	status, _, raw = f.do(t, http.MethodPost, fmt.Sprintf("/quotes/%d/status", quoteID), map[string]interface{}{"status": "sent"})
	require.Equal(t, http.StatusOK, status, raw)
	sent := f.snapshot(t)
	status, envelope, raw = f.do(t, http.MethodPost, fmt.Sprintf("/quotes/%d/lines", quoteID), map[string]interface{}{"product_id": f.product.ID, "quantity": 1, "tax_rate_bp": 2100})
	require.Equal(t, http.StatusConflict, status, raw)
	require.NotNil(t, envelope.Error, raw)
	assert.Equal(t, utils.ErrCodeConflict, envelope.Error.Code)
	status, envelope, raw = f.do(t, http.MethodPut, fmt.Sprintf("/quotes/%d/lines/%d", quoteID, lineID), map[string]interface{}{"tax_rate_bp": 2100})
	require.Equal(t, http.StatusConflict, status, raw)
	require.NotNil(t, envelope.Error, raw)
	assert.Equal(t, utils.ErrCodeConflict, envelope.Error.Code)
	assert.Equal(t, sent, f.snapshot(t), "the writes on the sent quote wrote nothing")

	// The sent quote still reads its tax fields.
	status, envelope, raw = f.do(t, http.MethodGet, fmt.Sprintf("/quotes/%d", quoteID), nil)
	require.Equal(t, http.StatusOK, status, raw)
	assert.Equal(t, "sent", envelope.Data["status"])
	assert.Equal(t, int64(4015), quoteTaxNumber(t, envelope.Data, "gross_total_cents"))
}
