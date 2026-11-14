package tests

// GET /aeo/export over HTTP (issue #244): the real JWT middleware, role guard,
// handler, service and repository on a SQLite file. A seeded run with answers
// and citations exports the same rows as CSV and JSON, for run_id and for days;
// a customer gets 403 and an unknown run 404; the CSV row count is the
// citation count plus the answers without citations.

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
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

const aeoExportAPISecret = "aeo-export-api-regression-secret-32-bytes!"

var aeoExportAPIColumns = []string{
	"run_id", "run_trigger", "run_status", "run_started_at",
	"answer_id", "answer_created_at", "prompt_id", "prompt_text",
	"provider", "model", "brand_mentioned", "first_mention_pos", "latency_ms",
	"error", "competitor_mentions",
	"citation_id", "citation_url", "citation_domain", "citation_is_owned", "citation_competitor_name",
}

// aeoExportRow identifies one exported row: the answer and its citation (0
// for an answer without citations).
type aeoExportRow struct {
	AnswerID   uint
	CitationID uint
}

func TestAEOExportAPISQLite(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", aeoExportAPISecret)
	gin.SetMode(gin.TestMode)

	dsn := filepath.Join(t.TempDir(), "aeo-export-api.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:  logger.Discard,
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.APIKey{},
		&models.AEOProfile{}, &models.AEOPrompt{}, &models.AEORun{}, &models.AEOAnswer{}, &models.AEOCitation{},
	))

	// The service clock is pinned, so the windows do not depend on the day the
	// test runs: days=7 is [2026-11-08, 2026-11-15).
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)

	livePrompt := models.AEOPrompt{Text: "Which CRM fits a small team?", IsActive: true}
	require.NoError(t, db.Create(&livePrompt).Error)
	deletedPrompt := models.AEOPrompt{Text: "Is a self-hosted CRM worth it?", IsActive: true}
	require.NoError(t, db.Create(&deletedPrompt).Error)
	require.NoError(t, db.Delete(&deletedPrompt).Error)

	completedAt := now.Add(-20 * time.Minute)
	run := models.AEORun{Trigger: models.AEOTriggerManual, Status: models.AEORunStatusPartial,
		StartedAt: now.Add(-2 * time.Hour), CompletedAt: &completedAt, TotalQueries: 3, FailedQueries: 1}
	require.NoError(t, db.Create(&run).Error)
	oldRun := models.AEORun{Trigger: models.AEOTriggerScheduled, Status: models.AEORunStatusCompleted,
		StartedAt: now.AddDate(0, 0, -40), TotalQueries: 1}
	require.NoError(t, db.Create(&oldRun).Error)

	repo := repository.NewAEORepository(db)
	addAnswer := func(runID, promptID uint, createdAt time.Time, errText string, citations ...models.AEOCitation) models.AEOAnswer {
		answer := models.AEOAnswer{
			RunID:              runID,
			PromptID:           promptID,
			Provider:           "anthropic",
			Model:              "model-a",
			AnswerText:         fmt.Sprintf("Answer at %s naming Acme CRM.", createdAt.Format(time.RFC3339)),
			BrandMentioned:     errText == "",
			FirstMentionPos:    -1,
			Error:              errText,
			CompetitorMentions: map[string]int{"Globex CRM": 1},
		}
		answer.CreatedAt = createdAt
		require.NoError(t, repo.CreateAnswerWithCitations(&answer, citations))
		return answer
	}
	cite := func(url, domain string, owned bool, competitor string) models.AEOCitation {
		return models.AEOCitation{URL: url, Domain: domain, IsOwned: owned, CompetitorName: competitor}
	}

	a1 := addAnswer(run.ID, livePrompt.ID, now.Add(-90*time.Minute), "",
		cite("https://acme.example/pricing", "acme.example", true, ""),
		cite("https://globex.example/vs", "globex.example", false, "Globex CRM"))
	a2 := addAnswer(run.ID, deletedPrompt.ID, now.Add(-80*time.Minute), "openai: request timed out")
	a3 := addAnswer(run.ID, livePrompt.ID, now.Add(-70*time.Minute), "",
		cite("=HYPERLINK(\"https://evil.example\")", "evil.example", false, ""),
		cite("https://gone.example/x", "gone.example", false, ""))
	// The second citation of a3 is soft-deleted and never exported.
	require.NoError(t, db.Delete(&models.AEOCitation{}, a3.Citations[1].ID).Error)
	oldAnswer := addAnswer(oldRun.ID, livePrompt.ID, now.AddDate(0, 0, -40), "",
		cite("https://old.example/a", "old.example", false, ""))

	runtime := service.NewAEORuntime(repo, nil, nil, utils.NewTransactionManager(db),
		service.WithAEOClock(func() time.Time { return now }))
	authService := service.NewAuthService(repository.NewUserRepository(db), repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: aeoExportAPISecret, ExpiryHours: 1})

	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupAEORoutes(protected, handler.NewAEOHandler(runtime.Service()))

	userService := service.NewUserService(repository.NewUserRepository(db))
	bearers := map[models.UserRole]string{}
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer} {
		user := &models.User{Email: string(role) + "@aeo-export.test", FirstName: "Api", LastName: "User", Role: role, IsActive: true}
		require.NoError(t, userService.Register(user, "password123"))
		bearer, err := authService.Login(user.Email, "password123")
		require.NoError(t, err)
		bearers[role] = bearer
	}

	get := func(role models.UserRole, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/aeo/export?"+query, nil)
		req.Header.Set("Authorization", "Bearer "+bearers[role])
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	csvRows := func(rec *httptest.ResponseRecorder) []aeoExportRow {
		t.Helper()
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))
		records, err := csv.NewReader(bytes.NewReader(rec.Body.Bytes())).ReadAll()
		require.NoError(t, err)
		require.NotEmpty(t, records)
		assert.Equal(t, aeoExportAPIColumns, records[0])
		rows := make([]aeoExportRow, 0, len(records)-1)
		for _, record := range records[1:] {
			answerID, err := strconv.ParseUint(record[4], 10, 64)
			require.NoError(t, err)
			row := aeoExportRow{AnswerID: uint(answerID)}
			if record[15] != "" {
				citationID, err := strconv.ParseUint(record[15], 10, 64)
				require.NoError(t, err)
				row.CitationID = uint(citationID)
			}
			rows = append(rows, row)
		}
		return rows
	}
	jsonExport := func(rec *httptest.ResponseRecorder) (models.AEOExport, []aeoExportRow) {
		t.Helper()
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
		for _, key := range []string{"owner_instance", "heartbeat_at", "running_lock", "schedule_slot", `"success"`} {
			assert.NotContains(t, rec.Body.String(), key)
		}
		var export models.AEOExport
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &export))
		rows := []aeoExportRow{}
		withoutCitations, citations := 0, 0
		for _, answer := range export.Answers {
			if len(answer.Citations) == 0 {
				withoutCitations++
				rows = append(rows, aeoExportRow{AnswerID: answer.ID})
			}
			for _, citation := range answer.Citations {
				citations++
				rows = append(rows, aeoExportRow{AnswerID: answer.ID, CitationID: citation.ID})
			}
		}
		require.Equal(t, citations+withoutCitations, len(rows))
		return export, rows
	}

	runRows := []aeoExportRow{
		{AnswerID: a1.ID, CitationID: a1.Citations[0].ID},
		{AnswerID: a1.ID, CitationID: a1.Citations[1].ID},
		{AnswerID: a2.ID},
		{AnswerID: a3.ID, CitationID: a3.Citations[0].ID},
	}

	t.Run("run export as CSV and JSON", func(t *testing.T) {
		csvRec := get(models.RoleAdmin, fmt.Sprintf("run_id=%d", run.ID))
		assert.Equal(t, fmt.Sprintf("attachment; filename=aeo-export-run-%d.csv", run.ID), csvRec.Header().Get("Content-Disposition"))
		fromCSV := csvRows(csvRec)
		assert.Equal(t, runRows, fromCSV, "3 live citations plus 1 answer without citations")

		jsonRec := get(models.RoleSales, fmt.Sprintf("run_id=%d&format=json", run.ID))
		assert.Equal(t, fmt.Sprintf("attachment; filename=aeo-export-run-%d.json", run.ID), jsonRec.Header().Get("Content-Disposition"))
		export, fromJSON := jsonExport(jsonRec)
		assert.Equal(t, fromCSV, fromJSON, "CSV and JSON export the same rows")
		assert.Equal(t, 1, export.FormatVersion)
		assert.True(t, now.Equal(export.GeneratedAt))
		assert.Equal(t, run.ID, export.Selection.RunID)
		require.Len(t, export.Runs, 1)
		assert.Equal(t, models.AEORunStatusPartial, export.Runs[0].Status)
		require.Len(t, export.Answers, 3)
		assert.Equal(t, "Is a self-hosted CRM worth it?", export.Answers[1].PromptText, "a deleted prompt keeps its text")
		assert.Equal(t, a1.AnswerText, export.Answers[0].AnswerText)
		assert.Equal(t, map[string]int{"Globex CRM": 1}, export.Answers[0].CompetitorMentions)

		// The formula in a citation URL is neutralised in the CSV only.
		assert.Contains(t, csvRec.Body.String(), `'=HYPERLINK(`)
		assert.Equal(t, `=HYPERLINK("https://evil.example")`, export.Answers[2].Citations[0].URL)
		assert.NotContains(t, csvRec.Body.String(), a1.AnswerText, "no answer text in the CSV")
	})

	t.Run("window export as CSV and JSON", func(t *testing.T) {
		csvRec := get(models.RoleSupport, "days=7")
		assert.Equal(t, "attachment; filename=aeo-export-7d-2026-11-14.csv", csvRec.Header().Get("Content-Disposition"))
		fromCSV := csvRows(csvRec)
		assert.Equal(t, runRows, fromCSV, "the old run's answer is outside 7 days")

		export, fromJSON := jsonExport(get(models.RoleSupport, "days=7&format=json"))
		assert.Equal(t, fromCSV, fromJSON)
		assert.Equal(t, 7, export.Selection.Days)
		require.NotNil(t, export.Selection.From)
		require.NotNil(t, export.Selection.To)
		assert.True(t, time.Date(2026, time.November, 8, 0, 0, 0, 0, time.UTC).Equal(*export.Selection.From))
		assert.True(t, time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC).Equal(*export.Selection.To))

		wide, wideRows := jsonExport(get(models.RoleAdmin, "days=90&format=json"))
		assert.Equal(t, append([]aeoExportRow{{AnswerID: oldAnswer.ID, CitationID: oldAnswer.Citations[0].ID}}, runRows...), wideRows)
		require.Len(t, wide.Runs, 2)
		assert.Equal(t, run.ID, wide.Runs[0].ID)
		assert.Equal(t, oldRun.ID, wide.Runs[1].ID)

		// The default selection is 30 days, as CSV.
		defaultRec := get(models.RoleAdmin, "")
		assert.Equal(t, "attachment; filename=aeo-export-30d-2026-11-14.csv", defaultRec.Header().Get("Content-Disposition"))
		assert.Equal(t, runRows, csvRows(defaultRec))
	})

	t.Run("refusals use the envelope", func(t *testing.T) {
		type envelope struct {
			Success bool `json:"success"`
			Error   struct {
				Code    string                 `json:"code"`
				Details map[string]interface{} `json:"details"`
			} `json:"error"`
		}
		check := func(rec *httptest.ResponseRecorder, status int, code string) envelope {
			t.Helper()
			assert.Equal(t, status, rec.Code, rec.Body.String())
			assert.Empty(t, rec.Header().Get("Content-Disposition"))
			var body envelope
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.False(t, body.Success)
			assert.Equal(t, code, body.Error.Code)
			return body
		}

		check(get(models.RoleCustomer, fmt.Sprintf("run_id=%d", run.ID)), http.StatusForbidden, "FORBIDDEN")
		check(get(models.RoleAdmin, "run_id=999999"), http.StatusNotFound, "NOT_FOUND")
		check(get(models.RoleAdmin, "run_id=999999&format=json"), http.StatusNotFound, "NOT_FOUND")
		refused := check(get(models.RoleAdmin, "days=14"), http.StatusBadRequest, "VALIDATION_ERROR")
		assert.Contains(t, refused.Error.Details, "days")
		refused = check(get(models.RoleAdmin, fmt.Sprintf("run_id=%d&days=7", run.ID)), http.StatusBadRequest, "VALIDATION_ERROR")
		assert.Contains(t, refused.Error.Details, "run_id")
		assert.Contains(t, refused.Error.Details, "days")

		req := httptest.NewRequest(http.MethodGet, "/api/v1/aeo/export", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}
