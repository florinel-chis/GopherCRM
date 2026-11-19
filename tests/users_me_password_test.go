package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
)

const (
	usersMeOldPassword      = "Password123!"
	usersMeSuppliedPassword = "Supplied!Pass254"
	usersMeWrongCurrent     = "Wrong!Current254"
	usersMeRefusalCode      = "PASSWORD_FIELD_NOT_ACCEPTED"
	usersMeRefusalMessage   = "This route does not accept password fields; use POST /auth/change-password"
)

// usersMeStack is the real route, middleware, services and repositories on
// SQLite, with the auth routes mounted by the test.
type usersMeStack struct {
	db          *gorm.DB
	router      *gin.Engine
	userService service.UserService
	apiKeys     service.APIKeyService
}

func newUsersMeStack(t *testing.T) *usersMeStack {
	t.Helper()
	return buildUsersMeStack(t, false)
}

// newGovernedUsersMeStack is the same stack on the whole schema, with the user
// service wired as the server wires it: role governance and its audit log.
func newGovernedUsersMeStack(t *testing.T) *usersMeStack {
	t.Helper()
	return buildUsersMeStack(t, true)
}

func buildUsersMeStack(t *testing.T, governed bool) *usersMeStack {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if governed {
		require.NoError(t, models.MigrateDatabaseWith(db))
	} else {
		require.NoError(t, db.AutoMigrate(
			&models.User{}, &models.APIKey{},
			&models.RefreshToken{}, &models.PasswordResetToken{},
		))
	}

	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	authService := service.NewAuthServiceWithSessions(
		userRepo, apiKeyRepo, repository.NewRefreshTokenRepository(db),
		repository.NewPasswordResetTokenRepository(db), utils.NewTransactionManager(db), &capturingMailer{},
		config.JWTConfig{Secret: "users-me-test-secret", ExpiryHours: 1, RefreshTokenDays: 30},
		"http://localhost:5173", "")
	userService := service.NewUserService(userRepo)
	if governed {
		userService = service.NewUserService(userRepo, service.WithUserRoleGovernance(
			repository.NewRoleRepository(db), repository.NewRoleAuditRepository(db), utils.NewTransactionManager(db)))
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.ErrorHandler())

	authHandler := handler.NewAuthHandler(authService, userService)
	api := router.Group("/api/v1")
	api.POST("/auth/login", authHandler.Login)
	api.POST("/auth/refresh", authHandler.Refresh)

	protected := api.Group("")
	protected.Use(middleware.Auth(authService))
	protected.POST("/auth/change-password", authHandler.ChangePassword)
	handler.SetupUserRoutes(protected, handler.NewUserHandler(userService))

	return &usersMeStack{
		db:          db,
		router:      router,
		userService: userService,
		apiKeys:     service.NewAPIKeyService(apiKeyRepo, userRepo, utils.NewTransactionManager(db), ""),
	}
}

func (s *usersMeStack) do(method, path, authorization, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	return rec
}

func (s *usersMeStack) putMe(authorization, body string) *httptest.ResponseRecorder {
	return s.do(http.MethodPut, "/api/v1/users/me", authorization, body)
}

func (s *usersMeStack) register(t *testing.T, label string, role models.UserRole) *models.User {
	t.Helper()
	user := &models.User{
		Email:     label + "@example.com",
		FirstName: "Pilot",
		LastName:  "User",
		Role:      role,
		IsActive:  true,
	}
	require.NoError(t, s.userService.Register(user, usersMeOldPassword))
	return user
}

func (s *usersMeStack) loginStatus(email, password string) int {
	return s.do(http.MethodPost, "/api/v1/auth/login", "",
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)).Code
}

func usersMeData(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	data, ok := response.Data.(map[string]interface{})
	require.True(t, ok, rec.Body.String())
	return data
}

