package tests

// Form mail delivery records over HTTP (issue #216): the real routes behind the
// real JWT and role middleware, the real services and repositories on SQLite
// with the production one-connection pool.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/forms"
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

const formMailAPISecret = "form-mail-delivery-api-regression-secret-32"

// formMailAPIMailer fails while fail is set and records the recipients.
type formMailAPIMailer struct {
	mu   sync.Mutex
	fail bool
	to   []string
}

func (m *formMailAPIMailer) SendPasswordReset(string, string) error { return nil }

func (m *formMailAPIMailer) Send(to, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.to = append(m.to, to)
	if m.fail {
		return errors.New("relay unavailable")
	}
	return nil
}

func (m *formMailAPIMailer) setFail(fail bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail = fail
}

type formMailAPIError struct {
	Code    string                 `json:"code"`
	Details map[string]interface{} `json:"details"`
}

type formMailAPIMeta struct {
	Total   int64 `json:"total"`
	Page    int   `json:"page"`
	PerPage int   `json:"per_page"`
}

type formMailAPIEnvelope struct {
	Success bool              `json:"success"`
	Data    json.RawMessage   `json:"data"`
	Meta    *formMailAPIMeta  `json:"meta"`
	Error   *formMailAPIError `json:"error"`
}

var formMailDeliveryKeys = []string{
	"attempts", "created_at", "error_code", "form_id", "id", "kind", "last_attempt_at",
	"retryable", "sent_at", "status", "stuck", "submission_id", "updated_at",
}

