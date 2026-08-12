package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

// @title GopherCRM API
// @version 1.1.0
// @description CRM API for managing users, leads, customers, tickets, tasks and API keys.
// @BasePath /api/v1

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description JWT access token. Format: "Bearer {token}"

// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name ApiKey
// @description API key issued via POST /api-keys. The plaintext key is shown only once at creation.

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	if err := utils.InitLogger(&cfg.Logging); err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}

	if err := models.InitDatabase(&cfg.Database); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	if err := models.MigrateDatabase(); err != nil {
		log.Fatalf("Failed to migrate database: %v", err)
	}

	// Initialize default configurations
	configRepo := repository.NewConfigurationRepository(models.DB)
	if err := configRepo.InitializeDefaults(); err != nil {
		log.Printf("Warning: Failed to initialize default configurations: %v", err)
	}

	// Background workers (currently the AEO scheduler) live for as long as the
	// process serves traffic. Cancelling this context is what stops them, and it
	// happens on the same signal that shuts the HTTP server down.
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()

	router := setupRouter(backgroundCtx, cfg)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: router,
	}

	go func() {
		utils.Logger.Infof("Starting server on port %d", cfg.Server.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	utils.Logger.Info("Shutting down server...")

	// Stop the background workers first so no new AEO run starts while the
	// server is draining.
	stopBackground()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}

	utils.Logger.Info("Server exiting")
}

func setupRouter(backgroundCtx context.Context, cfg *config.Config) *gin.Engine {
	if cfg.Server.Mode == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()

	// Configure trusted proxies to prevent IP spoofing via X-Forwarded-For.
	// By default (TRUSTED_PROXIES=""), no proxies are trusted and Gin's
	// c.ClientIP() returns the direct connection IP only.
	// To trust specific proxies, set TRUSTED_PROXIES to a comma-separated
	// list of CIDRs, e.g. "10.0.0.0/8,172.16.0.0/12".
	if err := router.SetTrustedProxies(cfg.Server.TrustedProxies); err != nil {
		log.Fatalf("Failed to set trusted proxies: %v", err)
	}

	// Public form endpoints are embedded on arbitrary third-party sites, so
	// they get permissive credential-less CORS; every other route keeps the
	// strict allowlist.
	router.Use(middleware.CORSWithPublicPrefixes(
		[]string{cfg.API.Prefix + "/forms/public"},
		cfg.Server.CORSExtraOrigins...))
	router.Use(middleware.RequestID())
	router.Use(middleware.Logger())
	router.Use(middleware.Recovery())
	router.Use(middleware.ErrorHandler())

	router.GET("/health", func(c *gin.Context) {
		utils.RespondSuccess(c, http.StatusOK, gin.H{
			"status": "healthy",
			"time":   time.Now().UTC(),
		})
	})

	api := router.Group(cfg.API.Prefix)
	{
		setupDependencies(backgroundCtx, api, cfg)
	}

	return router
}