// login returns the access and the refresh token of a new session.
func (s *usersMeStack) login(t *testing.T, email, password string) (string, string) {
	t.Helper()
	rec := s.do(http.MethodPost, "/api/v1/auth/login", "",
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, password))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := usersMeData(t, rec)
	access, _ := data["token"].(string)
	refresh, _ := data["refresh_token"].(string)
	require.NotEmpty(t, access)
	require.NotEmpty(t, refresh)
	return access, refresh
}

func (s *usersMeStack) refreshStatus(refreshToken string) *httptest.ResponseRecorder {
	return s.do(http.MethodPost, "/api/v1/auth/refresh", "",
		fmt.Sprintf(`{"refresh_token":%q}`, refreshToken))
}

// rotate proves a refresh token is still live and returns its successor.
func (s *usersMeStack) rotate(t *testing.T, refreshToken string) string {
	t.Helper()
	rec := s.refreshStatus(refreshToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	successor, _ := usersMeData(t, rec)["refresh_token"].(string)
	require.NotEmpty(t, successor)
	require.NotEqual(t, refreshToken, successor)
	return successor
}

func (s *usersMeStack) storedHash(t *testing.T, id uint) string {
	t.Helper()
	var hash string
	require.NoError(t, s.db.Raw("SELECT password FROM users WHERE id = ?", id).Scan(&hash).Error)
	require.NotEmpty(t, hash)
	return hash
}

// authorization answers the header of one of the two credential kinds.
func (s *usersMeStack) authorization(t *testing.T, credential string, user *models.User, access string) string {
	t.Helper()
	if credential == "jwt" {
		return "Bearer " + access
	}
	rawKey, _, err := s.apiKeys.Generate(user.ID, "users-me-test", nil)
	require.NoError(t, err)
	return "ApiKey " + rawKey
}

func assertUsersMeRefused(t *testing.T, rec *httptest.ResponseRecorder, supplied ...string) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	assert.False(t, response.Success)
	require.NotNil(t, response.Error)
	assert.Equal(t, usersMeRefusalCode, response.Error.Code)
	assert.Equal(t, usersMeRefusalMessage, response.Error.Message)
	assert.Nil(t, response.Error.Details)
	for _, value := range supplied {
		assert.NotContains(t, rec.Body.String(), value)
	}
}

// assertPasswordUntouched checks the stored hash, then that the old password
// logs in and the supplied one does not.
func (s *usersMeStack) assertPasswordUntouched(t *testing.T, user *models.User, hash string) {
	t.Helper()
	assert.Equal(t, hash, s.storedHash(t, user.ID), "the stored hash must be byte-identical")
	assert.Equal(t, http.StatusOK, s.loginStatus(user.Email, usersMeOldPassword), "the old password must log in")
	assert.Equal(t, http.StatusUnauthorized, s.loginStatus(user.Email, usersMeSuppliedPassword),
		"the supplied password must not log in")
}

// Against 93f23c5 this test answers 200 and the password changes: that red run
// is the confirmation of the defect in issue #254.
func TestUpdateMePasswordRefusedWithoutCurrentPassword(t *testing.T) {
	stack := newUsersMeStack(t)

	for _, credential := range []string{"jwt", "api_key"} {
		for _, role := range []models.UserRole{
			models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer,
		} {
			t.Run(credential+"/"+string(role), func(t *testing.T) {
				user := stack.register(t, "missing-"+strings.ReplaceAll(credential, "_", "-")+"-"+string(role), role)
				access, refresh := stack.login(t, user.Email, usersMeOldPassword)
				authorization := stack.authorization(t, credential, user, access)
				hash := stack.storedHash(t, user.ID)

				rec := stack.putMe(authorization, fmt.Sprintf(`{"password":%q}`, usersMeSuppliedPassword))

				assertUsersMeRefused(t, rec, usersMeSuppliedPassword)
				stack.assertPasswordUntouched(t, user, hash)
				stack.rotate(t, refresh)
			})
		}
	}
}

