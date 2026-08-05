package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These tests pin down the behaviour of re-using an email address that belongs to
// a soft-deleted user or customer.
//
// The unique indexes idx_users_email / idx_customers_email are NOT scoped to
// deleted_at (and deliberately must not be: in both MySQL and SQLite, NULLs in a
// unique index compare as distinct, so a composite UNIQUE(email, deleted_at)
// would allow unlimited *live* duplicates). A soft-deleted row therefore keeps
// its email address reserved at the database level while GORM's default scope
// hides it from ordinary queries.
//
// The requirement these tests enforce is that the mismatch surfaces as the
// ErrDuplicateEmail sentinel — never as a raw driver error string containing the
// index name, the driver error code, or "Duplicate entry" / "UNIQUE constraint
// failed".
//
// Note on scope: deleting through the repository is now a GDPR erasure, which
// replaces the address with a placeholder and therefore FREES it for re-use
// (erasure_test.go covers that). The rows described here are LEGACY rows —
// soft-deleted under the old behaviour, still holding their original address —
// which is why every case below builds that state explicitly via
// legacySoftDelete rather than by calling Delete. Those rows exist in real
// databases until the remediation script in scripts/ is run, so the pre-check
// and the driver-error wrapping stay load-bearing for them.

// driverErrorFragments are strings that must never reach an API client.
var driverErrorFragments = []string{
	"Duplicate entry",
	"UNIQUE constraint failed",
	"idx_users_email",
	"idx_customers_email",
	"Error 1062",
	"1062",
	"sqlite3",
}

func assertNoDriverInternals(t *testing.T, body string) {
	t.Helper()
	for _, fragment := range driverErrorFragments {
		assert.NotContains(t, body, fragment,
			"response body leaked database internals: %q", fragment)
	}
}

func setupEmailReuseDB(t *testing.T) *gorm.DB {
	db := setupDB(t)
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Customer{}))
	return db
}

// legacySoftDelete reproduces a row that was soft-deleted BEFORE deletion became
// an erasure: flagged deleted_at, but still holding its original email and so
// still occupying the unique index.
//
// Going through the repository would no longer produce that state, because the
// address is anonymised on the way out. This helper is the only honest way to
// set up the legacy scenario these tests exist to cover.
func legacySoftDelete(t *testing.T, db *gorm.DB, model interface{}, id uint) {
	t.Helper()
	require.NoError(t, db.Delete(model, id).Error)
}

// --- User: service layer -----------------------------------------------------

func TestRegisterRejectsEmailOfSoftDeletedUser(t *testing.T) {
	db := setupEmailReuseDB(t)
	userRepo := repository.NewUserRepository(db)
	userService := service.NewUserService(userRepo)

	original := &models.User{
		Email:     "alice@example.com",
		FirstName: "Alice",
		LastName:  "Original",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	require.NoError(t, userService.Register(original, "Str0ng!Passw0rd"))
	require.NotZero(t, original.ID)

	// Alice's account was soft-deleted under the old behaviour.
	legacySoftDelete(t, db, &models.User{}, original.ID)

	// Sanity check: the scoped lookup no longer sees the row, which is exactly
	// why the naive pre-check used to wave the second registration through.
	_, err := userRepo.GetByEmail("alice@example.com")
	require.Error(t, err)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound))

	// ...but the unscoped lookup does, and the DB unique index still enforces it.
	soft, err := userRepo.GetByEmailUnscoped("alice@example.com")
	require.NoError(t, err)
	require.Equal(t, original.ID, soft.ID)
	require.True(t, soft.DeletedAt.Valid)

	// Alice registers again.
	replacement := &models.User{
		Email:     "alice@example.com",
		FirstName: "Alice",
		LastName:  "Again",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	err = userService.Register(replacement, "Str0ng!Passw0rd")

	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrDuplicateEmail),
		"expected ErrDuplicateEmail sentinel, got %v", err)
	assertNoDriverInternals(t, err.Error())
	assert.Zero(t, replacement.ID, "no row should have been inserted")
}

func TestRegisterStillRejectsLiveDuplicateEmail(t *testing.T) {
	db := setupEmailReuseDB(t)
	userRepo := repository.NewUserRepository(db)
	userService := service.NewUserService(userRepo)

	first := &models.User{
		Email:     "live@example.com",
		FirstName: "Live",
		LastName:  "One",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	require.NoError(t, userService.Register(first, "Str0ng!Passw0rd"))

	second := &models.User{
		Email:     "live@example.com",
		FirstName: "Live",
		LastName:  "Two",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	err := userService.Register(second, "Str0ng!Passw0rd")

	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrDuplicateEmail))
	assertNoDriverInternals(t, err.Error())

	// Guard against a "fix" that loosens uniqueness for live rows.
	var count int64
	require.NoError(t, db.Model(&models.User{}).Where("email = ?", "live@example.com").Count(&count).Error)
	assert.Equal(t, int64(1), count, "exactly one live user may hold an email")
}

