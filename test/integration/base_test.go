package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/florinel-chis/gocrm/internal/config"
	"github.com/florinel-chis/gocrm/internal/handler"
	"github.com/florinel-chis/gocrm/internal/middleware"
	"github.com/florinel-chis/gocrm/internal/models"
	"github.com/florinel-chis/gocrm/internal/repository"
	"github.com/florinel-chis/gocrm/internal/service"
	"github.com/florinel-chis/gocrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/suite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
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
	leadService := service.NewLeadService(leadRepo, customerRepo)
	customerService := service.NewCustomerService(customerRepo, userRepo)
	ticketService := service.NewTicketService(ticketRepo, customerRepo, userRepo)
	apiKeyService := service.NewAPIKeyService(apiKeyRepo)

	// Setup handlers
	authHandler := handler.NewAuthHandler(suite.authService, userService)
	userHandler := handler.NewUserHandler(userService)
	leadHandler := handler.NewLeadHandler(leadService)
	customerHandler := handler.NewCustomerHandler(customerService)
	ticketHandler := handler.NewTicketHandler(ticketService)
	apiKeyHandler := handler.NewAPIKeyHandler(apiKeyService)

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
	handler.SetupTicketRoutes(protected, ticketHandler)
	handler.SetupAPIKeyRoutes(protected, apiKeyHandler)

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
		Email:        email,
		Password:     string(hashedPassword),
		Role:         role,
		IsActive:     true,
		FirstName:    "Test",
		LastName:     "User",
		LastLoginAt:  nil,
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