func TestUpdateMePasswordRefusedWithCurrentPassword(t *testing.T) {
	stack := newUsersMeStack(t)

	for _, credential := range []string{"jwt", "api_key"} {
		t.Run(credential, func(t *testing.T) {
			user := stack.register(t, "current-"+strings.ReplaceAll(credential, "_", "-"), models.RoleSales)
			access, refresh := stack.login(t, user.Email, usersMeOldPassword)
			authorization := stack.authorization(t, credential, user, access)
			hash := stack.storedHash(t, user.ID)

			wrong := stack.putMe(authorization, fmt.Sprintf(`{"password":%q,"current_password":%q}`,
				usersMeSuppliedPassword, usersMeWrongCurrent))
			assertUsersMeRefused(t, wrong, usersMeSuppliedPassword, usersMeWrongCurrent)
			assert.Equal(t, hash, stack.storedHash(t, user.ID))

			// A correct current password does not open this route.
			correct := stack.putMe(authorization, fmt.Sprintf(`{"password":%q,"current_password":%q}`,
				usersMeSuppliedPassword, usersMeOldPassword))
			assertUsersMeRefused(t, correct, usersMeSuppliedPassword, usersMeOldPassword)

			stack.assertPasswordUntouched(t, user, hash)
			stack.rotate(t, refresh)
		})
	}
}

func TestUpdateMeRefusalWritesNoProfileField(t *testing.T) {
	stack := newUsersMeStack(t)
	user := stack.register(t, "no-write", models.RoleSupport)
	access, _ := stack.login(t, user.Email, usersMeOldPassword)

	// Move updated_at away from now, so that any write would be visible in it.
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, stack.db.Model(&models.User{}).Where("id = ?", user.ID).
		UpdateColumn("updated_at", past).Error)
	var before models.User
	require.NoError(t, stack.db.First(&before, user.ID).Error)

	rec := stack.putMe("Bearer "+access, fmt.Sprintf(
		`{"email":"changed@example.com","first_name":"Changed","password":%q}`, usersMeSuppliedPassword))
	assertUsersMeRefused(t, rec, usersMeSuppliedPassword)

	var after models.User
	require.NoError(t, stack.db.First(&after, user.ID).Error)
	assert.Equal(t, before.Email, after.Email)
	assert.Equal(t, before.FirstName, after.FirstName)
	assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt),
		"updated_at moved from %s to %s", before.UpdatedAt, after.UpdatedAt)

	// The control: the same body without the refused key does write. The email
	// changes, so it carries the current password (#394).
	rec = stack.putMe("Bearer "+access, fmt.Sprintf(
		`{"email":"changed@example.com","first_name":"Changed","current_password":%q}`, usersMeOldPassword))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, stack.db.First(&after, user.ID).Error)
	assert.Equal(t, "changed@example.com", after.Email)
	assert.Equal(t, "Changed", after.FirstName)
	assert.False(t, before.UpdatedAt.Equal(after.UpdatedAt))
}

func TestChangePasswordRemainsTheSessionContract(t *testing.T) {
	const newPassword = "NewSecret!254a"

	stack := newUsersMeStack(t)
	user := stack.register(t, "change-contract", models.RoleSales)
	access, refresh := stack.login(t, user.Email, usersMeOldPassword)
	authorization := "Bearer " + access

	refusals := []struct {
		name string
		body string
		code string
	}{
		{"missing current password", fmt.Sprintf(`{"new_password":%q}`, newPassword), utils.ErrCodeValidation},
		{
			"wrong current password",
			fmt.Sprintf(`{"current_password":%q,"new_password":%q}`, usersMeWrongCurrent, newPassword),
			utils.ErrCodeBadRequest,
		},
	}
	for _, refusal := range refusals {
		rec := stack.do(http.MethodPost, "/api/v1/auth/change-password", authorization, refusal.body)
		require.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", refusal.name, rec.Body.String())
		var response utils.APIResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		require.NotNil(t, response.Error)
		assert.Equal(t, refusal.code, response.Error.Code, refusal.name)

		// The password and the refresh token are both still valid.
		assert.Equal(t, http.StatusOK, stack.loginStatus(user.Email, usersMeOldPassword), refusal.name)
		assert.Equal(t, http.StatusUnauthorized, stack.loginStatus(user.Email, newPassword), refusal.name)
		refresh = stack.rotate(t, refresh)
	}

	// A second session that never rotated must lose renewal as well.
	_, otherRefresh := stack.login(t, user.Email, usersMeOldPassword)

	rec := stack.do(http.MethodPost, "/api/v1/auth/change-password", authorization,
		fmt.Sprintf(`{"current_password":%q,"new_password":%q}`, usersMeOldPassword, newPassword))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, http.StatusOK, stack.loginStatus(user.Email, newPassword), "the new password must log in")
	assert.Equal(t, http.StatusUnauthorized, stack.loginStatus(user.Email, usersMeOldPassword),
		"the old password must not log in")
	assert.Equal(t, http.StatusUnauthorized, stack.refreshStatus(refresh).Code,
		"a refresh token issued before the change must be refused")
	assert.Equal(t, http.StatusUnauthorized, stack.refreshStatus(otherRefresh).Code,
		"another session's refresh token must be refused")
}