func setupDependencies(backgroundCtx context.Context, router *gin.RouterGroup, cfg *config.Config) {
	userRepo := repository.NewUserRepository(models.DB)
	leadRepo := repository.NewLeadRepository(models.DB)
	// Erasing a customer must also erase the lead it was converted from: the
	// conversion copied the person's name, email, phone and company into the
	// customer and left the originals in the lead. This constructor is the plain
	// customer repository plus that cascade, and it is the one the application
	// must be wired with.
	customerRepo := repository.NewCustomerRepositoryWithLeadErasure(models.DB)
	ticketRepo := repository.NewTicketRepository(models.DB)
	taskRepo := repository.NewTaskRepository(models.DB)
	labelRepo := repository.NewLabelRepository(models.DB)
	apiKeyRepo := repository.NewAPIKeyRepository(models.DB)
	configRepo := repository.NewConfigurationRepository(models.DB)
	refreshTokenRepo := repository.NewRefreshTokenRepository(models.DB)
	passwordResetRepo := repository.NewPasswordResetTokenRepository(models.DB)
	bulkOperationRepo := repository.NewBulkOperationRepository(models.DB)
	bulkRepo := repository.NewBulkRepository(models.DB)
	aeoRepo := repository.NewAEORepository(models.DB)
	formRepo := repository.NewFormRepository(models.DB)

	appMailer := mailer.NewFromConfig(cfg.SMTP)

	authService := service.NewAuthServiceWithSessions(
		userRepo, apiKeyRepo, refreshTokenRepo, passwordResetRepo, appMailer,
		cfg.JWT, cfg.App.BaseURL, cfg.API.APIKeySecret)
	userService := service.NewUserService(userRepo)
	txManager := utils.NewTransactionManager(models.DB)
	leadService := service.NewLeadService(leadRepo, customerRepo, txManager)
	customerService := service.NewCustomerService(customerRepo, userRepo)
	ticketService := service.NewTicketService(ticketRepo, customerRepo, userRepo)
	taskService := service.NewTaskService(taskRepo, userRepo, leadRepo, customerRepo, labelRepo)
	labelService := service.NewLabelService(labelRepo)
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, cfg.API.APIKeySecret)
	configService := service.NewConfigurationService(configRepo,
		utils.NewSecretBox(cfg.API.APIKeySecret, "configuration-secret"))
	bulkService := service.NewBulkOperationService(
		bulkOperationRepo, bulkRepo, userRepo, leadRepo, customerRepo,
		taskRepo, ticketRepo, txManager, utils.Logger,
	)

	// AEO. Only engines with credentials are loaded, so a deployment that sets
	// no provider key still boots — the module then answers 503 on run creation
	// instead of recording runs that can never produce an answer. The status
	// roster is separate because it must also name the engines that are missing
	// a key.
	aeoProviders := aeo.LoadProviders(cfg)
	aeoEngine := aeo.NewEngine(aeoRepo, aeoProviders, aeo.EngineOptions{
		QueryTimeout: time.Duration(cfg.AEO.QueryTimeoutSeconds) * time.Second,
	})
	aeoService := service.NewAEOService(aeoRepo, aeoEngine, aeoProviders, txManager,
		service.WithAEOProviderStatuses(aeo.ProviderStatuses(cfg)),
		// Admin-stored provider keys win over the environment, resolved freshly
		// at each run start and status read so a key saved in the settings UI
		// takes effect without a restart.
		service.WithAEOConfigSource(func() config.AEOConfig {
			return service.EffectiveAEOConfig(cfg.AEO, configService)
		}),
		service.WithAEOGenerationEngineSource(func() string {
			engine, err := configService.GetString(service.ConfigAEOGenerationEngine)
			if err != nil {
				return ""
			}
			return engine
		}))

	formService := service.NewFormService(formRepo, leadRepo, userRepo, appMailer,
		txManager, cfg.Forms, cfg.API.Prefix)

	authHandler := handler.NewAuthHandler(authService, userService)
	userHandler := handler.NewUserHandler(userService)
	leadHandler := handler.NewLeadHandler(leadService)
	customerHandler := handler.NewCustomerHandler(customerService)
	ticketHandler := handler.NewTicketHandler(ticketService, customerService)
	taskHandler := handler.NewTaskHandler(taskService)
	labelHandler := handler.NewLabelHandler(labelService)
	apiKeyHandler := handler.NewAPIKeyHandler(apiKeyService)
	configHandler := handler.NewConfigurationHandler(configService)
	dashboardHandler := handler.NewDashboardHandler(leadService, customerService, ticketService, taskService)
	bulkHandler := handler.NewBulkHandler(bulkService)
	aeoHandler := handler.NewAEOHandler(aeoService)
	formHandler := handler.NewFormHandler(formService)
	formPublicHandler := handler.NewFormPublicHandler(formService, cfg.Forms, cfg.API.Prefix)

	// Recover runs stranded by the previous process before anything can start a
	// new one. The engine runs in-process and writes the terminal status itself,
	// so a row still marked "running" at boot belongs to a process that died
	// mid-run (deploy, OOM, docker restart — even a graceful SIGTERM, which
	// drains HTTP without waiting for a run that may have hours of provider
	// calls left). Left alone, one such row makes the overlap guard answer 409
	// to every manual run and skip every scheduled one, permanently.
	if recovered, err := aeoService.ReconcileRunningRuns(); err != nil {
		utils.Logger.WithError(err).Error("Failed to reconcile stranded AEO runs")
	} else if recovered > 0 {
		utils.Logger.WithField("runs", recovered).
			Warn("Recovered AEO runs left behind by a previous process")
	}

	// The daily run is opt-out. It is skipped entirely when no engine is
	// configured, because every tick would otherwise log the same 503-shaped
	// failure.
	if cfg.AEO.ScheduleEnabled && len(aeoProviders) > 0 {
		aeo.StartScheduler(backgroundCtx, aeoService, cfg.AEO.ScheduleHour)
	} else {
		utils.Logger.WithFields(map[string]interface{}{
			"enabled":   cfg.AEO.ScheduleEnabled,
			"providers": len(aeoProviders),
		}).Info("AEO scheduler not started")
	}

	// Public routes with strict rate limiting for auth endpoints
	public := router.Group("")
	{
		// Apply strict rate limiting to authentication endpoints
		// 5 requests per minute with burst of 2 to prevent brute force attacks
		authRoutes := public.Group("/auth")
		authRoutes.Use(middleware.RateLimitStrict())
		{
			authRoutes.POST("/register", authHandler.Register)
			authRoutes.POST("/login", authHandler.Login)
			// Refresh is a credential exchange, so it stays on the strict tier.
			authRoutes.POST("/refresh", authHandler.Refresh)
			authRoutes.POST("/password-reset", authHandler.RequestPasswordReset)
			authRoutes.POST("/password-reset/confirm", authHandler.ConfirmPasswordReset)
		}

		// Embedded forms: definition, submission intake and the email-confirm
		// pages. Per-route rate tiers are set inside (generous on reads, strict
		// on submits and confirms).
		handler.SetupFormPublicRoutes(public, formPublicHandler)
	}

	// Protected routes with moderate rate limiting
	protected := router.Group("")
	protected.Use(middleware.Auth(authService))
	protected.Use(middleware.RateLimitModerate()) // 60 req/min for authenticated users
	{
		handler.SetupUserRoutes(protected, userHandler)
		handler.SetupLeadRoutes(protected, leadHandler)
		handler.SetupCustomerRoutes(protected, customerHandler)
		handler.SetupTicketRoutes(protected, ticketHandler)
		handler.SetupTaskRoutes(protected, taskHandler)
		handler.SetupLabelRoutes(protected, labelHandler)
		handler.SetupAPIKeyRoutes(protected, apiKeyHandler)
		handler.SetupConfigurationRoutes(protected, configHandler)
		handler.SetupDashboardRoutes(protected, dashboardHandler)
		handler.SetupBulkStatusRoutes(protected, bulkHandler)
		handler.SetupAEORoutes(protected, aeoHandler)
		handler.SetupFormRoutes(protected, formHandler)

		protectedAuth := protected.Group("/auth")
		{
			protectedAuth.POST("/logout", authHandler.Logout)
			protectedAuth.POST("/change-password", authHandler.ChangePassword)
		}
	}
}
