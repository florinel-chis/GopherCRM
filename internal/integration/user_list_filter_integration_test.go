package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// #428: GET /users filters on role in the query, before offset and limit.
// These roots drive the real user routes, handler, service and repository as
// an admin over SQLite and, when TEST_MYSQL_DSN is set, a disposable MySQL
// schema (openTicketVisibilityDB).

func TestUserListRoleFiltersSQLite(t *testing.T) {
	runUserListRoleFilters(t, false)
}

func TestUserListRoleFiltersMySQL(t *testing.T) {
	runUserListRoleFilters(t, true)
}

type userListEnvelope struct {
	Success bool          `json:"success"`
	Data    []models.User `json:"data"`
	Meta    utils.APIMeta `json:"meta"`
	Error   struct {
		Code    string                 `json:"code"`
		Details map[string]interface{} `json:"details"`
	} `json:"error"`
}

func runUserListRoleFilters(t *testing.T, useMySQL bool) {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)

	db := openTicketVisibilityDB(t, useMySQL)
	require.NoError(t, db.AutoMigrate(&models.Role{}, &models.User{}))
	userRepo := repository.NewUserRepository(db)

	router := gin.New()
	// Stands in for the auth middleware: the caller is a stored user.
	router.Use(func(c *gin.Context) {
		id, err := strconv.ParseUint(c.GetHeader("X-Test-User"), 10, 32)
		if err != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		user, err := userRepo.GetByID(uint(id))
		if err != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set("user_id", user.ID)
		c.Set("user_role", string(user.Role))
		c.Next()
	})
	handler.SetupUserRoutes(router.Group("/api/v1"), handler.NewUserHandler(service.NewUserService(userRepo)))

	seed := func(email, first string, role models.UserRole) *models.User {
		t.Helper()
		u := &models.User{Email: email, Password: "x", FirstName: first, LastName: "Filter", Role: role, IsActive: true}
		require.NoError(t, db.Create(u).Error)
		return u
	}
	const token = "zqtoken"
	admin := seed("ulf-admin@example.com", "Admin", models.RoleAdmin)
	sales := seed("ulf-sales-"+token+"@example.com", "Sam", models.RoleSales)
	seed("ulf-sales2@example.com", "Sid", models.RoleSales)
	seed("ulf-support-"+token+"@example.com", "Sue", models.RoleSupport)
	seed("ulf-support2@example.com", "Sol", models.RoleSupport)
	seed("ulf-support3@example.com", "Sy", models.RoleSupport)
	seed("ulf-customer-"+token+"@example.com", "Cat", models.RoleCustomer)
	gone := seed("ulf-support-gone@example.com", "Gone", models.RoleSupport)
	require.NoError(t, db.Delete(&models.User{}, gone.ID).Error)

	get := func(query string) (*httptest.ResponseRecorder, userListEnvelope) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users"+query, nil)
		req.Header.Set("X-Test-User", strconv.FormatUint(uint64(admin.ID), 10))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var body userListEnvelope
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
		return w, body
	}

	t.Run("role and search combine", func(t *testing.T) {
		w, body := get("?role=sales&search=" + token)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Len(t, body.Data, 1)
		assert.Equal(t, sales.ID, body.Data[0].ID)
		assert.Equal(t, int64(1), body.Meta.Total)
	})

	t.Run("the filter applies before the limit", func(t *testing.T) {
		w, body := get("?role=support&limit=1")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Len(t, body.Data, 1)
		assert.Equal(t, models.RoleSupport, body.Data[0].Role)
		assert.Equal(t, int64(3), body.Meta.Total, "the soft-deleted support user is not counted")
		assert.Equal(t, int64(3), body.Meta.TotalPages)
	})

	t.Run("every row matches the role", func(t *testing.T) {
		for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer} {
			w, body := get("?role=" + string(role))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.NotEmpty(t, body.Data, role)
			for _, u := range body.Data {
				assert.Equal(t, role, u.Role)
			}
		}
	})

	t.Run("a wrong-case role is 400", func(t *testing.T) {
		w, body := get("?role=Sales")
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, utils.ErrCodeValidation, body.Error.Code)
		assert.Contains(t, body.Error.Details, "role")
		assert.NotContains(t, w.Body.String(), "Sales")
	})

	t.Run("an empty role is no filter", func(t *testing.T) {
		_, none := get("")
		w, empty := get("?role=")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, int64(7), none.Meta.Total)
		assert.Equal(t, none.Meta.Total, empty.Meta.Total)
		assert.Equal(t, len(none.Data), len(empty.Data))
	})
}