func TestUpdateUserByIDDoesNotChangePassword(t *testing.T) {
	stack := newUsersMeStack(t)
	body := fmt.Sprintf(`{"first_name":"Renamed","password":%q}`, usersMeSuppliedPassword)

	put := func(t *testing.T, user *models.User, access string) {
		t.Helper()
		hash := stack.storedHash(t, user.ID)

		rec := stack.do(http.MethodPut, fmt.Sprintf("/api/v1/users/%d", user.ID), "Bearer "+access, body)

		// The key is ignored, not refused: the rest of the body still applies.
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "Renamed", usersMeData(t, rec)["first_name"])
		assert.NotContains(t, rec.Body.String(), usersMeSuppliedPassword)
		stack.assertPasswordUntouched(t, user, hash)
	}

	t.Run("the account itself", func(t *testing.T) {
		user := stack.register(t, "by-id-self", models.RoleCustomer)
		access, _ := stack.login(t, user.Email, usersMeOldPassword)
		put(t, user, access)
	})

	t.Run("an admin", func(t *testing.T) {
		admin := stack.register(t, "by-id-admin", models.RoleAdmin)
		target := stack.register(t, "by-id-target", models.RoleSales)
		access, _ := stack.login(t, admin.Email, usersMeOldPassword)
		put(t, target, access)
	})
}

const (
	ownEmailRequiredCode     = "CURRENT_PASSWORD_REQUIRED"
	ownEmailRequiredMessage  = "Changing your email requires your current password"
	ownEmailIncorrectCode    = "INVALID_CURRENT_PASSWORD"
	ownEmailIncorrectMessage = "The current password is incorrect"
)

// ownEmailCredentials are the two credential kinds of an account, by the name
// of their subtest and the kind the authorization helper takes.
var ownEmailCredentials = []struct{ name, kind string }{{"bearer", "jwt"}, {"api-key", "api_key"}}

// ownEmailRoutes are the two routes an account updates its own profile through.
var ownEmailRoutes = []struct {
	name string
	path func(user *models.User) string
}{
	{"users-me", func(*models.User) string { return "/api/v1/users/me" }},
	{"users-id", func(user *models.User) string { return fmt.Sprintf("/api/v1/users/%d", user.ID) }},
}

// ageUser moves updated_at into the past, so that any write shows in it, and
// answers the row as stored.
func (s *usersMeStack) ageUser(t *testing.T, id uint) models.User {
	t.Helper()
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, s.db.Model(&models.User{}).Where("id = ?", id).UpdateColumn("updated_at", past).Error)
	return s.storedUser(t, id)
}

func (s *usersMeStack) storedUser(t *testing.T, id uint) models.User {
	t.Helper()
	var user models.User
	require.NoError(t, s.db.First(&user, id).Error)
	return user
}

