package tests

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// userWriteStack is POST /auth/register and POST /users on the real handlers,
// services and repositories over in-memory SQLite. The user service carries
// the role governance, as the application wires it. failUserInsert, when set,
// is the driver error every insert into users fails with.
type userWriteStack struct {
	t              *testing.T
	db             *gorm.DB
	router         *gin.Engine
	logs           *logrustest.Hook
	adminToken     string
	failUserInsert string
	sequence       int
}

func newUserWriteStack(t *testing.T) *userWriteStack {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "warn", Format: "json"}))
	utils.Logger.SetOutput(io.Discard)
	stack := &userWriteStack{t: t, logs: logrustest.NewLocal(utils.Logger)}

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, models.MigrateDatabaseWith(db))
	stack.db = db

	// The store failure: the statement never reaches the driver and the
	// repository sees the error a failing INSERT would hand it.
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:fail_user_insert", func(tx *gorm.DB) {
		if stack.failUserInsert == "" || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "users" {
			return
		}
		_ = tx.AddError(errors.New(stack.failUserInsert))
	}))

	userRepo := repository.NewUserRepository(db)
	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: "user-write-errors-test-secret", ExpiryHours: 1})
	userService := service.NewUserService(userRepo, service.WithUserRoleGovernance(
		repository.NewRoleRepository(db), repository.NewRoleAuditRepository(db), utils.NewTransactionManager(db)))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.ErrorHandler())
	api := router.Group("/api/v1")
	api.POST("/auth/register", handler.NewAuthHandler(authService, userService).Register)
	protected := api.Group("")
	protected.Use(middleware.Auth(authService))
	handler.SetupUserRoutes(protected, handler.NewUserHandler(userService))
	stack.router = router

	admin := &models.User{Email: "admin@user-write.test", Password: "not-a-login-hash",
		FirstName: "Ada", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, userRepo.Create(admin))
	stack.adminToken, err = authService.GenerateJWT(admin)
	require.NoError(t, err)
	return stack
}

func (s *userWriteStack) email(label string) string {
	s.sequence++
	return fmt.Sprintf("%s-%d@user-write.test", label, s.sequence)
}

// userWriteRoute is one of the two routes that store a new user.
type userWriteRoute struct {
	name    string
	handler string // the handler field of its log entries
	send    func(s *userWriteStack, email, password string) *httptest.ResponseRecorder
}

func userWriteRoutes() []userWriteRoute {
	post := func(s *userWriteStack, path, token string, body map[string]string) *httptest.ResponseRecorder {
		raw, err := json.Marshal(body)
		require.NoError(s.t, err)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		s.logs.Reset()
		s.router.ServeHTTP(rec, req)
		return rec
	}
	return []userWriteRoute{
		{"POST /auth/register", "AuthHandler.Register", func(s *userWriteStack, email, password string) *httptest.ResponseRecorder {
			return post(s, "/api/v1/auth/register", "", map[string]string{
				"email": email, "password": password, "first_name": "Rita", "last_name": "Register"})
		}},
		{"POST /users", "UserHandler.Create", func(s *userWriteStack, email, password string) *httptest.ResponseRecorder {
			return post(s, "/api/v1/users", s.adminToken, map[string]string{
				"email": email, "password": password, "first_name": "Uma", "last_name": "User", "role": "sales"})
		}},
	}
}

func (s *userWriteStack) userCount() int64 {
	s.t.Helper()
	var count int64
	require.NoError(s.t, s.db.Model(&models.User{}).Count(&count).Error)
	return count
}

// handlerLogLevels are the levels of the entries the named handler logged
// for the last request.
func (s *userWriteStack) handlerLogLevels(handlerName string) []logrus.Level {
	var levels []logrus.Level
	for _, entry := range s.logs.AllEntries() {
		if entry.Data["handler"] == handlerName {
			levels = append(levels, entry.Level)
		}
	}
	return levels
}

func userWriteError(t *testing.T, rec *httptest.ResponseRecorder) *utils.APIError {
	t.Helper()
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	assert.False(t, response.Success)
	require.NotNil(t, response.Error, rec.Body.String())
	return response.Error
}

