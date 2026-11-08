package tests

// Customer external-id mappings (#212) over HTTP on SQLite: the routes of
// SetupCustomerRoutes and SetupCustomerExternalIDRoutes behind the real
// RequestID, Logger, ErrorHandler and JWT Auth middleware, the real
// repositories and services, and the full schema of models.MigrateDatabase
// with foreign keys on. Every outcome is read back from the database.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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

type externalIDAPIFixture struct {
	db     *gorm.DB
	router *gin.Engine
	tokens map[models.UserRole]string
}

func newExternalIDAPIFixture(t *testing.T) *externalIDAPIFixture {
	t.Helper()
	previousLogger := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previousLogger })

	dsn := filepath.Join(t.TempDir(), "external-ids.db") +
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

	userRepo := repository.NewUserRepository(db)
	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: "external-id-api-test-secret-0123456789", ExpiryHours: 1})
	customerService := service.NewCustomerService(repository.NewCustomerRepository(db), userRepo, repository.NewCompanyRepository(db))
	externalIDs := service.NewCustomerExternalIDService(repository.NewCustomerExternalIDRepository(db), utils.NewTransactionManager(db))
	customerHandler := handler.NewCustomerHandler(customerService).WithExternalIDs(externalIDs)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.Logger())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupCustomerRoutes(protected, customerHandler)
	handler.SetupCustomerExternalIDRoutes(protected, customerHandler)

	f := &externalIDAPIFixture{db: db, router: router, tokens: map[models.UserRole]string{}}
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer} {
		user := &models.User{Email: fmt.Sprintf("%s@external-id-api.test", role), Password: "x", FirstName: "Api",
			LastName: string(role), Role: role, IsActive: true}
		require.NoError(t, db.Create(user).Error)
		token, err := authService.GenerateJWT(user)
		require.NoError(t, err)
		f.tokens[role] = token
	}
	return f
}

func (f *externalIDAPIFixture) do(t *testing.T, role models.UserRole, method, path string, body interface{}) (int, utils.APIResponse) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.tokens[role])
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	var envelope utils.APIResponse
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	}
	return rec.Code, envelope
}

func (f *externalIDAPIFixture) customer(t *testing.T, label string) *models.Customer {
	t.Helper()
	c := &models.Customer{FirstName: "Api" + label, LastName: "Customer", Email: "api-" + label + "@external-id-api.test"}
	require.NoError(t, f.db.Create(c).Error)
	return c
}

func (f *externalIDAPIFixture) mappings(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Model(&models.CustomerExternalID{}).Count(&n).Error)
	return n
}

// lookup answers GET /customers?source=&scope=&external_id= and returns the
// ids of the customers in the answer and its total.
func (f *externalIDAPIFixture) lookup(t *testing.T, role models.UserRole, source, scope, externalID string) (int, []uint, int64) {
	t.Helper()
	query := url.Values{"source": {source}, "scope": {scope}, "external_id": {externalID}}
	status, envelope := f.do(t, role, http.MethodGet, "/customers?"+query.Encode(), nil)
	if status != http.StatusOK {
		return status, nil, -1
	}
	data, ok := envelope.Data.(map[string]interface{})
	require.True(t, ok)
	list, ok := data["customers"].([]interface{})
	require.True(t, ok, "customers is a list, never null")
	ids := make([]uint, 0, len(list))
	for _, entry := range list {
		ids = append(ids, uint(entry.(map[string]interface{})["id"].(float64)))
	}
	return status, ids, int64(data["total"].(float64))
}

