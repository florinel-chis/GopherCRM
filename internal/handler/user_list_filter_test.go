package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// #428: GET /users takes an optional role filter. These cases run the real
// handler over a mock service, so every service call, and every argument it
// receives, is pinned.

type userFilterResponse struct {
	Success bool          `json:"success"`
	Data    []models.User `json:"data"`
	Meta    utils.APIMeta `json:"meta"`
	Error   struct {
		Code    string                 `json:"code"`
		Details map[string]interface{} `json:"details"`
	} `json:"error"`
}

func newUserFilterRouter(t *testing.T, svc *mocks.UserService) *gin.Engine {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)
	h := NewUserHandler(svc)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleAdmin))
		c.Next()
	})
	router.GET("/users", h.List)
	return router
}

func getUserFilter(t *testing.T, router *gin.Engine, path string) (*httptest.ResponseRecorder, userFilterResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var body userFilterResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	return w, body
}

func newStrictUserService(t *testing.T) *mocks.UserService {
	svc := new(mocks.UserService)
	svc.Test(t)
	return svc
}

func assertOnlyListFiltered(t *testing.T, svc *mocks.UserService) {
	t.Helper()
	svc.AssertNotCalled(t, "List", mock.Anything, mock.Anything)
	svc.AssertNotCalled(t, "ListSorted", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	svc.AssertNotCalled(t, "Search", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	svc.AssertExpectations(t)
}

func TestUserListRoleFilterHandler(t *testing.T) {
	sales := []models.User{{BaseModel: models.BaseModel{ID: 7}, Email: "s@example.com", Role: models.RoleSales}}

	t.Run("role=sales calls ListFiltered only", func(t *testing.T) {
		svc := newStrictUserService(t)
		svc.On("ListFiltered", repository.UserListFilter{Role: "sales"}, 0, 20, "", "asc").Return(sales, int64(1), nil).Once()
		w, body := getUserFilter(t, newUserFilterRouter(t, svc), "/users?role=sales")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Len(t, body.Data, 1)
		assert.Equal(t, uint(7), body.Data[0].ID)
		assertOnlyListFiltered(t, svc)
	})

	t.Run("role combines with search, sort and page", func(t *testing.T) {
		svc := newStrictUserService(t)
		svc.On("ListFiltered", repository.UserListFilter{Search: "tok", Role: "sales"}, 5, 5, "email", "desc").Return(sales, int64(6), nil).Once()
		w, _ := getUserFilter(t, newUserFilterRouter(t, svc), "/users?role=sales&search=tok&sort_by=email&sort_order=desc&page=2&limit=5")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assertOnlyListFiltered(t, svc)
	})

	t.Run("empty role takes the unchanged List branch", func(t *testing.T) {
		svc := newStrictUserService(t)
		svc.On("List", 0, 20).Return([]models.User{}, int64(0), nil).Once()
		w, _ := getUserFilter(t, newUserFilterRouter(t, svc), "/users?role=")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		svc.AssertNotCalled(t, "ListFiltered", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		svc.AssertExpectations(t)
	})

	t.Run("invalid roles are 400 VALIDATION_ERROR, no call, value not echoed", func(t *testing.T) {
		for _, tc := range []struct{ query, raw string }{
			{"role=Sales", "Sales"},
			{"role=manager", "manager"},
			{"role=%20", " "},
		} {
			svc := newStrictUserService(t)
			w, body := getUserFilter(t, newUserFilterRouter(t, svc), "/users?"+tc.query)
			assert.Equal(t, http.StatusBadRequest, w.Code, tc.query)
			assert.Equal(t, utils.ErrCodeValidation, body.Error.Code, tc.query)
			assert.Contains(t, body.Error.Details, "role", tc.query)
			assert.Empty(t, svc.Calls, "no service method may be called: %s", tc.query)
			if tc.raw != " " {
				assert.NotContains(t, w.Body.String(), tc.raw, tc.query)
			} else {
				assert.NotContains(t, w.Body.String(), `"role":" "`, tc.query)
			}
		}
	})

	t.Run("a repeated role uses the first value", func(t *testing.T) {
		svc := newStrictUserService(t)
		svc.On("ListFiltered", repository.UserListFilter{Role: "sales"}, 0, 20, "", "asc").Return(sales, int64(1), nil).Once()
		w, _ := getUserFilter(t, newUserFilterRouter(t, svc), "/users?role=sales&role=bogus")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assertOnlyListFiltered(t, svc)
	})

	t.Run("meta comes from the ListFiltered total", func(t *testing.T) {
		svc := newStrictUserService(t)
		svc.On("ListFiltered", repository.UserListFilter{Role: "support"}, 0, 2, "", "asc").Return(sales, int64(5), nil).Once()
		w, body := getUserFilter(t, newUserFilterRouter(t, svc), "/users?role=support&limit=2")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, int64(5), body.Meta.Total)
		assert.Equal(t, int64(3), body.Meta.TotalPages)
		assertOnlyListFiltered(t, svc)
	})
}
