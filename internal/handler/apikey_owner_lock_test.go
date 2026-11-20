package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestAPIKeyCreateOwnerUnavailableMapping pins the answer of POST /api-keys
// when the issuing transaction refuses the owner under the owner lock (#342):
// an error matching service.ErrAccountUnavailable is the 401 a deactivated or
// erased account gets, with no key in the body; every other error, the
// unwired service included, stays a 500.
func TestAPIKeyCreateOwnerUnavailableMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})

	cases := []struct {
		name     string
		err      error
		status   int
		code     string
		message  string
		matchMsg bool
	}{
		{"the bare sentinel", service.ErrAccountUnavailable, http.StatusUnauthorized, utils.ErrCodeUnauthorized, "Unauthorized", true},
		{"a wrapped refusal", fmt.Errorf("issuing: %w", service.ErrAccountUnavailable), http.StatusUnauthorized,
			utils.ErrCodeUnauthorized, "Unauthorized", true},
		{"an unwired service", service.ErrAPIKeyIssuanceUnavailable, http.StatusInternalServerError, utils.ErrCodeInternal, "", false},
		{"a rolled-back insert", errors.New("failed to store api key: disk full"), http.StatusInternalServerError,
			utils.ErrCodeInternal, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := mocks.NewAPIKeyService(t)
			svc.On("Generate", uint(7), "CI pipeline", mock.Anything).Return("", nil, tc.err).Once()

			router := gin.New()
			router.Use(middleware.ErrorHandler())
			router.Use(func(c *gin.Context) {
				c.Set("request_id", "test-request-id")
				c.Set("user_id", uint(7))
				c.Set(service.AuthMethodContextKey, service.AuthMethodBearer)
				c.Next()
			})
			router.POST("/api-keys", NewAPIKeyHandler(svc).Create)

			req := httptest.NewRequest(http.MethodPost, "/api-keys", strings.NewReader(`{"name":"CI pipeline"}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			require.Equal(t, tc.status, w.Code, w.Body.String())
			var body struct {
				Success bool                   `json:"success"`
				Data    map[string]interface{} `json:"data"`
				Error   struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.False(t, body.Success)
			assert.Equal(t, tc.code, body.Error.Code)
			if tc.matchMsg {
				assert.Equal(t, tc.message, body.Error.Message)
			}
			assert.Nil(t, body.Data, "no key in the body")
			assert.NotContains(t, w.Body.String(), "gcrm_")
		})
	}
}
