package tests

// The business-text scrub of an erasure (#245), over the real routes behind the
// real JWT middleware, services and repositories on SQLite. DELETE
// /customers/{id} keeps its 204; afterwards the customer's deal and its
// accepted quote are still readable with their stage, amount, status and
// totals, and their staff-typed text is redacted. Another customer's deal and
// quote are unchanged.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

const (
	businessTextSecret   = "business-text-erasure-api-secret-32-bytes"
	businessTextPassword = "password123"
)

type businessTextDealJSON struct {
	Title       string           `json:"title"`
	Notes       string           `json:"notes"`
	LostReason  string           `json:"lost_reason"`
	Stage       models.DealStage `json:"stage"`
	AmountCents int64            `json:"amount_cents"`
}

type businessTextQuoteJSON struct {
	Title         string             `json:"title"`
	Notes         string             `json:"notes"`
	Status        models.QuoteStatus `json:"status"`
	SubtotalCents int64              `json:"subtotal_cents"`
	DiscountCents int64              `json:"discount_cents"`
	TotalCents    int64              `json:"total_cents"`
	Lines         []struct {
		Description    string `json:"description"`
		LineTotalCents int64  `json:"line_total_cents"`
	} `json:"lines"`
}

func TestBusinessTextErasureAPISQLite(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", businessTextSecret)

	path := filepath.Join(t.TempDir(), "business_text.db")
	db, err := gorm.Open(sqlite.Open("file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	previous := models.DB
	models.DB = db
	err = models.MigrateDatabase()
	models.DB = previous
	require.NoError(t, err)

	userRepo := repository.NewUserRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	dealRepo := repository.NewDealRepository(db)
	txManager := utils.NewTransactionManager(db)
	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: businessTextSecret, ExpiryHours: 1})
	userService := service.NewUserService(userRepo)
	customerService := service.NewCustomerService(customerRepo, userRepo, repository.NewCompanyRepository(db))
	dealService := service.NewDealService(dealRepo, repository.NewCompanyRepository(db), customerRepo,
		repository.NewLeadRepository(db), userRepo, nil, txManager)
	quoteService := service.NewQuoteService(repository.NewQuoteRepository(db), repository.NewProductRepository(db), dealRepo,
		customerRepo, userRepo, nil, nil, txManager)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupCustomerRoutes(protected, handler.NewCustomerHandler(customerService))
	handler.SetupDealRoutes(protected, handler.NewDealHandler(dealService))
	handler.SetupQuoteRoutes(protected, handler.NewQuoteHandler(quoteService))

	admin := &models.User{Email: "admin@business-text.test", FirstName: "Staff", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, userService.Register(admin, businessTextPassword))
	bearer, err := authService.Login(admin.Email, businessTextPassword)
	require.NoError(t, err)
	adminAuth := "Bearer " + bearer

	call := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(""))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", adminAuth)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	getDeal := func(id uint) businessTextDealJSON {
		t.Helper()
		rec := call(http.MethodGet, fmt.Sprintf("/deals/%d", id))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var envelope struct {
			Data businessTextDealJSON `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		return envelope.Data
	}
	getQuote := func(id uint) businessTextQuoteJSON {
		t.Helper()
		rec := call(http.MethodGet, fmt.Sprintf("/quotes/%d", id))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var envelope struct {
			Data businessTextQuoteJSON `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		return envelope.Data
	}

	product := &models.Product{Name: "Onboarding workshop", SKU: "WS-1", UnitPriceCents: 80000, Currency: "EUR", IsActive: true}
	require.NoError(t, db.Create(product).Error)

	seed := func(first, last, email string) (*models.Customer, *models.Deal, *models.Quote) {
		t.Helper()
		customer := &models.Customer{FirstName: first, LastName: last, Email: email, Phone: "+40 700 111 222"}
		require.NoError(t, db.Create(customer).Error)
		deal := &models.Deal{Title: "Workshop for " + first + " " + last, Stage: models.DealStageNegotiation, Probability: 70,
			AmountCents: 160000, Currency: "EUR", OwnerID: admin.ID, CustomerID: &customer.ID,
			Notes: "Call " + first + " on +40 700 111 222", LostReason: "Write to " + email}
		require.NoError(t, db.Create(deal).Error)
		quote := &models.Quote{Title: "Workshop offer for " + last, Notes: "Dear " + first, Currency: "EUR", DealID: &deal.ID, OwnerID: admin.ID}
		require.NoError(t, quoteService.Create(quote, admin.ID, models.RoleAdmin))
		description := "Workshop for " + first + " " + last
		_, err := quoteService.AddLine(quote.ID, models.QuoteLineInput{ProductID: product.ID, Quantity: 2, DiscountBP: 1000, Description: &description})
		require.NoError(t, err)
		_, err = quoteService.ChangeStatus(quote.ID, models.QuoteStatusSent, admin.ID)
		require.NoError(t, err)
		_, err = quoteService.ChangeStatus(quote.ID, models.QuoteStatusAccepted, admin.ID)
		require.NoError(t, err)
		return customer, deal, quote
	}
	customer, deal, quote := seed("Jane", "Doe", "jane.doe@business-text.test")
	_, otherDeal, otherQuote := seed("John", "Roe", "john.roe@business-text.test")

	dealBefore := getDeal(deal.ID)
	quoteBefore := getQuote(quote.ID)
	require.Equal(t, "Workshop for Jane Doe", dealBefore.Title)
	require.NotEmpty(t, dealBefore.Notes)
	require.NotEmpty(t, dealBefore.LostReason)
	require.Equal(t, models.QuoteStatusAccepted, quoteBefore.Status)
	require.Len(t, quoteBefore.Lines, 1)
	require.Equal(t, "Workshop for Jane Doe", quoteBefore.Lines[0].Description)
	otherDealBefore := getDeal(otherDeal.ID)
	otherQuoteBefore := getQuote(otherQuote.ID)

	rec := call(http.MethodDelete, fmt.Sprintf("/customers/%d", customer.ID))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String(), "the erasure keeps its empty 204")

	dealAfter := getDeal(deal.ID)
	assert.Equal(t, "[redacted]", dealAfter.Title)
	assert.Empty(t, dealAfter.Notes)
	assert.Empty(t, dealAfter.LostReason)
	assert.Equal(t, dealBefore.Stage, dealAfter.Stage)
	assert.Equal(t, dealBefore.AmountCents, dealAfter.AmountCents)

	quoteAfter := getQuote(quote.ID)
	assert.Equal(t, "[redacted]", quoteAfter.Title)
	assert.Empty(t, quoteAfter.Notes)
	assert.Equal(t, models.QuoteStatusAccepted, quoteAfter.Status)
	assert.Equal(t, quoteBefore.SubtotalCents, quoteAfter.SubtotalCents)
	assert.Equal(t, quoteBefore.DiscountCents, quoteAfter.DiscountCents)
	assert.Equal(t, quoteBefore.TotalCents, quoteAfter.TotalCents)
	require.Len(t, quoteAfter.Lines, 1)
	assert.Equal(t, product.Name, quoteAfter.Lines[0].Description)
	assert.Equal(t, quoteBefore.Lines[0].LineTotalCents, quoteAfter.Lines[0].LineTotalCents)

	assert.Equal(t, otherDealBefore, getDeal(otherDeal.ID), "another customer's deal is unchanged")
	assert.Equal(t, otherQuoteBefore, getQuote(otherQuote.ID), "another customer's quote is unchanged")
}
