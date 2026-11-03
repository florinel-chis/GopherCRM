package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	updateMeSecret  = "S3cret!Value-254"
	updateMeRefusal = "This route does not accept password fields; use POST /auth/change-password"
)

// captureUpdateMeLog sends everything the shared logger writes to a buffer for
// the duration of the test, debug level included.
func captureUpdateMeLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var output bytes.Buffer
	utils.Logger.SetOutput(&output)
	t.Cleanup(func() { utils.Logger.SetOutput(os.Stdout) })
	return &output
}

func newUpdateMeRouter(userService *mocks.UserService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.Logger(), middleware.ErrorHandler())
	router.Use(func(c *gin.Context) {
		c.Set("request_id", "test-request-id")
		c.Set("user_id", uint(7))
		c.Set("user_role", "customer")
		c.Next()
	})
	router.PUT("/users/me", NewUserHandler(userService).UpdateMe)
	return router
}

func putUpdateMe(router *gin.Engine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/users/me", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeUpdateMe(t *testing.T, rec *httptest.ResponseRecorder) utils.APIResponse {
	t.Helper()
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	return response
}

func TestUpdateMeRefusesPasswordFields(t *testing.T) {
	captureUpdateMeLog(t)

	secret := `"` + updateMeSecret + `"`
	cases := []struct {
		name     string
		body     string
		supplied string
	}{
		{"password string", `{"password":` + secret + `}`, updateMeSecret},
		{"current_password string", `{"current_password":` + secret + `}`, updateMeSecret},
		{"new_password string", `{"new_password":` + secret + `}`, updateMeSecret},
		{"empty string", `{"password":""}`, ""},
		{"null", `{"password":null}`, ""},
		{"null current_password", `{"current_password":null}`, ""},
		{"null new_password", `{"new_password":null}`, ""},
		{"number", `{"password":987654321}`, "987654321"},
		{"object", `{"password":{"nested":` + secret + `}}`, updateMeSecret},
		{"array", `{"new_password":[` + secret + `]}`, updateMeSecret},
		{"upper case key", `{"PASSWORD":` + secret + `}`, updateMeSecret},
		{"mixed case current_password", `{"Current_Password":` + secret + `}`, updateMeSecret},
		{"upper case new_password", `{"NEW_PASSWORD":` + secret + `}`, updateMeSecret},
		{
			"next to profile fields",
			`{"email":"new@example.com","first_name":"New","last_name":"Name","password":` + secret + `}`,
			updateMeSecret,
		},
		{"null next to a profile field", `{"first_name":"New","password":null}`, ""},
		{"all three keys", `{"password":` + secret + `,"current_password":"A","new_password":"B"}`, updateMeSecret},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userService := new(mocks.UserService)

			rec := putUpdateMe(newUpdateMeRouter(userService), tc.body)

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			response := decodeUpdateMe(t, rec)
			assert.False(t, response.Success)
			require.NotNil(t, response.Error)
			assert.Equal(t, "PASSWORD_FIELD_NOT_ACCEPTED", response.Error.Code)
			assert.Equal(t, updateMeRefusal, response.Error.Message)
			assert.Contains(t, response.Error.Message, "POST /auth/change-password")
			assert.Nil(t, response.Error.Details)
			if tc.supplied != "" {
				assert.NotContains(t, rec.Body.String(), tc.supplied)
			}
			userService.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
		})
	}

	// A body that fails binding keeps its answer, and is not written either.
	bindingFailures := []struct {
		name string
		body string
		code string
	}{
		{"invalid email", `{"email":"not-an-email","password":` + secret + `}`, utils.ErrCodeValidation},
		{"wrong email type", `{"email":123,"password":` + secret + `}`, utils.ErrCodeBadRequest},
		{"malformed JSON", `{"password":` + secret, utils.ErrCodeBadRequest},
	}
	for _, tc := range bindingFailures {
		t.Run("binding failure/"+tc.name, func(t *testing.T) {
			userService := new(mocks.UserService)

			rec := putUpdateMe(newUpdateMeRouter(userService), tc.body)

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			response := decodeUpdateMe(t, rec)
			require.NotNil(t, response.Error)
			assert.Equal(t, tc.code, response.Error.Code)
			assert.NotContains(t, rec.Body.String(), updateMeSecret)
			userService.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
		})
	}
}