// assertUserUnwritten checks that no column the routes write has moved.
func (s *usersMeStack) assertUserUnwritten(t *testing.T, before models.User, hash string) {
	t.Helper()
	after := s.storedUser(t, before.ID)
	assert.Equal(t, before.Email, after.Email)
	assert.Equal(t, before.FirstName, after.FirstName)
	assert.Equal(t, before.LastName, after.LastName)
	assert.Equal(t, before.Role, after.Role)
	assert.Equal(t, before.IsActive, after.IsActive)
	assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt),
		"updated_at moved from %s to %s", before.UpdatedAt, after.UpdatedAt)
	assert.Equal(t, hash, s.storedHash(t, before.ID), "the stored hash must be byte-identical")
}

func assertOwnEmailRefused(t *testing.T, rec *httptest.ResponseRecorder, code, message string) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	assert.False(t, response.Success)
	require.NotNil(t, response.Error)
	assert.Equal(t, code, response.Error.Code)
	assert.Equal(t, message, response.Error.Message)
	assert.Nil(t, response.Error.Details)
	assert.NotContains(t, rec.Body.String(), usersMeWrongCurrent)
	assert.NotContains(t, rec.Body.String(), usersMeOldPassword)
}

// captureUsersMeLog sends the shared logger to a buffer at debug level.
func captureUsersMeLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var output bytes.Buffer
	utils.Logger.SetOutput(&output)
	t.Cleanup(func() { utils.Logger.SetOutput(os.Stdout) })
	return &output
}

func (s *usersMeStack) auditRows(t *testing.T) int64 {
	t.Helper()
	var count int64
	require.NoError(t, s.db.Model(&models.RoleAuditEntry{}).Count(&count).Error)
	return count
}

