package main

import (
	"context"
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
// @version 1.2.0
// @description GopherCRM is a self-hosted CRM: users, leads, customers, companies, deals, products, quotes, tickets, tasks, labels, follow-up automation, notifications, embeddable lead-capture forms and answer-engine visibility tracking (AEO). Every operation in this reference lives under the base path /api/v1 (configurable through API_PREFIX). GET /health is served outside the base path and is deliberately not part of this reference.
// @description
// @description ## Authentication
// @description Protected operations accept one credential in the Authorization header, either `Authorization: Bearer <jwt>` (an access token from POST /auth/login or POST /auth/refresh) or `Authorization: ApiKey <key>` (a personal key from POST /api-keys; the plaintext key is shown once, at creation, and the gcrm_ prefix may be sent or left off). No other header, query parameter or cookie is read. A missing header, or one that is neither form, is 401 UNAUTHORIZED "Missing or invalid authorization header"; an invalid or expired credential, or one whose account is deactivated or erased, is 401 UNAUTHORIZED "Invalid credentials".
// @description Login returns an access token (valid for JWT_EXPIRY_HOURS, 24 hours by default), a single-use refresh token (valid for REFRESH_TOKEN_EXPIRY_DAYS, 30 days by default) and the user. POST /auth/refresh rotates strictly: the presented refresh token is spent and replaced, a spent token is refused with 401, and of several concurrent refreshes of one token exactly one succeeds, so a client must keep the replacement from every response. Logout, POST /auth/change-password and password reset revoke refresh tokens (a password changed through PUT /users/me revokes none), but an issued access token cannot be revoked early: it stays valid until it expires. Registration is open only while the security.allow_public_registration setting is on (it ships off), always creates a customer and returns an access token without a refresh token.
// @description Roles are admin, sales, support and customer. Each operation names the roles it admits; another role gets 403 FORBIDDEN "Insufficient permissions". Where a role sees only some records (for example sales and its own leads), the operation describes the narrowing and the status a record outside it is answered with.
// @description
// @description ## Response envelope
// @description Every JSON response has the shape {success, data, error, meta}. success is true for a 2xx response; data carries the payload (its shape is documented per operation); error is present only on failures; meta always carries request_id and, on paginated lists, page, per_page, total and total_pages (zero values are omitted, so an empty page has no total). A 204 response has no body.
// @description Exceptions that are not enveloped: GET /customers/export (text/csv), the public form pages GET /forms/public/embed.js (JavaScript), GET /forms/public/{key}/view and GET/POST /forms/public/confirm (HTML), a CORS rejection (a bare 403 with no body) and a path or method that no route matches (the router's plain-text 404 "404 page not found").
// @description
// @description ## Errors
// @description error.code is the stable value to branch on; error.message is for people and may change. Shared codes:
// @description - 400 VALIDATION_ERROR: a binding or field rule failed. error.details is usually an object mapping each failed field to a message; binding failures key it by the server-side Go field name (for example FirstName), while service-level field checks use the JSON name. Operations that send another shape document it (a lead conversion rule sends {field}, plus status and allowed_statuses for a refused status; POST /aeo/prompts/{id}/run with an invalid id sends a plain string), and some omit details. A body over the public form submission limit is 413 with this code.
// @description - 400 BAD_REQUEST: malformed JSON ("Invalid request format"), typically an unparsable path id, or another request the handler refuses as a whole.
// @description - 400 INVALID_INPUT and INVALID_REFERENCE: a rule the service checks beyond the binding (automation rules, bulk status requests), or an id that names no live record (for example company_id, label_ids, a quote line's product).
// @description - 401 UNAUTHORIZED: missing, malformed, invalid or expired credentials.
// @description - 403 FORBIDDEN: the role is not admitted, or the record belongs to someone else where the operation says so.
// @description - 404 NOT_FOUND: no live record with that id within the caller's visibility. Erased (soft-deleted) records are never returned.
// @description - 409 CONFLICT and SESSION_STALE: a duplicate (email, domain, label name), a refused state change, or a stale session on a password change.
// @description - INVALID_STATUS_TRANSITION: a refused status change; 400 for a quote transition, 409 for cancelling an automation run that has already finished.
// @description - 429 TOO_MANY_REQUESTS: see Rate limits.
// @description - 500 INTERNAL_ERROR: an unexpected failure ("An unexpected error occurred"); details are logged server-side, never returned.
// @description - 502 MAIL_DELIVERY_FAILED (sending a quote by email), 503 SESSION_REFRESH_UNAVAILABLE (refresh), 503 PROVIDER_NOT_CONFIGURED, PROVIDER_REJECTED, PROVIDERS_UNAVAILABLE and SHUTTING_DOWN (AEO).
// @description Each operation lists the codes it can return and the condition behind each.
// @description
// @description ## Pagination, sorting and filtering
// @description List operations take offset (default 0; negative or non-numeric values become 0) and limit (default 20; 0, negative or non-numeric values become 20; values above 100 are clamped to 100). Some lists also take a 1-based page that, when present, overrides offset. Out-of-range values are clamped or replaced, never rejected. Sortable lists accept sort_by from a fixed per-entity column list and sort_order asc or desc; what an unknown value does (ignored, defaulted or refused) is documented per operation.
// @description
// @description ## Rate limits
// @description Requests are counted per client IP (the direct peer address unless TRUSTED_PROXIES names the proxy in front) in three tiers, each with its own buckets: strict, 10 requests per minute with a burst of 5, on every unauthenticated /auth route and on public form submission and confirmation; authenticated, 120 per minute with a burst of 30, shared by every authenticated operation and counted after authentication; generous, 240 per minute with a burst of 40, on public form reads. An exceeded limit is 429 TOO_MANY_REQUESTS with error.details {"retry_after": "60s"}, a fixed hint rather than the real wait. No Retry-After or X-RateLimit-* header is sent. Preflight OPTIONS requests are not counted.
// @description
// @description ## CORS and headers
// @description Browser requests are admitted only from the configured origins (localhost development origins plus CORS_ALLOWED_ORIGINS), with credentials allowed, the methods GET, POST, PUT, PATCH, DELETE and OPTIONS and the request headers Origin, Content-Type, Authorization and X-Request-ID. A request from any other origin is refused with a bare 403 before any handler or log line. The public form endpoints under /forms/public answer any origin without credentials (methods GET, POST and OPTIONS, request header Content-Type).
// @description Every response except a CORS preflight answer or a CORS rejection carries X-Request-ID: the value the client sent in that header, otherwise a generated UUID. The same value is meta.request_id in the envelope. Browsers on the configured origins can read X-Request-ID and Content-Length (exposed headers); the public form endpoints expose no headers, so a cross-origin page reads the id from meta.request_id instead.
// @BasePath /api/v1

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description JWT access token from POST /auth/login or POST /auth/refresh, sent as "Authorization: Bearer <jwt>". Valid until it expires; logout does not revoke it.

// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name Authorization
// @description Personal API key from POST /api-keys, sent in the same Authorization header as "Authorization: ApiKey <key>" (the gcrm_ prefix may be sent or left off). A header named ApiKey is not read. The plaintext key is shown only once, at creation.

// @tag.name auth
// @tag.description Login, registration, token refresh, logout, password change and reset.
// @tag.name users
// @tag.description Staff and customer accounts, the caller's own profile, activation (admin).
// @tag.name api-keys
// @tag.description Personal API keys for the Authorization: ApiKey <key> scheme.
// @tag.name configurations
// @tag.description System settings, UI configuration and encrypted integration secrets.
// @tag.name dashboard
// @tag.description Aggregated widgets: stats, pipeline, activity, recent records.
// @tag.name leads
// @tag.description Leads, conversion to customers, lead bulk status.
// @tag.name customers
// @tag.description Customers, CSV export, linked login, their tickets.
// @tag.name companies
// @tag.description Companies (organisations) and their linked records.
// @tag.name deals
// @tag.description Deal pipeline, stage changes and stage history.
// @tag.name products
// @tag.description Product catalogue used by quote lines.
// @tag.name quotes
// @tag.description Quotes, quote lines, status workflow and history.
// @tag.name tickets
// @tag.description Support tickets, comments, ticket bulk status.
// @tag.name tasks
// @tag.description Tasks, upcoming tasks, task bulk status.
// @tag.name labels
// @tag.description Task labels.
// @tag.name automation
// @tag.description Automation rules, dry runs and run history.
// @tag.name notifications
// @tag.description In-app notifications created by automation.
// @tag.name aeo
// @tag.description Answer-engine visibility tracking: profile, prompts, runs, reports.
// @tag.name forms
// @tag.description Embeddable lead-capture forms and their submissions (staff side).
// @tag.name public-forms
// @tag.description Unauthenticated endpoints reached by visitors of the sites a form is embedded on: definition, submission, embed script, view page and double opt-in confirmation. Some return JavaScript or HTML instead of the JSON envelope; CORS is open to any origin without credentials.

// revision is the commit the binary was built from, set by the release build
// (-ldflags "-X main.revision=<sha>"). The deploy pipeline waits for /health to
// report it, so a deploy is only done once the new build actually serves.
var revision = "dev"

// healthPayload is the body of GET /health.
func healthPayload(now time.Time) gin.H {
	return gin.H{
		"status":   "healthy",
		"time":     now.UTC(),
		"revision": revision,
	}
}

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
	// happens on the same signal that shuts the HTTP server down. stopAEO is
	// the drain handle: it closes run admission, cancels and joins the
	// scheduler and every active run, so the database is closed only once
	// nothing can still write to it.
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()

	router, stopAEO := setupRouter(backgroundCtx, cfg)

	srv := &http.Server{
		Addr:    cfg.Server.Address(),
		Handler: router,
	}

	go func() {
		utils.Logger.Infof("Starting server on %s", cfg.Server.Address())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	utils.Logger.Info("Shutting down server...")

	// Stop the background workers and drain the automation worker and the
	// AEO runtime first: admission closes, so no new run starts while the
	// server is draining, an automation step in flight finishes or rolls
	// back, and every scheduled, manual or single-prompt AEO run already in
	// flight is cancelled and joined under aeoDrainTimeout. A request that
	// asks for an AEO run from here on is answered 503.
	stopBackground()
	drainErr := drainAEO(stopAEO, aeoDrainTimeout)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	shutdownErr := srv.Shutdown(ctx)

	if shutdownErr != nil || drainErr != nil {
		// A drain deadline expired, so handlers or a run may still be running
		// queries. sql.DB.Close() waits for every in-flight query with no
		// deadline of its own, which would hang the process well past the
		// timeout it just blew. Skip the close and exit: the WAL is recovered on
		// the next open, so at worst we forfeit the checkpoint, never the
		// committed data, and a run left in "running" is failed by the next
		// boot's reconciliation.
		utils.Logger.Warn("Skipping database close: shutdown timed out with work still in flight")
		log.Fatal("Server forced to shutdown:", shutdownFailure(shutdownErr, drainErr))
	}

	// Reached only after a clean drain, so neither a request nor a run can
	// still be in flight, and before any fatal exit: on SQLite the final close
	// is what checkpoints the write-ahead log into the database file.
	if err := models.CloseDatabase(); err != nil {
		utils.Logger.Errorf("Failed to close database: %v", err)
	}

	utils.Logger.Info("Server exiting")
}

func setupRouter(backgroundCtx context.Context, cfg *config.Config) (*gin.Engine, func(context.Context) error) {
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
		utils.RespondSuccess(c, http.StatusOK, healthPayload(time.Now()))
	})

	api := router.Group(cfg.API.Prefix)
	stopAEO := setupDependencies(backgroundCtx, api, cfg)

	return router, stopAEO
}

