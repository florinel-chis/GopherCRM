package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
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
	"github.com/stretchr/testify/suite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type BaseIntegrationTestSuite struct {
	suite.Suite
	db          *gorm.DB
	router      *gin.Engine
	cfg         *config.Config
	authService service.AuthService
	server      *httptest.Server
	client      *http.Client
	baseURL     string
	mailer      *recordingMailer
}

// sentMail is one message the recording mailer was asked to deliver.
type sentMail struct {
	To, Subject, Body string
}

// recordingMailer stands in for the SMTP transport: it records every message
// and fails when err is set, so the quote email can be asserted end to end
// without a relay.
type recordingMailer struct {
	mu   sync.Mutex
	sent []sentMail
	err  error
}

func (m *recordingMailer) SendPasswordReset(to, resetURL string) error {
	return m.Send(to, "password reset", resetURL)
}

func (m *recordingMailer) Send(to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, sentMail{To: to, Subject: subject, Body: body})
	return nil
}

// fail makes every following Send return err; nil restores delivery.
func (m *recordingMailer) fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// messages returns a copy of what was sent so far.
func (m *recordingMailer) messages() []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]sentMail, len(m.sent))
	copy(out, m.sent)
	return out
}

func (m *recordingMailer) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent, m.err = nil, nil
}

func (suite *BaseIntegrationTestSuite) SetupSuite() {
	// Initialize logger
	logConfig := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}
	utils.InitLogger(&logConfig)

	// Setup test database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	suite.Require().NoError(err)
	suite.db = db
	models.DB = db

	// Run migrations
	err = models.MigrateDatabase()
	suite.Require().NoError(err)

	// Initialize config
	suite.cfg = &config.Config{
		JWT: config.JWTConfig{
			Secret:      "test-secret-key",
			ExpiryHours: 24,
		},
		Logging: config.LoggingConfig{
			Level:  "debug",
			Format: "json",
		},
		API: config.APIConfig{
			Prefix: "/api/v1",
		},
	}

	// Setup repositories
	userRepo := repository.NewUserRepository(suite.db)
	leadRepo := repository.NewLeadRepository(suite.db)
	customerRepo := repository.NewCustomerRepository(suite.db)
	ticketRepo := repository.NewTicketRepository(suite.db)
	apiKeyRepo := repository.NewAPIKeyRepository(suite.db)

	// Setup services
	suite.authService = service.NewAuthService(userRepo, apiKeyRepo, suite.cfg.JWT)
	userService := service.NewUserService(userRepo)
	txManager := utils.NewTransactionManager(suite.db)
	companyRepo := repository.NewCompanyRepository(suite.db)
	leadService := service.NewLeadService(leadRepo, customerRepo, companyRepo, txManager)
	customerService := service.NewCustomerService(customerRepo, userRepo, companyRepo)
	companyService := service.NewCompanyService(companyRepo, userRepo, txManager)
	configRepo := repository.NewConfigurationRepository(suite.db)
	suite.Require().NoError(configRepo.InitializeDefaults())
	configService := service.NewConfigurationService(configRepo, utils.NewSecretBox("test-secret-key", "configuration-secret"))
	dealRepo := repository.NewDealRepository(suite.db)
	dealService := service.NewDealService(dealRepo, companyRepo, customerRepo, leadRepo, userRepo, configService, txManager)
	productRepo := repository.NewProductRepository(suite.db)
	productService := service.NewProductService(productRepo, configService)
	// The suite records every quote email instead of delivering it; a test
	// that needs a failing mailer swaps suite.mailer.err before the call.
	suite.mailer = &recordingMailer{}
	quoteService := service.NewQuoteService(repository.NewQuoteRepository(suite.db), productRepo, dealRepo, customerRepo, userRepo, configService, suite.mailer, txManager)
	ticketService := service.NewTicketService(ticketRepo, customerRepo, userRepo)
	taskService := service.NewTaskService(repository.NewTaskRepository(suite.db), userRepo, leadRepo, customerRepo, repository.NewLabelRepository(suite.db))
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, "test-api-key-secret")

	// Setup handlers
	authHandler := handler.NewAuthHandler(suite.authService, userService)
	userHandler := handler.NewUserHandler(userService)
	leadHandler := handler.NewLeadHandler(leadService)
	customerHandler := handler.NewCustomerHandler(customerService)
	companyHandler := handler.NewCompanyHandler(companyService)
	dealHandler := handler.NewDealHandler(dealService)
	productHandler := handler.NewProductHandler(productService)
	quoteHandler := handler.NewQuoteHandler(quoteService)
	configHandler := handler.NewConfigurationHandler(configService)
	ticketHandler := handler.NewTicketHandler(ticketService, customerService)
	apiKeyHandler := handler.NewAPIKeyHandler(apiKeyService)
	dashboardHandler := handler.NewDashboardHandler(leadService, customerService, ticketService, taskService, dealService)

	// Setup router with middleware
	gin.SetMode(gin.TestMode)
	suite.router = gin.New()
	suite.router.Use(middleware.RequestID())
	suite.router.Use(middleware.Logger())
	suite.router.Use(middleware.ErrorHandler())
	suite.router.Use(middleware.Recovery())

	// Setup routes
	api := suite.router.Group("/api/v1")

	// Auth routes (public)
	api.POST("/auth/register", authHandler.Register)
	api.POST("/auth/login", authHandler.Login)

	// Protected routes
	protected := api.Group("")
	protected.Use(middleware.Auth(suite.authService))

	handler.SetupUserRoutes(protected, userHandler)
	handler.SetupLeadRoutes(protected, leadHandler)
	handler.SetupCustomerRoutes(protected, customerHandler)
	handler.SetupCompanyRoutes(protected, companyHandler)
	handler.SetupDealRoutes(protected, dealHandler)
	handler.SetupProductRoutes(protected, productHandler)
	handler.SetupQuoteRoutes(protected, quoteHandler)
	handler.SetupConfigurationRoutes(protected, configHandler)
	handler.SetupTicketRoutes(protected, ticketHandler)
	handler.SetupAPIKeyRoutes(protected, apiKeyHandler)
	handler.SetupDashboardRoutes(protected, dashboardHandler)

	// Start test server
	suite.server = httptest.NewServer(suite.router)
	suite.client = &http.Client{
		Timeout: 10 * time.Second,
	}
	suite.baseURL = suite.server.URL
}

