package tests

// Mail product name (#292) over HTTP on SQLite: the public password reset
// route and the quote and ticket routes behind the real RequestID,
// ErrorHandler and JWT Auth middleware, the real repositories and services
// (the auth and ticket services built with their product-name options, the
// quote service with the real ConfigurationService), a recording mailer and
// the full schema of models.MigrateDatabase seeded by InitializeDefaults.
// Every configurations row is read before and after the sends: the mails
// never write, seed or migrate configuration.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

type productNameAPIReset struct{ To, ResetURL, ProductName string }

type productNameAPIMessage struct{ To, Subject, Body string }

// productNameAPIMailer records every reset and every generic message.
type productNameAPIMailer struct {
	mu     sync.Mutex
	resets []productNameAPIReset
	sent   []productNameAPIMessage
}

func (m *productNameAPIMailer) SendPasswordReset(to, resetURL, productName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resets = append(m.resets, productNameAPIReset{To: to, ResetURL: resetURL, ProductName: productName})
	return nil
}

func (m *productNameAPIMailer) Send(to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, productNameAPIMessage{To: to, Subject: subject, Body: body})
	return nil
}

func (m *productNameAPIMailer) snapshot() ([]productNameAPIReset, []productNameAPIMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]productNameAPIReset(nil), m.resets...), append([]productNameAPIMessage(nil), m.sent...)
}

// productNameConfigRow is what the test compares of one configurations row.
type productNameConfigRow struct {
	Value        string
	DefaultValue string
	UpdatedAt    string
	Deleted      bool
}

type productNameAPIFixture struct {
	db       *gorm.DB
	router   *gin.Engine
	mailer   *productNameAPIMailer
	token    string
	admin    *models.User
	customer *models.Customer
	ticket   *models.Ticket
	quote    *models.Quote
}

func newProductNameAPIFixture(t *testing.T) *productNameAPIFixture {
	t.Helper()
	previousLogger := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previousLogger })

	dsn := filepath.Join(t.TempDir(), "mail-product-name-api.db") +
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
	// As every boot does: the shipped entries, general.company_name included.
	require.NoError(t, repository.NewConfigurationRepository(db).InitializeDefaults())

	f := &productNameAPIFixture{db: db, mailer: &productNameAPIMailer{}}
	userRepo := repository.NewUserRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	txManager := utils.NewTransactionManager(db)
	configService := service.NewConfigurationService(repository.NewConfigurationRepository(db), nil)
	jwtConfig := config.JWTConfig{Secret: "mail-product-name-api-test-secret-0123456789", ExpiryHours: 1, RefreshTokenDays: 30}

	authService := service.NewAuthServiceWithSessions(userRepo, repository.NewAPIKeyRepository(db),
		repository.NewRefreshTokenRepository(db), repository.NewPasswordResetTokenRepository(db), txManager, f.mailer,
		jwtConfig, "http://localhost:5173", "", service.WithAuthProductName(configService))
	quoteService := service.NewQuoteService(repository.NewQuoteRepository(db), repository.NewProductRepository(db),
		repository.NewDealRepository(db), customerRepo, userRepo, configService, f.mailer, txManager)
	ticketService := service.NewTicketService(repository.NewTicketRepository(db), customerRepo, userRepo,
		service.WithTicketCommentMail(repository.NewTicketMailDeliveryRepository(db), txManager, f.mailer),
		service.WithTicketProductName(configService))
	customerService := service.NewCustomerService(customerRepo, userRepo, repository.NewCompanyRepository(db))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	api := router.Group("/api/v1")
	authHandler := handler.NewAuthHandler(authService, service.NewUserService(userRepo))
	api.POST("/auth/password-reset", authHandler.RequestPasswordReset)
	protected := api.Group("")
	protected.Use(middleware.Auth(authService))
	handler.SetupQuoteRoutes(protected, handler.NewQuoteHandler(quoteService))
	handler.SetupTicketRoutes(protected, handler.NewTicketHandler(ticketService, customerService))
	f.router = router

	f.admin = &models.User{Email: "admin@mail-product-name-api.test", Password: "x", FirstName: "Api", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(f.admin).Error)
	f.token, err = authService.GenerateJWT(f.admin)
	require.NoError(t, err)
	f.customer = &models.Customer{FirstName: "Api", LastName: "Customer", Email: "customer@mail-product-name-api.test"}
	require.NoError(t, db.Create(f.customer).Error)
	f.ticket = &models.Ticket{Title: "API ticket", Description: "d", Status: models.TicketStatusOpen,
		Priority: models.TicketPriorityMedium, CustomerID: f.customer.ID, AssignedToID: &f.admin.ID}
	require.NoError(t, db.Create(f.ticket).Error)
	sentAt := time.Now().UTC()
	f.quote = &models.Quote{Title: "API quote", Status: models.QuoteStatusSent, Currency: "EUR",
		CustomerID: &f.customer.ID, OwnerID: f.admin.ID, SentAt: &sentAt}
	require.NoError(t, db.Create(f.quote).Error)
	return f
}