// setupDependencies wires every repository, service and handler onto the API
// group and returns the function that drains the automation worker and the
// AEO runtime at shutdown, in that order, before main closes the database.
func setupDependencies(backgroundCtx context.Context, router *gin.RouterGroup, cfg *config.Config) func(context.Context) error {
	userRepo := repository.NewUserRepository(models.DB)
	leadRepo := repository.NewLeadRepository(models.DB)
	// The user, customer and lead repositories erase a whole person on Delete
	// (the lead it was converted from, the customer it became, the customer's
	// own login): the cascade lives in the plain repositories, so there is no
	// special constructor to remember. See repository/erasure_cascade.go.
	customerRepo := repository.NewCustomerRepository(models.DB)
	ticketRepo := repository.NewTicketRepository(models.DB)
	taskRepo := repository.NewTaskRepository(models.DB)
	labelRepo := repository.NewLabelRepository(models.DB)
	companyRepo := repository.NewCompanyRepository(models.DB)
	dealRepo := repository.NewDealRepository(models.DB)
	productRepo := repository.NewProductRepository(models.DB)
	quoteRepo := repository.NewQuoteRepository(models.DB)
	apiKeyRepo := repository.NewAPIKeyRepository(models.DB)
	configRepo := repository.NewConfigurationRepository(models.DB)
	refreshTokenRepo := repository.NewRefreshTokenRepository(models.DB)
	passwordResetRepo := repository.NewPasswordResetTokenRepository(models.DB)
	bulkOperationRepo := repository.NewBulkOperationRepository(models.DB)
	bulkStatusRepo := repository.NewBulkStatusRepository(models.DB)
	aeoRepo := repository.NewAEORepository(models.DB)
	formRepo := repository.NewFormRepository(models.DB)
	automationRepo := repository.NewAutomationRepository(models.DB)
	notificationRepo := repository.NewNotificationRepository(models.DB)

	appMailer := mailer.NewFromConfig(cfg.SMTP)
	txManager := utils.NewTransactionManager(models.DB)

	// The password reset confirmation claims the token, writes the password
	// and revokes the sessions in one transaction, so the auth service takes
	// the same transaction manager as the other transactional services.
	authService := service.NewAuthServiceWithSessions(
		userRepo, apiKeyRepo, refreshTokenRepo, passwordResetRepo, txManager, appMailer,
		cfg.JWT, cfg.App.BaseURL, cfg.API.APIKeySecret)
	userService := service.NewUserService(userRepo)
	configService := service.NewConfigurationService(configRepo,
		utils.NewSecretBox(cfg.API.APIKeySecret, "configuration-secret"))
	// Every lead and deal write path records its automation events in the
	// transaction that commits the write (issue #164): the lead service, the
	// deal service, the form submission path and the bulk status path all take the
	// same outbox repository. Lead conversion enforces the administrator's
	// lead-conversion settings (issue #104).
	leadService := service.NewLeadService(leadRepo, customerRepo, companyRepo, txManager,
		service.WithLeadAutomationEvents(automationRepo),
		service.WithLeadConversionRules(configService))
	customerService := service.NewCustomerService(customerRepo, userRepo, companyRepo)
	companyService := service.NewCompanyService(companyRepo, userRepo, txManager)
	ticketService := service.NewTicketService(ticketRepo, customerRepo, userRepo)
	taskService := service.NewTaskService(taskRepo, userRepo, leadRepo, customerRepo, labelRepo)
	labelService := service.NewLabelService(labelRepo)
	apiKeyService := service.NewAPIKeyService(apiKeyRepo, cfg.API.APIKeySecret)
	dealService := service.NewDealService(dealRepo, companyRepo, customerRepo, leadRepo, userRepo, configService, txManager,
		service.WithDealAutomationEvents(automationRepo))
	// Products take the deals' default currency; quotes email through the
	// same mailer as the password reset and the forms.
	productService := service.NewProductService(productRepo, configService)
	quoteService := service.NewQuoteService(quoteRepo, productRepo, dealRepo, customerRepo, userRepo, configService, appMailer, txManager)
	bulkService := service.NewBulkStatusService(
		bulkOperationRepo, bulkStatusRepo, txManager, utils.Logger,
		service.WithBulkAutomationEvents(automationRepo),
	)

	// AEO. Only engines with credentials are loaded, so a deployment that sets
	// no provider key still boots — the module then answers 503 on run creation
	// instead of recording runs that can never produce an answer. The status
	// roster is separate because it must also name the engines that are missing
	// a key. The runtime owns every run the service starts; its Stop is the
	// drain handle this function returns.
	aeoProviders := aeo.LoadProviders(cfg)
	aeoEngine := aeo.NewEngine(aeoRepo, aeoProviders, aeo.EngineOptions{
		QueryTimeout: time.Duration(cfg.AEO.QueryTimeoutSeconds) * time.Second,
	})
	aeoRuntime := newAEORuntime(service.NewAEORuntime(aeoRepo, aeoEngine, aeoProviders, txManager,
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
		})))
	aeoService := aeoRuntime.runs.Service()

	formService := service.NewFormService(formRepo, leadRepo, userRepo, appMailer,
		txManager, cfg.Forms, cfg.API.Prefix, service.WithFormAutomationEvents(automationRepo))

	// Follow-up automation. The engine executes rules behind locking reads
	// and conditional advances; the runtime owns the worker goroutine and is
	// the second drain handle main receives. The worker is started only when
	// AUTOMATION_ENABLED is on; the API and the event capture work either way.
	automationEngine := service.NewAutomationEngine(automationRepo, notificationRepo, taskRepo, userRepo, appMailer, txManager)
	automationService := service.NewAutomationService(automationRepo, userRepo, automationEngine, txManager)
	notificationService := service.NewNotificationService(notificationRepo)
	var automationRuntime *service.AutomationRuntime
	if cfg.Automation.Enabled {
		automationRuntime = service.NewAutomationRuntime(automationEngine,
			time.Duration(cfg.Automation.PollIntervalSeconds)*time.Second)
	}
	automation := newAutomationBackground(automationRuntime)

	authHandler := handler.NewAuthHandler(authService, userService)
	userHandler := handler.NewUserHandler(userService)
	leadHandler := handler.NewLeadHandler(leadService)
	customerHandler := handler.NewCustomerHandler(customerService)
	ticketHandler := handler.NewTicketHandler(ticketService, customerService)
	taskHandler := handler.NewTaskHandler(taskService)
	labelHandler := handler.NewLabelHandler(labelService)
	companyHandler := handler.NewCompanyHandler(companyService)
	dealHandler := handler.NewDealHandler(dealService)
	productHandler := handler.NewProductHandler(productService)
	quoteHandler := handler.NewQuoteHandler(quoteService)
	apiKeyHandler := handler.NewAPIKeyHandler(apiKeyService)
	configHandler := handler.NewConfigurationHandler(configService)
	dashboardHandler := handler.NewDashboardHandler(leadService, customerService, ticketService, taskService, dealService)
	bulkHandler := handler.NewBulkHandler(bulkService)
	aeoHandler := handler.NewAEOHandler(aeoService)
	formHandler := handler.NewFormHandler(formService)
	formPublicHandler := handler.NewFormPublicHandler(formService, cfg.Forms, cfg.API.Prefix)
	automationHandler := handler.NewAutomationHandler(automationService)
	notificationHandler := handler.NewNotificationHandler(notificationService)

	// Recover the automation runs a previous process left in running before
	// the worker and the HTTP server start, under the single-replica
	// assumption: no live executor owns them. A run goes back to pending at
	// its next position; one whose last step row is an email in sending is
	// failed with email_unconfirmed, so the mail is never sent twice.
	//
	// Only the process that runs the worker reconciles. A replica started with
	// AUTOMATION_ENABLED=false owns no run and must not touch the ones the
	// worker replica is executing: resetting a live running run would make its
	// next conditional advance fail and the step be re-executed, and failing
	// an email mid-send would leave the run failed under a step the worker
	// then marks succeeded.
	if cfg.Automation.Enabled {
		if reset, failed, err := automationEngine.Reconcile(); err != nil {
			utils.Logger.WithError(err).Error("Failed to reconcile stranded automation runs")
		} else if reset > 0 || failed > 0 {
			utils.Logger.WithFields(map[string]interface{}{"reset": reset, "failed": failed}).
				Warn("Recovered automation runs left behind by a previous process")
		}
	}

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

	// The daily run is opt-out. It is armed whenever scheduling is enabled,
	// whatever the boot-time roster: provider keys are administrator-editable,
	// so the engines are resolved when a tick is due and a key stored through
	// the settings UI is picked up by the next scheduled run without a restart.
	// A tick that finds the module unconfigured is logged once at info level
	// and the loop sleeps until the next day. Disabled scheduling arms nothing.
	if cfg.AEO.ScheduleEnabled {
		aeoRuntime.startScheduler(backgroundCtx, cfg.AEO.ScheduleHour)
	} else {
		utils.Logger.WithFields(map[string]interface{}{
			"enabled":   cfg.AEO.ScheduleEnabled,
			"providers": len(aeoProviders),
		}).Info("AEO scheduler not started")
	}

	// The automation worker, after the reconciliation above and before the
	// routes are served.
	if cfg.Automation.Enabled {
		automation.start(backgroundCtx)
	} else {
		utils.Logger.Info("Automation worker not started")
	}

	// Public routes with strict rate limiting for auth endpoints
	public := router.Group("")
	{
		// Apply strict rate limiting to authentication endpoints
		// 10 requests per minute with burst of 5 to prevent brute force attacks
		authRoutes := public.Group("/auth")
		authRoutes.Use(middleware.RateLimitStrict())
		{
			// The sign-up switch (security.allow_public_registration, off by
			// default) is enforced here and probed by the SPA through the
			// unauthenticated status endpoint.
			authRoutes.GET("/registration", handler.RegistrationStatus(configService))
			authRoutes.POST("/register", middleware.RequireRegistrationEnabled(configService), authHandler.Register)
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
	protected.Use(middleware.RateLimitModerate()) // 120 req/min, burst 30, for authenticated users
	{
		handler.SetupUserRoutes(protected, userHandler)
		handler.SetupLeadRoutes(protected, leadHandler)
		handler.SetupCustomerRoutes(protected, customerHandler)
		handler.SetupCompanyRoutes(protected, companyHandler)
		handler.SetupDealRoutes(protected, dealHandler)
		handler.SetupProductRoutes(protected, productHandler)
		handler.SetupQuoteRoutes(protected, quoteHandler)
		handler.SetupTicketRoutes(protected, ticketHandler)
		handler.SetupTaskRoutes(protected, taskHandler)
		handler.SetupLabelRoutes(protected, labelHandler)
		handler.SetupAPIKeyRoutes(protected, apiKeyHandler)
		handler.SetupConfigurationRoutes(protected, configHandler)
		handler.SetupDashboardRoutes(protected, dashboardHandler)
		handler.SetupBulkStatusRoutes(protected, bulkHandler)
		handler.SetupAEORoutes(protected, aeoHandler)
		handler.SetupFormRoutes(protected, formHandler)
		handler.SetupAutomationRoutes(protected, automationHandler)
		handler.SetupNotificationRoutes(protected, notificationHandler)

		protectedAuth := protected.Group("/auth")
		{
			protectedAuth.POST("/logout", authHandler.Logout)
			protectedAuth.POST("/change-password", authHandler.ChangePassword)
		}
	}

	// One drain handle for both runtimes: the automation worker first (its
	// step transactions are short), then the AEO runs.
	return combineStops(automation.Stop, aeoRuntime.Stop)
}