// --- User: repository layer (defense in depth) -------------------------------

// The service pre-check is not the only guard: a race between two registrations
// can slip past it. The repository must translate the driver's duplicate-key
// violation into the sentinel too, so bypass the service entirely here.
func TestUserRepositoryCreateWrapsDuplicateKeyAsSentinel(t *testing.T) {
	db := setupEmailReuseDB(t)
	userRepo := repository.NewUserRepository(db)

	t.Run("soft-deleted row holds the email", func(t *testing.T) {
		seed := &models.User{Email: "repo-soft@example.com", FirstName: "A", LastName: "B", Password: "x", Role: models.RoleCustomer}
		require.NoError(t, userRepo.Create(seed))
		legacySoftDelete(t, db, &models.User{}, seed.ID)

		err := userRepo.Create(&models.User{
			Email: "repo-soft@example.com", FirstName: "C", LastName: "D", Password: "x", Role: models.RoleCustomer,
		})
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrDuplicateEmail),
			"repository must wrap the driver duplicate-key error, got %v", err)
		assertNoDriverInternals(t, err.Error())
	})

	t.Run("live row holds the email", func(t *testing.T) {
		seed := &models.User{Email: "repo-live@example.com", FirstName: "A", LastName: "B", Password: "x", Role: models.RoleCustomer}
		require.NoError(t, userRepo.Create(seed))

		err := userRepo.Create(&models.User{
			Email: "repo-live@example.com", FirstName: "C", LastName: "D", Password: "x", Role: models.RoleCustomer,
		})
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrDuplicateEmail))
		assertNoDriverInternals(t, err.Error())
	})

	t.Run("non-duplicate errors are passed through untouched", func(t *testing.T) {
		// A missing table is not a duplicate-key violation and must not be
		// mislabelled as one by the error classifier.
		bare := repository.NewUserRepository(setupDB(t)) // no AutoMigrate
		err := bare.Create(&models.User{
			Email: "no-table@example.com", FirstName: "A", LastName: "B", Password: "x", Role: models.RoleCustomer,
		})
		require.Error(t, err)
		assert.False(t, errors.Is(err, apperrors.ErrDuplicateEmail),
			"a missing-table error must not be reported as a duplicate email: %v", err)
	})
}

// --- User: handler layer -----------------------------------------------------

func newRegisterRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{
		Secret:      "test-secret-key-at-least-32-characters-long",
		ExpiryHours: 24,
	}
	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	authService := service.NewAuthService(userRepo, apiKeyRepo, jwtCfg)
	userService := service.NewUserService(userRepo)
	authHandler := handler.NewAuthHandler(authService, userService)

	r := gin.New()
	r.POST("/api/v1/auth/register", authHandler.Register)
	return r
}

func TestRegisterHandlerReturns409ForSoftDeletedEmail(t *testing.T) {
	db := setupEmailReuseDB(t)
	require.NoError(t, db.AutoMigrate(&models.APIKey{}))
	router := newRegisterRouter(db)

	body := map[string]string{
		"email":      "handler-alice@example.com",
		"password":   "Str0ng!Passw0rd",
		"first_name": "Alice",
		"last_name":  "Original",
	}

	first := doJSON(t, router, http.MethodPost, "/api/v1/auth/register", body)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())

	// Soft-delete the account.
	userRepo := repository.NewUserRepository(db)
	existing, err := userRepo.GetByEmail("handler-alice@example.com")
	require.NoError(t, err)
	legacySoftDelete(t, db, &models.User{}, existing.ID)

	// Re-register the same address.
	second := doJSON(t, router, http.MethodPost, "/api/v1/auth/register", body)

	assert.Equal(t, http.StatusConflict, second.Code,
		"re-registering a soft-deleted email must be a 409 conflict, body=%s", second.Body.String())
	assertNoDriverInternals(t, second.Body.String())
	assert.Contains(t, second.Body.String(), "user with this email already exists")
}

func TestRegisterHandlerReturns409ForLiveDuplicateEmail(t *testing.T) {
	db := setupEmailReuseDB(t)
	require.NoError(t, db.AutoMigrate(&models.APIKey{}))
	router := newRegisterRouter(db)

	body := map[string]string{
		"email":      "handler-live@example.com",
		"password":   "Str0ng!Passw0rd",
		"first_name": "Live",
		"last_name":  "User",
	}

	first := doJSON(t, router, http.MethodPost, "/api/v1/auth/register", body)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())

	second := doJSON(t, router, http.MethodPost, "/api/v1/auth/register", body)
	assert.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	assertNoDriverInternals(t, second.Body.String())
}

