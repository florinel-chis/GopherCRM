package tests

// The retention purge over HTTP (issue #217): the real routes behind the real JWT
// middleware, the real services and repositories on SQLite with the production
// one-connection pool. A purged submission disappears from every route that could
// show it, and its confirmation link gets the generic invalid-link page.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

const formRetentionAPISecret = "form-retention-api-regression-secret-32-bytes"

type formRetentionAPIMailer struct {
	mu     sync.Mutex
	bodies []string
}

func (m *formRetentionAPIMailer) SendPasswordReset(string, string) error { return nil }

func (m *formRetentionAPIMailer) Send(_, _, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bodies = append(m.bodies, body)
	return nil
}

func (m *formRetentionAPIMailer) lastToken(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(t, m.bodies)
	body := m.bodies[len(m.bodies)-1]
	index := strings.Index(body, "token=")
	require.NotEqual(t, -1, index, "the confirmation mail carries the link")
	raw := body[index+len("token="):]
	if end := strings.IndexAny(raw, " \n\t"); end != -1 {
		raw = raw[:end]
	}
	token, err := url.QueryUnescape(raw)
	require.NoError(t, err)
	return token
}

type formRetentionMeta struct {
	Total int64 `json:"total"`
}

type formRetentionError struct {
	Code string `json:"code"`
}

type formRetentionEnvelope struct {
	Success bool                `json:"success"`
	Data    json.RawMessage     `json:"data"`
	Meta    formRetentionMeta   `json:"meta"`
	Error   *formRetentionError `json:"error"`
}

func TestFormRetentionAPISQLite(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", formRetentionAPISecret)

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:form_retention_api_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.APIKey{}, &models.Company{}, &models.Customer{}, &models.Lead{},
		&models.Form{}, &models.FormSubmission{}, &models.FormConfirmationToken{},
	))

	userRepo := repository.NewUserRepository(db)
	formRepo := repository.NewFormRepository(db)
	txManager := utils.NewTransactionManager(db)
	authService := service.NewAuthService(userRepo, repository.NewAPIKeyRepository(db), config.JWTConfig{Secret: formRetentionAPISecret, ExpiryHours: 1})
	mail := &formRetentionAPIMailer{}
	formsCfg := config.FormsConfig{PublicBaseURL: "https://crm.example.test"}
	formService := service.NewFormService(formRepo, repository.NewLeadRepository(db), userRepo, mail, txManager, formsCfg, "/api/v1")

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

	admin := &models.User{Email: "admin@form-retention.test", FirstName: "Api", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, service.NewUserService(userRepo).Register(admin, "password123"))
	bearer, err := authService.Login(admin.Email, "password123")
	require.NoError(t, err)

	get := func(path string) formRetentionEnvelope {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1"+path, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var envelope formRetentionEnvelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
		return envelope
	}
	listIDs := func(path string) ([]uint, int64) {
		t.Helper()
		envelope := get(path)
		require.True(t, envelope.Success)
		var rows []struct {
			ID uint `json:"id"`
		}
		require.NoError(t, json.Unmarshal(envelope.Data, &rows))
		ids := make([]uint, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return ids, envelope.Meta.Total
	}
	submissionCount := func(formID uint) int64 {
		t.Helper()
		envelope := get("/forms")
		require.True(t, envelope.Success)
		var rows []struct {
			ID              uint  `json:"id"`
			SubmissionCount int64 `json:"submission_count"`
		}
		require.NoError(t, json.Unmarshal(envelope.Data, &rows))
		for _, row := range rows {
			if row.ID == formID {
				return row.SubmissionCount
			}
		}
		t.Fatalf("form %d is not listed", formID)
		return 0
	}

	form := &models.Form{
		Name:           "Retention form",
		Status:         models.FormStatusPublished,
		Fields:         []models.FormFieldDef{{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true}},
		DoubleOptIn:    true,
		CreateLead:     true,
		DefaultOwnerID: admin.ID,
	}
	require.NoError(t, formService.Create(form, admin.ID))

	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	seed := func(status models.FormSubmissionStatus, createdAt time.Time) uint {
		t.Helper()
		row := &models.FormSubmission{
			FormID: form.ID,
			Status: status,
			Email:  "visitor@example.com",
			Data:   map[string]string{"email": "visitor@example.com"},
		}
		row.CreatedAt = createdAt
		require.NoError(t, db.Create(row).Error)
		return row.ID
	}

	purgedSpam := seed(models.FormSubmissionSpam, old)
	keptSpam := seed(models.FormSubmissionSpam, time.Now().UTC())
	keptConfirmed := seed(models.FormSubmissionConfirmed, old)

	// The pending row comes from a real submission, so its link is a real one.
	_, err = formService.SubmitPublic(context.Background(), form.PublicID, &service.PublicSubmissionRequest{
		Values:    map[string]string{"email": "late@example.com"},
		Challenge: forms.NewChallenge([]byte(formRetentionAPISecret), time.Now().Add(-30*time.Second)),
	}, service.SubmissionMeta{IP: "203.0.113.7"})
	require.NoError(t, err)
	rawToken := mail.lastToken(t)
	var purgedPending models.FormSubmission
	require.NoError(t, db.Where("email = ?", "late@example.com").First(&purgedPending).Error)
	require.NoError(t, db.Exec("UPDATE form_submissions SET created_at = ? WHERE id = ?", old, purgedPending.ID).Error)
	require.NoError(t, db.Exec("UPDATE form_confirmation_tokens SET expires_at = ? WHERE submission_id = ?", time.Now().Add(-time.Hour), purgedPending.ID).Error)

	// Everything is visible before the purge, so the assertions below prove the
	// purge is what took the rows away.
	ids, total := listIDs(fmt.Sprintf("/forms/%d/submissions", form.ID))
	assert.ElementsMatch(t, []uint{purgedSpam, keptSpam, keptConfirmed, purgedPending.ID}, ids)
	assert.EqualValues(t, 4, total)
	ids, total = listIDs(fmt.Sprintf("/forms/%d/submissions?status=spam", form.ID))
	assert.ElementsMatch(t, []uint{purgedSpam, keptSpam}, ids)
	assert.EqualValues(t, 2, total)
	assert.EqualValues(t, 4, submissionCount(form.ID))
	assert.True(t, get(fmt.Sprintf("/forms/submissions/%d", purgedSpam)).Success)

	result, err := service.NewFormSubmissionPurger(formRepo, txManager, 72*time.Hour).Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, service.FormPurgeResult{Spam: 1, Pending: 1, Tokens: 1, Chunks: 2}, result)

	ids, total = listIDs(fmt.Sprintf("/forms/%d/submissions", form.ID))
	assert.ElementsMatch(t, []uint{keptSpam, keptConfirmed}, ids, "the list omits the purged rows")
	assert.EqualValues(t, 2, total, "and so does its total")

	ids, total = listIDs(fmt.Sprintf("/forms/%d/submissions?status=spam", form.ID))
	assert.Equal(t, []uint{keptSpam}, ids)
	assert.EqualValues(t, 1, total)

	assert.EqualValues(t, 2, submissionCount(form.ID), "the form list reports the reduced count")

	for _, id := range []uint{purgedSpam, purgedPending.ID} {
		envelope := get(fmt.Sprintf("/forms/submissions/%d", id))
		assert.False(t, envelope.Success)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/confirm", strings.NewReader(url.Values{"token": {rawToken}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, rec.Body.String(), "This link is no longer valid")

	var leads int64
	require.NoError(t, db.Unscoped().Model(&models.Lead{}).Count(&leads).Error)
	assert.Zero(t, leads, "the purged link created no lead")
}
