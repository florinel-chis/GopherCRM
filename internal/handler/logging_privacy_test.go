package handler

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
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