// On main the first request of every subtest answers 200 and the email moves:
// that is the defect of issue #394.
func TestOwnEmailChangeNeedsCurrentPassword(t *testing.T) {
	stack := newGovernedUsersMeStack(t)
	logged := captureUsersMeLog(t)
	taken := stack.register(t, "own-email-taken", models.RoleSales)

	for _, route := range ownEmailRoutes {
		for _, credential := range ownEmailCredentials {
			t.Run(route.name+"/"+credential.name, func(t *testing.T) {
				label := "own-email-" + route.name + "-" + credential.name
				user := stack.register(t, label, models.RoleSales)
				access, _ := stack.login(t, user.Email, usersMeOldPassword)
				authorization := stack.authorization(t, credential.kind, user, access)
				path := route.path(user)
				newEmail := label + "-new@example.com"
				before := stack.ageUser(t, user.ID)
				hash := stack.storedHash(t, user.ID)

				refusals := []struct {
					name    string
					body    string
					code    string
					message string
				}{
					{
						"missing",
						fmt.Sprintf(`{"email":%q,"first_name":"Changed"}`, newEmail),
						ownEmailRequiredCode, ownEmailRequiredMessage,
					},
					{
						"null",
						fmt.Sprintf(`{"email":%q,"first_name":"Changed","current_password":null}`, newEmail),
						ownEmailRequiredCode, ownEmailRequiredMessage,
					},
					{
						"empty",
						fmt.Sprintf(`{"email":%q,"first_name":"Changed","current_password":""}`, newEmail),
						ownEmailRequiredCode, ownEmailRequiredMessage,
					},
					{
						"wrong",
						fmt.Sprintf(`{"email":%q,"first_name":"Changed","current_password":%q}`, newEmail, usersMeWrongCurrent),
						ownEmailIncorrectCode, ownEmailIncorrectMessage,
					},
					{
						// The guard answers before the address is looked up, so a
						// credential without the password learns nothing about it.
						"missing with an address in use",
						fmt.Sprintf(`{"email":%q}`, taken.Email),
						ownEmailRequiredCode, ownEmailRequiredMessage,
					},
					{
						"wrong with an address in use",
						fmt.Sprintf(`{"email":%q,"current_password":%q}`, taken.Email, usersMeWrongCurrent),
						ownEmailIncorrectCode, ownEmailIncorrectMessage,
					},
				}
				for _, refusal := range refusals {
					t.Run(refusal.name, func(t *testing.T) {
						rec := stack.do(http.MethodPut, path, authorization, refusal.body)
						assertOwnEmailRefused(t, rec, refusal.code, refusal.message)
						stack.assertUserUnwritten(t, before, hash)
					})
				}

				t.Run("right", func(t *testing.T) {
					rec := stack.do(http.MethodPut, path, authorization, fmt.Sprintf(
						`{"email":%q,"first_name":"Changed","current_password":%q}`, newEmail, usersMeOldPassword))
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					assert.Equal(t, newEmail, usersMeData(t, rec)["email"])
					assert.NotContains(t, rec.Body.String(), usersMeOldPassword)

					after := stack.storedUser(t, user.ID)
					assert.Equal(t, newEmail, after.Email)
					assert.Equal(t, "Changed", after.FirstName)
					assert.False(t, before.UpdatedAt.Equal(after.UpdatedAt))
					// The password is proof only: it is not rewritten and still logs in.
					assert.Equal(t, hash, stack.storedHash(t, user.ID))
					assert.Equal(t, http.StatusOK, stack.loginStatus(newEmail, usersMeOldPassword))
				})

				t.Run("right with an address in use", func(t *testing.T) {
					rec := stack.do(http.MethodPut, path, authorization, fmt.Sprintf(
						`{"email":%q,"current_password":%q}`, taken.Email, usersMeOldPassword))
					require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
					assert.Equal(t, newEmail, stack.storedUser(t, user.ID).Email)
				})
			})
		}
	}

	// PUT /users/me refuses password and new_password first, also next to a new
	// email and the correct current password.
	t.Run("users-me/password fields stay refused", func(t *testing.T) {
		user := stack.register(t, "own-email-refused-keys", models.RoleSupport)
		access, _ := stack.login(t, user.Email, usersMeOldPassword)
		before := stack.ageUser(t, user.ID)
		hash := stack.storedHash(t, user.ID)

		for _, key := range []string{"password", "new_password"} {
			rec := stack.putMe("Bearer "+access, fmt.Sprintf(
				`{"email":"own-email-refused-keys-new@example.com","current_password":%q,%q:%q}`,
				usersMeOldPassword, key, usersMeSuppliedPassword))
			assertUsersMeRefused(t, rec, usersMeSuppliedPassword, usersMeOldPassword)
			stack.assertUserUnwritten(t, before, hash)
		}
	})

	// An admin's own-account body that also carries role or is_active goes to
	// the governed transaction, and is guarded before it.
	for _, credential := range ownEmailCredentials {
		t.Run("users-id/governed/"+credential.name, func(t *testing.T) {
			label := "own-email-governed-" + credential.name
			admin := stack.register(t, label, models.RoleAdmin)
			access, _ := stack.login(t, admin.Email, usersMeOldPassword)
			authorization := stack.authorization(t, credential.kind, admin, access)
			path := fmt.Sprintf("/api/v1/users/%d", admin.ID)
			newEmail := label + "-new@example.com"
			before := stack.ageUser(t, admin.ID)
			hash := stack.storedHash(t, admin.ID)
			audited := stack.auditRows(t)

			for name, fields := range map[string]string{
				"role":      `"role":"sales"`,
				"is_active": `"is_active":true`,
				"both":      `"role":"admin","is_active":false`,
			} {
				t.Run(name+"/missing", func(t *testing.T) {
					rec := stack.do(http.MethodPut, path, authorization, fmt.Sprintf(`{"email":%q,%s}`, newEmail, fields))
					assertOwnEmailRefused(t, rec, ownEmailRequiredCode, ownEmailRequiredMessage)
					stack.assertUserUnwritten(t, before, hash)
					assert.Equal(t, audited, stack.auditRows(t), "a refusal writes no audit row")
				})
				t.Run(name+"/wrong", func(t *testing.T) {
					rec := stack.do(http.MethodPut, path, authorization, fmt.Sprintf(
						`{"email":%q,%s,"current_password":%q}`, newEmail, fields, usersMeWrongCurrent))
					assertOwnEmailRefused(t, rec, ownEmailIncorrectCode, ownEmailIncorrectMessage)
					stack.assertUserUnwritten(t, before, hash)
					assert.Equal(t, audited, stack.auditRows(t), "a refusal writes no audit row")
				})
			}

			t.Run("right", func(t *testing.T) {
				rec := stack.do(http.MethodPut, path, authorization, fmt.Sprintf(
					`{"email":%q,"role":"admin","is_active":true,"current_password":%q}`, newEmail, usersMeOldPassword))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				after := stack.storedUser(t, admin.ID)
				assert.Equal(t, newEmail, after.Email)
				assert.Equal(t, models.RoleAdmin, after.Role)
				assert.Equal(t, hash, stack.storedHash(t, admin.ID))
			})
		})
	}

	// Neither password of the requests above reached the log, and the refusals
	// name no email.
	output := logged.String()
	assert.NotContains(t, output, usersMeWrongCurrent)
	assert.NotContains(t, output, usersMeOldPassword)
	assert.NotContains(t, output, usersMeSuppliedPassword)
	refusals := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Refused own email change") {
			refusals++
			assert.NotContains(t, line, "@example.com")
			assert.Contains(t, line, `"level":"warning"`)
		}
	}
	assert.Positive(t, refusals, "every refusal is logged")
}

