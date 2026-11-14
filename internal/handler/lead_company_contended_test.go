package handler

// AC-9 of #182: POST /leads/{id}/convert and PUT /leads/{id} answer an error
// matching apperrors.ErrLeadCompanyContended with 409 CONFLICT in the unified
// envelope; the other mappings of both routes are unchanged.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestLeadCompanyContendedHandlerMapping(t *testing.T) {
	previous := utils.Logger
	t.Cleanup(func() { utils.Logger = previous })
	utils.Logger = logrus.New()
	utils.Logger.SetLevel(logrus.PanicLevel)

	lead := &models.Lead{BaseModel: models.BaseModel{ID: 1}, Email: "lead@example.com", OwnerID: 1, Status: models.LeadStatusQualified}
	contended := fmt.Errorf("lead 1: company_id changed in each of 4 attempts: %w", apperrors.ErrLeadCompanyContended)

	router := func(svc *mocks.LeadService) *gin.Engine {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("request_id", "lead-company-contended")
			c.Set("user_id", uint(1))
			c.Set("user_role", string(models.RoleAdmin))
			c.Next()
		})
		h := NewLeadHandler(svc)
		r.POST("/leads/:id/convert", h.ConvertToCustomer)
		r.PUT("/leads/:id", h.Update)
		return r
	}
	send := func(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	envelope := func(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) *utils.APIError {
		t.Helper()
		require.Equal(t, status, rec.Code, rec.Body.String())
		var response utils.APIResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
		assert.False(t, response.Success)
		require.NotNil(t, response.Error)
		assert.Equal(t, code, response.Error.Code)
		return response.Error
	}

	convertCases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"contended is 409", contended, http.StatusConflict, utils.ErrCodeConflict},
		{"duplicate email stays 409", fmt.Errorf("insert: %w", apperrors.ErrDuplicateEmail), http.StatusConflict, utils.ErrCodeConflict},
		{"already converted stays 400", apperrors.NewLeadAlreadyConverted(1), http.StatusBadRequest, utils.ErrCodeBadRequest},
		{"erased lead stays 404", fmt.Errorf("lead: %w", apperrors.ErrNotFound), http.StatusNotFound, utils.ErrCodeNotFound},
		{"other failure stays 500", errors.New("commit failed"), http.StatusInternalServerError, utils.ErrCodeInternal},
	}
	for _, tc := range convertCases {
		t.Run("convert: "+tc.name, func(t *testing.T) {
			svc := new(mocks.LeadService)
			svc.On("GetByIDContext", mock.Anything, uint(1)).Return(lead, nil)
			svc.On("ConvertToCustomer", mock.Anything, uint(1), mock.Anything).Return(nil, tc.err).Once()
			apiErr := envelope(t, send(router(svc), http.MethodPost, "/leads/1/convert", `{}`), tc.status, tc.code)
			if errors.Is(tc.err, apperrors.ErrLeadCompanyContended) {
				assert.Equal(t, leadCompanyContendedMessage, apiErr.Message)
			}
			svc.AssertExpectations(t)
		})
	}

	updateCases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"contended is 409", contended, http.StatusConflict, utils.ErrCodeConflict},
		{"unknown company stays 400 INVALID_REFERENCE", fmt.Errorf("unknown company_id 9: %w", apperrors.ErrCompanyNotFound), http.StatusBadRequest, apperrors.CodeInvalidReference},
		{"invalid owner stays 400 INVALID_REFERENCE", fmt.Errorf("owner: %w", apperrors.ErrInvalidLeadOwner), http.StatusBadRequest, apperrors.CodeInvalidReference},
		{"other failure stays 500", errors.New("commit failed"), http.StatusInternalServerError, utils.ErrCodeInternal},
	}
	for _, tc := range updateCases {
		t.Run("update: "+tc.name, func(t *testing.T) {
			svc := new(mocks.LeadService)
			svc.On("GetByID", uint(1)).Return(lead, nil)
			svc.On("Update", uint(1), mock.Anything).Return(nil, tc.err).Once()
			apiErr := envelope(t, send(router(svc), http.MethodPut, "/leads/1", `{"notes":"edited"}`), tc.status, tc.code)
			if errors.Is(tc.err, apperrors.ErrLeadCompanyContended) {
				assert.Equal(t, leadCompanyContendedMessage, apiErr.Message)
			}
			svc.AssertExpectations(t)
		})
	}
}