func TestUpdateMeProfileFieldsStillApply(t *testing.T) {
	captureUpdateMeLog(t)

	account := &models.User{
		BaseModel: models.BaseModel{ID: 7},
		Email:     "user@example.com",
		FirstName: "Current",
		LastName:  "Name",
	}

	cases := []struct {
		name    string
		body    string
		updates map[string]interface{}
	}{
		{
			name: "every profile field",
			body: `{"email":"user@example.com","first_name":"Current","last_name":"Name"}`,
			updates: map[string]interface{}{
				"email": "user@example.com", "first_name": "Current", "last_name": "Name",
			},
		},
		{name: "empty body object", body: `{}`, updates: map[string]interface{}{}},
		{
			// Only the three named keys are refused.
			name:    "other unknown keys stay ignored",
			body:    `{"first_name":"Current","passwd":"ignored","password_hint":"ignored"}`,
			updates: map[string]interface{}{"first_name": "Current"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userService := new(mocks.UserService)
			// The exact map: no password key and, for {}, no profile field.
			userService.On("Update", uint(7), tc.updates).Return(account, nil).Once()

			rec := putUpdateMe(newUpdateMeRouter(userService), tc.body)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			response := decodeUpdateMe(t, rec)
			assert.True(t, response.Success)
			data, ok := response.Data.(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, account.Email, data["email"])
			assert.Equal(t, account.FirstName, data["first_name"])
			userService.AssertExpectations(t)
		})
	}

	t.Run("duplicate email", func(t *testing.T) {
		userService := new(mocks.UserService)
		userService.On("Update", uint(7), map[string]interface{}{"email": "taken@example.com"}).
			Return(nil, fmt.Errorf("user with this email already exists: %w", apperrors.ErrDuplicateEmail)).Once()

		rec := putUpdateMe(newUpdateMeRouter(userService), `{"email":"taken@example.com"}`)

		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		response := decodeUpdateMe(t, rec)
		require.NotNil(t, response.Error)
		assert.Equal(t, utils.ErrCodeConflict, response.Error.Code)
		userService.AssertExpectations(t)
	})
}

func TestUpdateMeRefusalLogOmitsPassword(t *testing.T) {
	secret := `"` + updateMeSecret + `"`

	for _, key := range []string{"password", "current_password", "new_password", "PASSWORD"} {
		t.Run(key, func(t *testing.T) {
			output := captureUpdateMeLog(t)

			rec := putUpdateMe(newUpdateMeRouter(new(mocks.UserService)),
				`{"first_name":"New","`+key+`":`+secret+`}`)

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			logged := output.String()
			assert.NotContains(t, logged, updateMeSecret)

			// The access line and the handler's debug lines say nothing of
			// passwords, so the refusal is the only line that does.
			var refusals []string
			for _, line := range strings.Split(logged, "\n") {
				if strings.Contains(strings.ToLower(line), "password") {
					refusals = append(refusals, line)
				}
			}
			require.Len(t, refusals, 1, logged)
			assert.Contains(t, refusals[0], "Refused PUT /users/me: body carries a password field")
			assert.Contains(t, refusals[0], `"level":"warning"`)
		})
	}

	t.Run("binding failure", func(t *testing.T) {
		output := captureUpdateMeLog(t)

		rec := putUpdateMe(newUpdateMeRouter(new(mocks.UserService)),
			`{"email":"not-an-email","password":`+secret+`}`)

		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.NotContains(t, output.String(), updateMeSecret)
	})
}