func (f *productNameAPIFixture) post(t *testing.T, path string, body interface{}, bearer bool) (int, string) {
	t.Helper()
	reader := bytes.NewReader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1"+path, reader)
	req.Header.Set("Content-Type", "application/json")
	if bearer {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// configurationRows reads every configurations row, soft-deleted ones included.
func (f *productNameAPIFixture) configurationRows(t *testing.T) map[string]productNameConfigRow {
	t.Helper()
	var rows []models.Configuration
	require.NoError(t, f.db.Unscoped().Order("config_key").Find(&rows).Error)
	out := make(map[string]productNameConfigRow, len(rows))
	for _, row := range rows {
		out[row.Key] = productNameConfigRow{
			Value:        row.Value,
			DefaultValue: row.DefaultValue,
			UpdatedAt:    row.UpdatedAt.UTC().Format(time.RFC3339Nano),
			Deleted:      row.DeletedAt.Valid,
		}
	}
	return out
}

func TestMailProductNameAPISQLite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, db *gorm.DB)
		want    string
		deleted bool
	}{
		{"stored name", func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.Model(&models.Configuration{}).Where("config_key = ?", service.ConfigGeneralCompanyName).
				Update("value", "Acme Ltd").Error)
		}, "Acme Ltd", false},
		{"stored blank", func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.Model(&models.Configuration{}).Where("config_key = ?", service.ConfigGeneralCompanyName).
				Update("value", "").Error)
		}, "GopherCRM", false},
		{"row hard-deleted", func(t *testing.T, db *gorm.DB) {
			require.NoError(t, db.Unscoped().Where("config_key = ?", service.ConfigGeneralCompanyName).
				Delete(&models.Configuration{}).Error)
		}, "GopherCRM", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProductNameAPIFixture(t)
			tc.prepare(t, f.db)
			before := f.configurationRows(t)
			stored, present := before[service.ConfigGeneralCompanyName]
			if tc.deleted {
				require.False(t, present, "no general.company_name row before the sends")
			} else {
				require.True(t, present)
				require.False(t, stored.Deleted)
			}

			status, raw := f.post(t, "/auth/password-reset", map[string]string{"email": f.admin.Email}, false)
			require.Equal(t, http.StatusOK, status, raw)
			status, raw = f.post(t, fmt.Sprintf("/quotes/%d/email", f.quote.ID), nil, true)
			require.Equal(t, http.StatusOK, status, raw)
			status, raw = f.post(t, fmt.Sprintf("/tickets/%d/comments", f.ticket.ID),
				map[string]interface{}{"body": "API reply", "email_customer": true}, true)
			require.Equal(t, http.StatusCreated, status, raw)

			resets, sent := f.mailer.snapshot()
			require.Len(t, resets, 1, "one reset mail")
			assert.Equal(t, f.admin.Email, resets[0].To)
			assert.Equal(t, tc.want, resets[0].ProductName, "SendPasswordReset receives the product name exactly")
			require.Len(t, sent, 2, "one quote mail and one ticket mail")
			assert.Equal(t, fmt.Sprintf("Quote %s from %s", models.QuoteNumber(f.quote.ID), tc.want), sent[0].Subject)
			assert.Equal(t, fmt.Sprintf("Ticket %s from %s", models.TicketNumber(f.ticket.ID), tc.want), sent[1].Subject)

			after := f.configurationRows(t)
			assert.Equal(t, before, after, "the sends change no configurations row: same keys, values, defaults and updated_at")
			if tc.deleted {
				var n int64
				require.NoError(t, f.db.Unscoped().Model(&models.Configuration{}).
					Where("config_key = ?", service.ConfigGeneralCompanyName).Count(&n).Error)
				assert.Zero(t, n, "no general.company_name row exists after the sends, live or soft-deleted")
				return
			}

			// A later boot's re-seeding keeps every value and default.
			require.NoError(t, repository.NewConfigurationRepository(f.db).InitializeDefaults())
			reseeded := f.configurationRows(t)
			require.Len(t, reseeded, len(before))
			for key, row := range before {
				got, ok := reseeded[key]
				require.True(t, ok, key)
				assert.Equal(t, row.Value, got.Value, key)
				assert.Equal(t, row.DefaultValue, got.DefaultValue, key)
			}
		})
	}
}