func TestFormMailDeliveryAPISQLite(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", formMailAPISecret)

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:form_mail_api_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.APIKey{}, &models.Company{}, &models.Customer{}, &models.Lead{},
		&models.Form{}, &models.FormSubmission{}, &models.FormConfirmationToken{}, &models.FormMailDelivery{},
	))

	userRepo := repository.NewUserRepository(db)
	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db), config.JWTConfig{Secret: formMailAPISecret, ExpiryHours: 1})
	mail := &formMailAPIMailer{}
	formsCfg := config.FormsConfig{PublicBaseURL: "https://crm.example.test"}
	formService := service.NewFormService(repository.NewFormRepository(db), repository.NewLeadRepository(db), userRepo, mail,
		utils.NewTransactionManager(db), formsCfg, "/api/v1")

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	api := router.Group("/api/v1")
	handler.SetupFormPublicRoutes(api, handler.NewFormPublicHandler(formService, formsCfg, "/api/v1"))
	protected := api.Group("")
	protected.Use(middleware.Auth(authService))
	handler.SetupFormRoutes(protected, handler.NewFormHandler(formService))

	tokens := map[models.UserRole]string{}
	var admin *models.User
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer} {
		user := &models.User{Email: fmt.Sprintf("%s@form-mail.test", role), FirstName: "Api", LastName: string(role), Role: role, IsActive: true}
		require.NoError(t, service.NewUserService(userRepo).Register(user, "password123"))
		bearer, err := authService.Login(user.Email, "password123")
		require.NoError(t, err)
		tokens[role] = bearer
		if role == models.RoleAdmin {
			admin = user
		}
	}

	call := func(t *testing.T, role models.UserRole, method, path string) (int, formMailAPIEnvelope) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, nil)
		req.Header.Set("Authorization", "Bearer "+tokens[role])
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var envelope formMailAPIEnvelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
		return rec.Code, envelope
	}
	assertDeliveryShape := func(t *testing.T, raw json.RawMessage) []map[string]interface{} {
		t.Helper()
		var rows []map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &rows), string(raw))
		for _, row := range rows {
			keys := make([]string, 0, len(row))
			for key := range row {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			assert.Equal(t, formMailDeliveryKeys, keys, "a delivery carries no address field")
		}
		return rows
	}

	newForm := func(t *testing.T, name string) *models.Form {
		t.Helper()
		form := &models.Form{
			Name:            name,
			Status:          models.FormStatusPublished,
			Fields:          []models.FormFieldDef{{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true}},
			NotifyEmails:    []string{"sales-team@example.com", "support-team@example.com"},
			FollowUpSubject: "Your guide",
			CreateLead:      true,
			DefaultOwnerID:  admin.ID,
		}
		require.NoError(t, formService.Create(form, admin.ID))
		return form
	}
	submit := func(t *testing.T, form *models.Form, email string, fail bool) uint {
		t.Helper()
		mail.setFail(fail)
		defer mail.setFail(false)
		_, err := formService.SubmitPublic(context.Background(), form.PublicID, &service.PublicSubmissionRequest{
			Values:    map[string]string{"email": email},
			Challenge: forms.NewChallenge([]byte(formMailAPISecret), time.Now().Add(-30*time.Second)),
		}, service.SubmissionMeta{IP: "203.0.113.7"})
		require.NoError(t, err)
		var submission models.FormSubmission
		require.NoError(t, db.Where("form_id = ? AND email = ?", form.ID, email).Order("id desc").First(&submission).Error)
		return submission.ID
	}

	form := newForm(t, "Delivery API form")
	failedID := submit(t, form, "visitor@example.com", true)
	sentID := submit(t, form, "happy@example.com", false)
	bare := &models.FormSubmission{FormID: form.ID, Status: models.FormSubmissionReceived, Email: "old@example.com", Data: map[string]string{"email": "old@example.com"}}
	require.NoError(t, db.Create(bare).Error)

	t.Run("submission reads carry deliveries", func(t *testing.T) {
		status, envelope := call(t, models.RoleSupport, http.MethodGet, fmt.Sprintf("/forms/submissions/%d", failedID))
		require.Equal(t, http.StatusOK, status)
		var submission struct {
			Deliveries json.RawMessage `json:"deliveries"`
		}
		require.NoError(t, json.Unmarshal(envelope.Data, &submission))
		rows := assertDeliveryShape(t, submission.Deliveries)
		require.Len(t, rows, 3)
		for _, row := range rows {
			assert.Equal(t, models.FormMailStatusFailed, row["status"])
			assert.Equal(t, true, row["retryable"])
		}

		status, envelope = call(t, models.RoleSupport, http.MethodGet, fmt.Sprintf("/forms/submissions/%d", bare.ID))
		require.Equal(t, http.StatusOK, status)
		require.NoError(t, json.Unmarshal(envelope.Data, &submission))
		assert.JSONEq(t, `[]`, string(submission.Deliveries), "[] when there are none")

		status, envelope = call(t, models.RoleSales, http.MethodGet, fmt.Sprintf("/forms/%d/submissions", form.ID))
		require.Equal(t, http.StatusOK, status)
		var list []struct {
			ID         uint            `json:"id"`
			Deliveries json.RawMessage `json:"deliveries"`
		}
		require.NoError(t, json.Unmarshal(envelope.Data, &list))
		require.Len(t, list, 3)
		counts := map[uint]int{}
		for _, item := range list {
			counts[item.ID] = len(assertDeliveryShape(t, item.Deliveries))
			assert.NotEqual(t, "null", string(item.Deliveries))
		}
		assert.Equal(t, map[uint]int{failedID: 3, sentID: 3, bare.ID: 0}, counts)
		assert.NotContains(t, string(envelope.Data), "sales-team@example.com", "no address appears on a delivery")
	})

	var failedRows []map[string]interface{}
	t.Run("the attention list paginates and validates its filters", func(t *testing.T) {
		status, envelope := call(t, models.RoleSupport, http.MethodGet, "/forms/deliveries")
		require.Equal(t, http.StatusOK, status)
		failedRows = assertDeliveryShape(t, envelope.Data)
		require.Len(t, failedRows, 3, "the failed rows; the sent ones are not listed")
		require.NotNil(t, envelope.Meta)
		assert.EqualValues(t, 3, envelope.Meta.Total)

		status, envelope = call(t, models.RoleAdmin, http.MethodGet, fmt.Sprintf("/forms/deliveries?form_id=%d&kind=notification&limit=1&offset=1", form.ID))
		require.Equal(t, http.StatusOK, status)
		rows := assertDeliveryShape(t, envelope.Data)
		require.Len(t, rows, 1)
		assert.Equal(t, models.FormMailKindNotification, rows[0]["kind"])
		assert.EqualValues(t, 2, envelope.Meta.Total)
		assert.Equal(t, 2, envelope.Meta.Page)
		assert.Equal(t, 1, envelope.Meta.PerPage)

		status, envelope = call(t, models.RoleAdmin, http.MethodGet, "/forms/deliveries?status=sent")
		require.Equal(t, http.StatusOK, status)
		assert.Len(t, assertDeliveryShape(t, envelope.Data), 3, "exactly the stored status")

		for query, field := range map[string]string{"form_id=abc": "form_id", "status=bogus": "status", "kind=bogus": "kind"} {
			status, envelope := call(t, models.RoleAdmin, http.MethodGet, "/forms/deliveries?"+query)
			assert.Equal(t, http.StatusBadRequest, status, query)
			require.NotNil(t, envelope.Error, query)
			assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code, query)
			assert.Contains(t, envelope.Error.Details, field, query)
		}
	})

	idOf := func(row map[string]interface{}) uint {
		return uint(row["id"].(float64))
	}

	t.Run("retry answers 200, 409, 404 and 502", func(t *testing.T) {
		require.Len(t, failedRows, 3)
		mail.setFail(true)
		status, envelope := call(t, models.RoleSales, http.MethodPost, fmt.Sprintf("/forms/deliveries/%d/retry", idOf(failedRows[0])))
		mail.setFail(false)
		assert.Equal(t, http.StatusBadGateway, status)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeMailDeliveryFailed, envelope.Error.Code)

		status, envelope = call(t, models.RoleSales, http.MethodPost, fmt.Sprintf("/forms/deliveries/%d/retry", idOf(failedRows[0])))
		require.Equal(t, http.StatusOK, status)
		var delivery map[string]interface{}
		require.NoError(t, json.Unmarshal(envelope.Data, &delivery))
		assert.Equal(t, models.FormMailStatusSent, delivery["status"])
		assert.EqualValues(t, 3, delivery["attempts"])
		assert.Equal(t, false, delivery["retryable"])

		status, envelope = call(t, models.RoleAdmin, http.MethodPost, fmt.Sprintf("/forms/deliveries/%d/retry", idOf(failedRows[0])))
		assert.Equal(t, http.StatusConflict, status, "a sent row")
		require.NotNil(t, envelope.Error)
		assert.Equal(t, "FORM_DELIVERY_NOT_RETRYABLE", envelope.Error.Code)

		status, envelope = call(t, models.RoleAdmin, http.MethodPost, "/forms/deliveries/999999/retry")
		assert.Equal(t, http.StatusNotFound, status)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)

		// A row of a soft-deleted form.
		doomed := newForm(t, "Deleted form")
		doomedSubmission := submit(t, doomed, "gone@example.com", true)
		var doomedRow models.FormMailDelivery
		require.NoError(t, db.Where("submission_id = ?", doomedSubmission).Order("id").First(&doomedRow).Error)
		require.NoError(t, formService.Delete(doomed.ID))
		status, envelope = call(t, models.RoleAdmin, http.MethodPost, fmt.Sprintf("/forms/deliveries/%d/retry", doomedRow.ID))
		assert.Equal(t, http.StatusConflict, status, "a row of a soft-deleted form")
		require.NotNil(t, envelope.Error)
		assert.Equal(t, "FORM_DELIVERY_NOT_RETRYABLE", envelope.Error.Code)
	})

	t.Run("support reads but cannot retry; the customer role reaches nothing", func(t *testing.T) {
		target := idOf(failedRows[1])
		status, envelope := call(t, models.RoleSupport, http.MethodPost, fmt.Sprintf("/forms/deliveries/%d/retry", target))
		assert.Equal(t, http.StatusForbidden, status)
		require.NotNil(t, envelope.Error)
		var row models.FormMailDelivery
		require.NoError(t, db.First(&row, target).Error)
		assert.Equal(t, models.FormMailStatusFailed, row.Status, "the refused retry changed nothing")

		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/forms/deliveries"},
			{http.MethodPost, fmt.Sprintf("/forms/deliveries/%d/retry", target)},
			{http.MethodGet, fmt.Sprintf("/forms/submissions/%d", failedID)},
			{http.MethodGet, fmt.Sprintf("/forms/%d/submissions", form.ID)},
		} {
			status, _ := call(t, models.RoleCustomer, route.method, route.path)
			assert.Equal(t, http.StatusForbidden, status, "%s %s", route.method, route.path)
		}
	})
}
