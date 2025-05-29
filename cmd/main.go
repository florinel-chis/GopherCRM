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

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
)

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

	router := setupRouter(cfg)

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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}

	utils.Logger.Info("Server exiting")
}

func setupRouter(cfg *config.Config) *gin.Engine {
	if cfg.Server.Mode == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	
	router.Use(middleware.CORS())
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
		setupDependencies(api, cfg)
	}

	return router
}

func setupDependencies(router *gin.RouterGroup, cfg *config.Config) {
	userRepo := repository.NewUserRepository(models.DB)
	leadRepo := repository.NewLeadRepository(models.DB)
	customerRepo := repository.NewCustomerRepository(models.DB)
	ticketRepo := repository.NewTicketRepository(models.DB)
	taskRepo := repository.NewTaskRepository(models.DB)
	apiKeyRepo := repository.NewAPIKeyRepository(models.DB)
	configRepo := repository.NewConfigurationRepository(models.DB)

	authService := service.NewAuthService(userRepo, apiKeyRepo, cfg.JWT)
	userService := service.NewUserService(userRepo)
	leadService := service.NewLeadService(leadRepo, customerRepo)
	customerService := service.NewCustomerService(customerRepo, userRepo)
	ticketService := service.NewTicketService(ticketRepo, customerRepo, userRepo)
	taskService := service.NewTaskService(taskRepo, userRepo, leadRepo, customerRepo)
	apiKeyService := service.NewAPIKeyService(apiKeyRepo)
	configService := service.NewConfigurationService(configRepo)

	authHandler := handler.NewAuthHandler(authService, userService)
	userHandler := handler.NewUserHandler(userService)
	leadHandler := handler.NewLeadHandler(leadService)
	customerHandler := handler.NewCustomerHandler(customerService)
	ticketHandler := handler.NewTicketHandler(ticketService)
	taskHandler := handler.NewTaskHandler(taskService)
	apiKeyHandler := handler.NewAPIKeyHandler(apiKeyService)
	configHandler := handler.NewConfigurationHandler(configService)
	dashboardHandler := handler.NewDashboardHandler(leadService, customerService, ticketService, taskService)

	public := router.Group("")
	{
		public.POST("/auth/register", authHandler.Register)
		public.POST("/auth/login", authHandler.Login)
	}

	protected := router.Group("")
	protected.Use(middleware.Auth(authService))
	{
		handler.SetupUserRoutes(protected, userHandler)
		handler.SetupLeadRoutes(protected, leadHandler)
		handler.SetupCustomerRoutes(protected, customerHandler)
		handler.SetupTicketRoutes(protected, ticketHandler)
		handler.SetupTaskRoutes(protected, taskHandler)
		handler.SetupAPIKeyRoutes(protected, apiKeyHandler)
		handler.SetupConfigurationRoutes(protected, configHandler)
		handler.SetupDashboardRoutes(protected, dashboardHandler)
	}
}