func TestOwnProfileUpdateWithoutEmailChangeNeedsNoPassword(t *testing.T) {
	stack := newGovernedUsersMeStack(t)

	for _, route := range ownEmailRoutes {
		for _, credential := range ownEmailCredentials {
			t.Run(route.name+"/"+credential.name, func(t *testing.T) {
				user := stack.register(t, "no-change-"+route.name+"-"+credential.name, models.RoleSales)
				access, _ := stack.login(t, user.Email, usersMeOldPassword)
				authorization := stack.authorization(t, credential.kind, user, access)
				path := route.path(user)
				hash := stack.storedHash(t, user.ID)

				steps := []struct {
					name      string
					body      string
					firstName string
				}{
					{"names only", `{"first_name":"Named","last_name":"Only"}`, "Named"},
					{"the stored email again", fmt.Sprintf(`{"email":%q,"first_name":"Same"}`, user.Email), "Same"},
					{
						// The key is ignored, not verified, when the email does not change.
						"a wrong current_password without an email",
						fmt.Sprintf(`{"first_name":"Ignored","current_password":%q}`, usersMeWrongCurrent),
						"Ignored",
					},
					{
						"a wrong current_password with the stored email",
						fmt.Sprintf(`{"email":%q,"first_name":"Stored","current_password":%q}`, user.Email, usersMeWrongCurrent),
						"Stored",
					},
				}
				for _, step := range steps {
					t.Run(step.name, func(t *testing.T) {
						rec := stack.do(http.MethodPut, path, authorization, step.body)
						require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
						assert.NotContains(t, rec.Body.String(), usersMeWrongCurrent)
						after := stack.storedUser(t, user.ID)
						assert.Equal(t, step.firstName, after.FirstName)
						assert.Equal(t, user.Email, after.Email)
						assert.Equal(t, hash, stack.storedHash(t, user.ID))
					})
				}
			})
		}
	}
}

func TestOtherUsersEmailChangeNeedsNoPassword(t *testing.T) {
	stack := newGovernedUsersMeStack(t)

	for _, credential := range ownEmailCredentials {
		t.Run(credential.name, func(t *testing.T) {
			admin := stack.register(t, "other-admin-"+credential.name, models.RoleAdmin)
			target := stack.register(t, "other-target-"+credential.name, models.RoleSales)
			access, _ := stack.login(t, admin.Email, usersMeOldPassword)
			authorization := stack.authorization(t, credential.kind, admin, access)
			path := fmt.Sprintf("/api/v1/users/%d", target.ID)
			hash := stack.storedHash(t, target.ID)

			first := "other-target-" + credential.name + "-first@example.com"
			rec := stack.do(http.MethodPut, path, authorization, fmt.Sprintf(`{"email":%q}`, first))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, first, stack.storedUser(t, target.ID).Email)

			// A current_password in the body is not verified for another account.
			second := "other-target-" + credential.name + "-second@example.com"
			rec = stack.do(http.MethodPut, path, authorization, fmt.Sprintf(
				`{"email":%q,"current_password":%q}`, second, usersMeWrongCurrent))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, second, stack.storedUser(t, target.ID).Email)
			assert.Equal(t, hash, stack.storedHash(t, target.ID))
		})
	}
}