func TestCustomerExternalIDAPISQLite(t *testing.T) {
	f := newExternalIDAPIFixture(t)
	triple := map[string]interface{}{"source": "storefront", "scope": "site-eu", "external_id": "Cust-1001"}

	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales} {
		t.Run(fmt.Sprintf("%s creates, resolves and deletes", role), func(t *testing.T) {
			customer := f.customer(t, "rw-"+string(role))
			other := f.customer(t, "other-"+string(role))
			createPath := fmt.Sprintf("/customers/%d/external-ids", customer.ID)

			status, envelope := f.do(t, role, http.MethodPost, createPath, triple)
			require.Equal(t, http.StatusCreated, status, "%+v", envelope.Error)
			data := envelope.Data.(map[string]interface{})
			mappingID := uint(data["id"].(float64))
			assert.EqualValues(t, customer.ID, data["customer_id"])
			assert.Equal(t, "storefront", data["source"])
			assert.Equal(t, "site-eu", data["scope"])
			assert.Equal(t, "Cust-1001", data["external_id"])
			assert.NotContains(t, data, "customer")
			var stored models.CustomerExternalID
			require.NoError(t, f.db.First(&stored, mappingID).Error)
			assert.Equal(t, customer.ID, stored.CustomerID)

			// Admin, sales and support resolve the triple to exactly that
			// customer; the customer role is refused.
			for _, reader := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport} {
				status, ids, total := f.lookup(t, reader, "storefront", "site-eu", "Cust-1001")
				require.Equal(t, http.StatusOK, status, string(reader))
				assert.Equal(t, []uint{customer.ID}, ids, string(reader))
				assert.EqualValues(t, 1, total)
			}
			status, _, _ = f.lookup(t, models.RoleCustomer, "storefront", "site-eu", "Cust-1001")
			assert.Equal(t, http.StatusForbidden, status)
			// The comparison is exact.
			_, ids, total := f.lookup(t, role, "storefront", "site-eu", "cust-1001")
			assert.Empty(t, ids)
			assert.Zero(t, total)
			_, ids, _ = f.lookup(t, role, "storefront", "", "Cust-1001")
			assert.Empty(t, ids, "an absent scope is '', not a wildcard")

			// A duplicate triple is 409, for the same and for another customer.
			before := f.mappings(t)
			status, envelope = f.do(t, role, http.MethodPost, createPath, triple)
			require.Equal(t, http.StatusConflict, status)
			assert.Equal(t, utils.ErrCodeConflict, envelope.Error.Code)
			status, _ = f.do(t, role, http.MethodPost, fmt.Sprintf("/customers/%d/external-ids", other.ID), triple)
			require.Equal(t, http.StatusConflict, status, "another customer cannot take the triple")
			assert.Equal(t, before, f.mappings(t))

			// Another customer's id at the path is 404 and deletes nothing.
			status, _ = f.do(t, role, http.MethodDelete, fmt.Sprintf("/customers/%d/external-ids/%d", other.ID, mappingID), nil)
			assert.Equal(t, http.StatusNotFound, status)
			assert.Equal(t, before, f.mappings(t))

			// Delete.
			status, envelope = f.do(t, role, http.MethodDelete, fmt.Sprintf("/customers/%d/external-ids/%d", customer.ID, mappingID), nil)
			require.Equal(t, http.StatusNoContent, status, "%+v", envelope.Error)
			assert.Equal(t, before-1, f.mappings(t))
			status, ids, total = f.lookup(t, role, "storefront", "site-eu", "Cust-1001")
			require.Equal(t, http.StatusOK, status)
			assert.Empty(t, ids)
			assert.Zero(t, total, "the lookup returns an empty list after the delete")

			// The triple can be created again, here for the other customer.
			status, _ = f.do(t, role, http.MethodPost, fmt.Sprintf("/customers/%d/external-ids", other.ID), triple)
			require.Equal(t, http.StatusCreated, status)
			_, ids, _ = f.lookup(t, role, "storefront", "site-eu", "Cust-1001")
			assert.Equal(t, []uint{other.ID}, ids)

			// Clean up so the next role starts from a free triple.
			var again models.CustomerExternalID
			require.NoError(t, f.db.Where("customer_id = ?", other.ID).Take(&again).Error)
			status, _ = f.do(t, role, http.MethodDelete, fmt.Sprintf("/customers/%d/external-ids/%d", other.ID, again.ID), nil)
			require.Equal(t, http.StatusNoContent, status)
		})
	}

	t.Run("support and customer cannot write", func(t *testing.T) {
		customer := f.customer(t, "readonly")
		status, _ := f.do(t, models.RoleAdmin, http.MethodPost, fmt.Sprintf("/customers/%d/external-ids", customer.ID), triple)
		require.Equal(t, http.StatusCreated, status)
		var mapping models.CustomerExternalID
		require.NoError(t, f.db.Where("customer_id = ?", customer.ID).Take(&mapping).Error)
		before := f.mappings(t)

		for _, role := range []models.UserRole{models.RoleSupport, models.RoleCustomer} {
			status, envelope := f.do(t, role, http.MethodPost, fmt.Sprintf("/customers/%d/external-ids", customer.ID),
				map[string]interface{}{"source": "storefront", "external_id": "written-by-" + string(role)})
			assert.Equal(t, http.StatusForbidden, status, string(role))
			assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
			status, _ = f.do(t, role, http.MethodDelete, fmt.Sprintf("/customers/%d/external-ids/%d", customer.ID, mapping.ID), nil)
			assert.Equal(t, http.StatusForbidden, status, string(role))
		}
		assert.Equal(t, before, f.mappings(t), "nothing is written")
		require.NoError(t, f.db.First(&models.CustomerExternalID{}, mapping.ID).Error, "the mapping is still there")
	})

	t.Run("validation and missing customer", func(t *testing.T) {
		customer := f.customer(t, "validation")
		before := f.mappings(t)
		status, envelope := f.do(t, models.RoleAdmin, http.MethodPost, fmt.Sprintf("/customers/%d/external-ids", customer.ID),
			map[string]interface{}{"source": "storefront", "external_id": "trailing "})
		require.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
		details, _ := envelope.Error.Details.(map[string]interface{})
		assert.Equal(t, "external_id", details["field"])

		status, _ = f.do(t, models.RoleAdmin, http.MethodPost, "/customers/99999999/external-ids", triple)
		assert.Equal(t, http.StatusNotFound, status)

		status, envelope = f.do(t, models.RoleSupport, http.MethodGet, "/customers?external_id=x", nil)
		require.Equal(t, http.StatusBadRequest, status)
		details, _ = envelope.Error.Details.(map[string]interface{})
		assert.Equal(t, "source", details["field"])
		assert.Equal(t, before, f.mappings(t))
	})
}