// --- Customer: service layer -------------------------------------------------

func TestCustomerCreateRejectsEmailOfSoftDeletedCustomer(t *testing.T) {
	db := setupEmailReuseDB(t)
	customerRepo := repository.NewCustomerRepository(db)
	userRepo := repository.NewUserRepository(db)
	customerService := service.NewCustomerService(customerRepo, userRepo)

	original := &models.Customer{FirstName: "Bob", LastName: "Original", Email: "bob@example.com"}
	require.NoError(t, customerService.Create(original))
	require.NotZero(t, original.ID)

	legacySoftDelete(t, db, &models.Customer{}, original.ID)

	_, err := customerRepo.GetByEmail("bob@example.com")
	require.Error(t, err)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound))

	replacement := &models.Customer{FirstName: "Bob", LastName: "Again", Email: "bob@example.com"}
	err = customerService.Create(replacement)

	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrDuplicateEmail),
		"expected ErrDuplicateEmail sentinel, got %v", err)
	assertNoDriverInternals(t, err.Error())
	assert.Zero(t, replacement.ID)
}

func TestCustomerCreateStillRejectsLiveDuplicateEmail(t *testing.T) {
	db := setupEmailReuseDB(t)
	customerRepo := repository.NewCustomerRepository(db)
	userRepo := repository.NewUserRepository(db)
	customerService := service.NewCustomerService(customerRepo, userRepo)

	require.NoError(t, customerService.Create(&models.Customer{
		FirstName: "Live", LastName: "One", Email: "live-customer@example.com",
	}))

	err := customerService.Create(&models.Customer{
		FirstName: "Live", LastName: "Two", Email: "live-customer@example.com",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrDuplicateEmail))
	assertNoDriverInternals(t, err.Error())

	var count int64
	require.NoError(t, db.Model(&models.Customer{}).Where("email = ?", "live-customer@example.com").Count(&count).Error)
	assert.Equal(t, int64(1), count, "exactly one live customer may hold an email")
}

// --- Customer: repository layer (defense in depth) ---------------------------

func TestCustomerRepositoryCreateWrapsDuplicateKeyAsSentinel(t *testing.T) {
	db := setupEmailReuseDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	seed := &models.Customer{FirstName: "A", LastName: "B", Email: "repo-customer@example.com"}
	require.NoError(t, customerRepo.Create(seed))
	legacySoftDelete(t, db, &models.Customer{}, seed.ID)

	err := customerRepo.Create(&models.Customer{
		FirstName: "C", LastName: "D", Email: "repo-customer@example.com",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrDuplicateEmail),
		"repository must wrap the driver duplicate-key error, got %v", err)
	assertNoDriverInternals(t, err.Error())
}

// --- Customer: handler layer -------------------------------------------------

func TestCreateCustomerHandlerDoesNotLeakDriverErrorForSoftDeletedEmail(t *testing.T) {
	db := setupEmailReuseDB(t)

	customerRepo := repository.NewCustomerRepository(db)
	userRepo := repository.NewUserRepository(db)
	customerService := service.NewCustomerService(customerRepo, userRepo)
	customerHandler := handler.NewCustomerHandler(customerService)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	// Skip the JWT machinery; CustomerHandler.Create only reads user_role.
	router.POST("/api/v1/customers", func(c *gin.Context) {
		c.Set("user_role", string(models.RoleAdmin))
		c.Next()
	}, customerHandler.Create)

	body := map[string]string{
		"first_name": "Bob",
		"last_name":  "Original",
		"email":      "handler-bob@example.com",
	}

	first := doJSON(t, router, http.MethodPost, "/api/v1/customers", body)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())

	existing, err := customerRepo.GetByEmail("handler-bob@example.com")
	require.NoError(t, err)
	legacySoftDelete(t, db, &models.Customer{}, existing.ID)

	second := doJSON(t, router, http.MethodPost, "/api/v1/customers", body)

	// Before the fix this fell into the handler's generic branch. It must now be
	// classified as a duplicate-email conflict, consistent with AuthHandler and
	// UserHandler.
	assert.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	assert.NotEqual(t, http.StatusInternalServerError, second.Code)
	assert.Contains(t, second.Body.String(), "customer with this email already exists")
	assertNoDriverInternals(t, second.Body.String())
}

// --- helpers -----------------------------------------------------------------

func doJSON(t *testing.T, router *gin.Engine, method, path string, payload interface{}) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// Guard against a body that is not JSON at all.
	require.True(t, strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "{"),
		"expected a JSON response body, got %q", rec.Body.String())
	return rec
}
