package handler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestHandlerLoggingPrivacy_APIKeyPayload(t *testing.T) {
	const token = "handler-api-key-token-canary"
	const name = "handler-private-name-canary"
	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprintf("success=%t", success), func(t *testing.T) {
			old := utils.Logger
			defer func() { utils.Logger = old }()
			var output bytes.Buffer
			utils.Logger = logrus.New()
			utils.Logger.SetLevel(logrus.DebugLevel)
			utils.Logger.SetFormatter(&logrus.JSONFormatter{})
			utils.Logger.SetOutput(&output)

			svc := new(mocks.APIKeyService)
			if success {
				svc.On("Generate", uint(12), name, (*time.Time)(nil)).Return(token, &models.APIKey{BaseModel: models.BaseModel{ID: 14}, Name: name}, nil)
			} else {
				svc.On("Generate", uint(12), name, (*time.Time)(nil)).Return("", nil, fmt.Errorf("key generation rejected %q", name))
			}
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("request_id", "handler-privacy-request")
				c.Set("user_id", uint(12))
				c.Set(service.AuthMethodContextKey, service.AuthMethodBearer)
				// A fresh sign-in, so the request reaches the service (#532).
				c.Set(service.SignedInAtContextKey, time.Now())
				c.Next()
			})
			router.POST("/api-keys", NewAPIKeyHandler(svc).Create)
			req := httptest.NewRequest(http.MethodPost, "/api-keys", bytes.NewBufferString(`{"name":"`+name+`"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if success {
				assert.Equal(t, http.StatusCreated, rec.Code)
				assert.Contains(t, rec.Body.String(), token)
				assert.Contains(t, rec.Body.String(), name)
				assert.Contains(t, output.String(), `"status_code":201`)
			} else {
				assert.Equal(t, http.StatusInternalServerError, rec.Code)
				assert.Contains(t, output.String(), "Failed to generate API key")
				assert.Contains(t, output.String(), "key generation rejected")
			}
			assert.NotContains(t, output.String(), token)
			assert.NotContains(t, output.String(), name)
			assert.Contains(t, output.String(), "APIKeyHandler.Create")
			assert.Contains(t, output.String(), "handler-privacy-request")
			assert.Contains(t, output.String(), `"user_id":12`)
			svc.AssertExpectations(t)
		})
	}
}

// A POST /api-keys or a PUT is_active=true refused to an API-key request logs
// one Warn line whose own fields are user_id and error_code only: no key name
// from the body, no Authorization value, no key.
func TestHandlerLoggingPrivacy_APIKeySessionRefusal(t *testing.T) {
	const name = "refused-private-name-canary"
	const credential = "gcrm_refusedcredentialcanary0123456789abcdef"
	cases := []struct {
		label, method, path, body string
	}{
		{"create", http.MethodPost, "/api-keys", `{"name":"` + name + `"}`},
		{"switch_on", http.MethodPut, "/api-keys/14", `{"name":"` + name + `","is_active":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			old := utils.Logger
			defer func() { utils.Logger = old }()
			var output bytes.Buffer
			utils.Logger = logrus.New()
			utils.Logger.SetLevel(logrus.DebugLevel)
			utils.Logger.SetFormatter(&logrus.JSONFormatter{})
			utils.Logger.SetOutput(&output)

			svc := new(mocks.APIKeyService)
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("request_id", "handler-privacy-request")
				c.Set("user_id", uint(12))
				c.Set(service.AuthMethodContextKey, service.AuthMethodAPIKey)
				c.Next()
			})
			h := NewAPIKeyHandler(svc)
			router.POST("/api-keys", h.Create)
			router.PUT("/api-keys/:id", h.Update)
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "ApiKey "+credential)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.NotContains(t, output.String(), name)
			assert.NotContains(t, output.String(), credential)
			assert.NotContains(t, output.String(), "refusedcredentialcanary")

			// The refusal line: its own fields beyond the request context are
			// user_id and error_code.
			allowed := map[string]bool{"level": true, "msg": true, "time": true, "request_id": true, "handler": true, "user_id": true, "error_code": true}
			found := 0
			scanner := bufio.NewScanner(&output)
			for scanner.Scan() {
				var entry map[string]interface{}
				if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry["level"] != "warning" {
					continue
				}
				found++
				assert.Equal(t, "session_required", entry["error_code"])
				assert.Equal(t, float64(12), entry["user_id"])
				for key := range entry {
					assert.True(t, allowed[key], "unexpected log field %q", key)
				}
			}
			assert.Equal(t, 1, found, "one refusal line")
			assert.Empty(t, svc.Calls, "the service is not called")
		})
	}
}