func (suite *BaseIntegrationTestSuite) TearDownSuite() {
	suite.server.Close()
}

// Helper method to create a user
func (suite *BaseIntegrationTestSuite) CreateUser(email, password string, role models.UserRole) *models.User {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	suite.Require().NoError(err)

	user := &models.User{
		Email:       email,
		Password:    string(hashedPassword),
		Role:        role,
		IsActive:    true,
		FirstName:   "Test",
		LastName:    "User",
		LastLoginAt: nil,
	}

	err = suite.db.Create(user).Error
	suite.Require().NoError(err)

	return user
}

// Helper method to get auth token
func (suite *BaseIntegrationTestSuite) GetAuthToken(email, password string) string {
	loginReq := map[string]string{
		"email":    email,
		"password": password,
	}

	body, err := json.Marshal(loginReq)
	suite.Require().NoError(err)

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/auth/login", suite.baseURL), bytes.NewBuffer(body))
	suite.Require().NoError(err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := suite.client.Do(req)
	suite.Require().NoError(err)
	defer resp.Body.Close()

	suite.Require().Equal(http.StatusOK, resp.StatusCode)

	var loginResp utils.APIResponse
	err = json.NewDecoder(resp.Body).Decode(&loginResp)
	suite.Require().NoError(err)

	data := loginResp.Data.(map[string]interface{})
	return data["token"].(string)
}
