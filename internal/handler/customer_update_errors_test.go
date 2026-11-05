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
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Regression root of #256: PUT /customers/{id} classifies what the service
// returns with errors.Is. Only apperrors.ErrNotFound means the write lost to an
// erasure; a raw record-not-found or any other failure stays a generic 500.
func TestCustomerHandlerUpdateMapsServiceErrors(t *testing.T) {
	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Cleanup(func() { utils.Logger = previous })
	gin.SetMode(gin.TestMode)

	const internalMessage = "An unexpected error occurred"

	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantText   string
	}{
		{
			name:       "wrapped not found",
			err:        fmt.Errorf("customer 7 not found: %w", apperrors.ErrNotFound),
			wantStatus: http.StatusNotFound,
			wantCode:   utils.ErrCodeNotFound,
			wantText:   "Customer not found",
		},
		{
			name:       "doubly wrapped not found",
			err:        fmt.Errorf("outer: %w", fmt.Errorf("customer 7 not found: %w", apperrors.ErrNotFound)),
			wantStatus: http.StatusNotFound,
			wantCode:   utils.ErrCodeNotFound,
			wantText:   "Customer not found",
		},
		{
			name:       "unknown company",
			err:        fmt.Errorf("unknown company_id 9: %w", apperrors.ErrCompanyNotFound),
			wantStatus: http.StatusBadRequest,
			wantCode:   apperrors.CodeInvalidReference,
			wantText:   "unknown company_id 9: company not found",
		},
		{
			name:       "duplicate email",
			err:        fmt.Errorf("customer with this email already exists: %w", apperrors.ErrDuplicateEmail),
			wantStatus: http.StatusConflict,
			wantCode:   utils.ErrCodeConflict,
			wantText:   "customer with this email already exists",
		},
		{
			name:       "validation",
			err:        fmt.Errorf("website must be an http or https URL: %w", apperrors.ErrValidation),
			wantStatus: http.StatusBadRequest,
			wantCode:   utils.ErrCodeBadRequest,
			wantText:   "website must be an http or https URL: validation failed",
		},
		{
			name:       "database failure",
			err:        errors.New("driver: bad connection"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   utils.ErrCodeInternal,
			wantText:   internalMessage,
		},
		{
			name:       "text that only looks like not found",
			err:        errors.New("customer 7 not found"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   utils.ErrCodeInternal,
			wantText:   internalMessage,
		},
		{
			name:       "raw record not found",
			err:        gorm.ErrRecordNotFound,
			wantStatus: http.StatusInternalServerError,
			wantCode:   utils.ErrCodeInternal,
			wantText:   internalMessage,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			customerService := new(mocks.CustomerService)
			customerService.On("GetByID", uint(7)).Return(&models.Customer{
				BaseModel: models.BaseModel{ID: 7},
				FirstName: "Stored",
				LastName:  "Customer",
				Email:     "stored@example.com",
			}, nil)
			customerService.On("Update", mock.AnythingOfType("*models.Customer")).Return(tc.err)

			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("user_id", uint(1))
				c.Set("user_role", string(models.RoleAdmin))
				c.Next()
			})
			router.PUT("/customers/:id", NewCustomerHandler(customerService).Update)

			request := httptest.NewRequest(http.MethodPut, "/customers/7", strings.NewReader(`{"notes":"edited"}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			require.Equal(t, tc.wantStatus, recorder.Code, recorder.Body.String())
			var envelope utils.APIResponse
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
			require.NotNil(t, envelope.Error)
			assert.False(t, envelope.Success)
			assert.Equal(t, tc.wantCode, envelope.Error.Code)
			assert.Equal(t, tc.wantText, envelope.Error.Message)
			customerService.AssertExpectations(t)
		})
	}
}