// TestUserWriteFailureIsInternalError: a user that cannot be stored is a 500
// INTERNAL_ERROR with the generic message on POST /auth/register and
// POST /users, the driver text goes to the log only, and the answers a client
// can act on (duplicate email, password policy) keep their codes (#397).
func TestUserWriteFailureIsInternalError(t *testing.T) {
	const validPassword = "Str0ng!Passw0rd"
	atLimit := "Aa1!" + strings.Repeat("x", 68)
	require.Len(t, atLimit, 72)

	t.Run("control: without the failure both routes answer 201", func(t *testing.T) {
		stack := newUserWriteStack(t)
		for _, route := range userWriteRoutes() {
			before := stack.userCount()
			rec := route.send(stack, stack.email("control"), validPassword)
			assert.Equal(t, http.StatusCreated, rec.Code, "%s: %s", route.name, rec.Body.String())
			assert.Equal(t, before+1, stack.userCount(), route.name)

			// 72 bytes go through the real bcrypt; 73 are refused and store nothing.
			rec = route.send(stack, stack.email("at-limit"), atLimit)
			assert.Equal(t, http.StatusCreated, rec.Code, "%s: %s", route.name, rec.Body.String())
			assert.Equal(t, before+2, stack.userCount(), route.name)

			rec = route.send(stack, stack.email("over-limit"), atLimit+"x")
			assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", route.name, rec.Body.String())
			apiError := userWriteError(t, rec)
			assert.Equal(t, "BAD_REQUEST", apiError.Code, route.name)
			assert.Equal(t, "password must be at most 72 bytes long", apiError.Message, route.name)
			assert.Equal(t, before+2, stack.userCount(), "%s: no user stored", route.name)
		}
	})

	t.Run("a failed insert is 500 INTERNAL_ERROR without the driver text", func(t *testing.T) {
		for _, failure := range []struct {
			name   string
			marker string
		}{
			// A deadlock is replayed by the governed write before it surfaces.
			{"retryable", "Error 1213: Deadlock found when trying to get lock; try restarting transaction: INSERT INTO users"},
			{"not retryable", "Error 1054: Unknown column 'x' in 'field list': INSERT INTO users"},
		} {
			for _, route := range userWriteRoutes() {
				t.Run(failure.name+" "+route.name, func(t *testing.T) {
					stack := newUserWriteStack(t)
					before := stack.userCount()
					stack.failUserInsert = failure.marker

					rec := route.send(stack, stack.email("failed"), validPassword)

					assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
					apiError := userWriteError(t, rec)
					assert.Equal(t, "INTERNAL_ERROR", apiError.Code)
					assert.Equal(t, "An unexpected error occurred", apiError.Message)
					assert.Nil(t, apiError.Details)
					for _, leak := range []string{"1213", "Deadlock", "1054", "Unknown column", "INSERT", "users",
						"abandoned", "attempts"} {
						assert.NotContains(t, rec.Body.String(), leak)
					}

					// The detail is in the log: one error-level entry of the
					// handler, and the failure is no longer logged as a warning.
					assert.Equal(t, []logrus.Level{logrus.ErrorLevel}, stack.handlerLogLevels(route.handler))

					stack.failUserInsert = ""
					assert.Equal(t, before, stack.userCount(), "nothing was stored")
				})
			}
		}
	})

	t.Run("a duplicate email stays 409 CONFLICT", func(t *testing.T) {
		stack := newUserWriteStack(t)
		for _, route := range userWriteRoutes() {
			email := stack.email("taken")
			require.Equal(t, http.StatusCreated, route.send(stack, email, validPassword).Code, route.name)
			before := stack.userCount()

			rec := route.send(stack, email, validPassword)

			assert.Equal(t, http.StatusConflict, rec.Code, "%s: %s", route.name, rec.Body.String())
			apiError := userWriteError(t, rec)
			assert.Equal(t, "CONFLICT", apiError.Code, route.name)
			assert.Equal(t, "user with this email already exists", apiError.Message, route.name)
			assert.Equal(t, []logrus.Level{logrus.WarnLevel}, stack.handlerLogLevels(route.handler), route.name)
			assert.Equal(t, before, stack.userCount(), route.name)
		}
	})

	t.Run("a weak password stays 400 BAD_REQUEST", func(t *testing.T) {
		stack := newUserWriteStack(t)
		for _, route := range userWriteRoutes() {
			before := stack.userCount()
			rec := route.send(stack, stack.email("weak"), "weakpassword")

			assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", route.name, rec.Body.String())
			apiError := userWriteError(t, rec)
			assert.Equal(t, "BAD_REQUEST", apiError.Code, route.name)
			assert.Equal(t, "password must contain at least one uppercase letter", apiError.Message, route.name)
			assert.Equal(t, before, stack.userCount(), route.name)
		}
	})
}