// specObject walks nested objects of the parsed spec by key.
func specObject(t *testing.T, node map[string]interface{}, keys ...string) map[string]interface{} {
	t.Helper()
	for _, key := range keys {
		next, ok := node[key].(map[string]interface{})
		require.True(t, ok, "spec has no object at %q", key)
		node = next
	}
	return node
}

func specDescription(t *testing.T, node map[string]interface{}) string {
	t.Helper()
	description, ok := node["description"].(string)
	require.True(t, ok, "spec object has no description")
	return description
}

func TestAPIReferenceDocumentsUpdateMeRefusal(t *testing.T) {
	const staleRevocationClaim = "a password changed through PUT /users/me revokes none"

	raw, err := os.ReadFile(filepath.Join("..", "api", "swagger.json"))
	require.NoError(t, err)
	var spec map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &spec))

	assert.NotContains(t, specDescription(t, specObject(t, spec, "info")), staleRevocationClaim)

	updateMe := specObject(t, spec, "paths", "/users/me", "put")
	assert.NotContains(t, specDescription(t, updateMe), "current password is not required")
	assert.Contains(t, specDescription(t, updateMe), usersMeRefusalCode)

	refused := specDescription(t, specObject(t, updateMe, "responses", "400"))
	assert.Contains(t, refused, usersMeRefusalCode)
	assert.Contains(t, refused, "POST /auth/change-password")

	failed := specDescription(t, specObject(t, updateMe, "responses", "500"))
	assert.NotContains(t, failed, "72")
	assert.NotContains(t, failed, "bcrypt")

	byIDOperation := specObject(t, spec, "paths", "/users/{id}", "put")
	byID := specDescription(t, byIDOperation)
	assert.NotContains(t, byID, "PUT /users/me")
	assert.Contains(t, byID, "POST /auth/change-password")

	for _, name := range []string{"password", "new_password"} {
		property := specObject(t, spec, "definitions", "internal_handler.UpdateMeRequest", "properties", name)
		assert.Contains(t, specDescription(t, property), usersMeRefusalCode, name)
		assert.NotContains(t, property, "minLength", name)
	}

	// An own email change needs current_password (#394): both routes name the
	// two codes, in the description and in the 400 answer, and both bodies
	// document the field as a string that is no longer refused.
	for route, operation := range map[string]map[string]interface{}{"/users/me": updateMe, "/users/{id}": byIDOperation} {
		badRequest := specDescription(t, specObject(t, operation, "responses", "400"))
		for _, code := range []string{ownEmailRequiredCode, ownEmailIncorrectCode} {
			assert.Contains(t, specDescription(t, operation), code, route)
			assert.Contains(t, badRequest, code, route)
		}
	}
	for _, definition := range []string{"internal_handler.UpdateMeRequest", "internal_handler.UpdateUserRequest"} {
		property := specObject(t, spec, "definitions", definition, "properties", "current_password")
		description := specDescription(t, property)
		assert.Contains(t, description, ownEmailRequiredCode, definition)
		assert.Contains(t, description, ownEmailIncorrectCode, definition)
		assert.NotContains(t, description, usersMeRefusalCode, definition)
		assert.Equal(t, "string", property["type"], definition)
		assert.NotContains(t, property, "minLength", definition)
	}

	// The other two artifacts render the same refusal.
	for _, artifact := range []string{"swagger.yaml", "index.html"} {
		content, err := os.ReadFile(filepath.Join("..", "api", artifact))
		require.NoError(t, err)
		assert.Contains(t, string(content), usersMeRefusalCode, artifact)
		assert.NotContains(t, string(content), staleRevocationClaim, artifact)
	}
}
