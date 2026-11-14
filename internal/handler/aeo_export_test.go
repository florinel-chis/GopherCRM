package handler

// Regression roots for GET /aeo/export (#244) at the handler: parameter
// validation, roles, error mapping, log privacy, the CSV shape and formula
// guard, and the JSON body. The service is a mock; the service, repository and
// HTTP roots live in internal/service, internal/repository and tests.

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type aeoExportEnvelope struct {
	Success bool `json:"success"`
	Error   *struct {
		Code    string                 `json:"code"`
		Message string                 `json:"message"`
		Details map[string]interface{} `json:"details"`
	} `json:"error"`
}

// initAEOExportLogger installs a JSON logger, optionally writing into buf, and
// restores the previous one when the test ends.
func initAEOExportLogger(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	previous := utils.Logger
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	if buf != nil {
		utils.Logger.SetOutput(buf)
	} else {
		utils.Logger.SetOutput(&bytes.Buffer{})
	}
	t.Cleanup(func() { utils.Logger = previous })
	gin.SetMode(gin.TestMode)
}

// aeoExportRouter mounts the AEO routes as production does, with the caller's
// role set by a stand-in for the auth middleware.
func aeoExportRouter(svc service.AEOService, role models.UserRole) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(7))
		c.Set("user_role", string(role))
		c.Next()
	})
	SetupAEORoutes(router.Group(""), NewAEOHandler(svc))
	return router
}

func serveAEOExport(router *gin.Engine, rawQuery string) *httptest.ResponseRecorder {
	target := "/aeo/export"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeAEOExportEnvelope(t *testing.T, rec *httptest.ResponseRecorder) aeoExportEnvelope {
	t.Helper()
	var envelope aeoExportEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	return envelope
}

// aeoExportFixture is a window export with one answer holding two citations
// and one failed answer without citations.
func aeoExportFixture() *models.AEOExport {
	to := time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)
	from := to.AddDate(0, 0, -30)
	completed := time.Date(2026, time.November, 14, 6, 3, 41, 0, time.UTC)
	triggeredBy := uint(5)
	return &models.AEOExport{
		FormatVersion: 1,
		GeneratedAt:   time.Date(2026, time.November, 14, 10, 15, 30, 123000000, time.UTC),
		Selection:     models.AEOExportSelection{Days: 30, From: &from, To: &to},
		Runs: []models.AEOExportRun{{
			ID:            12,
			Trigger:       "manual",
			Status:        "partial",
			StartedAt:     time.Date(2026, time.November, 14, 6, 0, 0, 0, time.UTC),
			CompletedAt:   &completed,
			TotalQueries:  2,
			FailedQueries: 1,
			TriggeredByID: &triggeredBy,
		}},
		Answers: []models.AEOExportAnswer{
			{
				ID:                 88,
				RunID:              12,
				PromptID:           3,
				PromptText:         "Which CRM fits a small team?",
				Provider:           "anthropic",
				Model:              "model-a",
				CreatedAt:          time.Date(2026, time.November, 14, 6, 0, 12, 0, time.UTC),
				AnswerText:         "Acme CRM is a good fit.",
				BrandMentioned:     true,
				FirstMentionPos:    0,
				LatencyMs:          2350,
				CompetitorMentions: map[string]int{"Zeta CRM": 1, "Globex CRM": 2},
				Citations: []models.AEOExportCitation{
					{ID: 301, URL: "https://acme.example/a", Domain: "acme.example", IsOwned: true},
					{ID: 302, URL: "https://globex.example/b", Domain: "globex.example", CompetitorName: "Globex CRM"},
				},
			},
			{
				ID:                 89,
				RunID:              12,
				PromptID:           4,
				PromptText:         "Is there a self-hosted CRM?",
				Provider:           "openai",
				Model:              "model-b",
				CreatedAt:          time.Date(2026, time.November, 14, 6, 0, 30, 0, time.UTC),
				FirstMentionPos:    -1,
				LatencyMs:          60000,
				Error:              "openai: request timed out",
				CompetitorMentions: map[string]int{},
				Citations:          []models.AEOExportCitation{},
			},
		},
	}
}

func readAEOExportCSV(t *testing.T, body []byte) [][]string {
	t.Helper()
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	require.NoError(t, err)
	return records
}

func TestAEOExportHandlerValidation(t *testing.T) {
	initAEOExportLogger(t, nil)

	const canary = "ValueCanaryZ9"
	cases := []struct {
		name  string
		query url.Values
		keys  []string
	}{
		{"unknown format", url.Values{"format": {"xml" + canary}}, []string{"format"}},
		{"upper-case format", url.Values{"format": {"CSV"}}, []string{"format"}},
		{"run_id zero", url.Values{"run_id": {"0"}}, []string{"run_id"}},
		{"run_id negative", url.Values{"run_id": {"-3"}}, []string{"run_id"}},
		{"run_id text", url.Values{"run_id": {"abc" + canary}}, []string{"run_id"}},
		{"run_id fraction", url.Values{"run_id": {"1.5"}}, []string{"run_id"}},
		{"run_id overflow", url.Values{"run_id": {"99999999999"}}, []string{"run_id"}},
		{"days outside the set", url.Values{"days": {"14"}}, []string{"days"}},
		{"days zero", url.Values{"days": {"0"}}, []string{"days"}},
		{"days 365 is not clamped", url.Values{"days": {"365"}}, []string{"days"}},
		{"days text", url.Values{"days": {"week" + canary}}, []string{"days"}},
		{"run_id with days", url.Values{"run_id": {"5"}, "days": {"7"}}, []string{"run_id", "days"}},
		{"bad format and bad days", url.Values{"format": {"pdf"}, "days": {"8"}}, []string{"format", "days"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := new(mocks.AEOService)
			rec := serveAEOExport(aeoExportRouter(svc, models.RoleAdmin), tc.query.Encode())

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Empty(t, rec.Header().Get("Content-Disposition"))
			envelope := decodeAEOExportEnvelope(t, rec)
			require.NotNil(t, envelope.Error)
			assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)
			keys := make([]string, 0, len(envelope.Error.Details))
			for key := range envelope.Error.Details {
				keys = append(keys, key)
			}
			assert.ElementsMatch(t, tc.keys, keys)
			assert.NotContains(t, rec.Body.String(), canary, "the refused value is not echoed")
			for _, values := range tc.query {
				for _, value := range values {
					assert.NotContains(t, rec.Body.String(), `"`+value+`"`, "the refused value is not echoed")
				}
			}
			svc.AssertNotCalled(t, "Export", mock.Anything, mock.Anything)
		})
	}

	defaults := []struct {
		name      string
		query     string
		selection models.AEOExportSelection
		mediaType string
	}{
		{"no parameters: csv, 30 days", "", models.AEOExportSelection{Days: 30}, "text/csv; charset=utf-8"},
		{"empty values count as absent", "format=&days=&run_id=", models.AEOExportSelection{Days: 30}, "text/csv; charset=utf-8"},
		{"json without a selection", "format=json", models.AEOExportSelection{Days: 30}, "application/json; charset=utf-8"},
		{"days 7", "days=7", models.AEOExportSelection{Days: 7}, "text/csv; charset=utf-8"},
		{"days 90 json", "days=90&format=json", models.AEOExportSelection{Days: 90}, "application/json; charset=utf-8"},
		{"run_id", "run_id=12", models.AEOExportSelection{RunID: 12}, "text/csv; charset=utf-8"},
	}
	for _, tc := range defaults {
		t.Run(tc.name, func(t *testing.T) {
			svc := new(mocks.AEOService)
			svc.On("Export", mock.Anything, tc.selection).Return(aeoExportFixture(), nil).Once()
			rec := serveAEOExport(aeoExportRouter(svc, models.RoleSales), tc.query)
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, tc.mediaType, rec.Header().Get("Content-Type"))
			svc.AssertExpectations(t)
		})
	}
}

func TestAEOExportRoles(t *testing.T) {
	initAEOExportLogger(t, nil)

	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport} {
		t.Run(string(role), func(t *testing.T) {
			svc := new(mocks.AEOService)
			svc.On("Export", mock.Anything, models.AEOExportSelection{Days: 30}).Return(aeoExportFixture(), nil).Once()
			rec := serveAEOExport(aeoExportRouter(svc, role), "")
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.NotEmpty(t, rec.Header().Get("Content-Disposition"))
			svc.AssertExpectations(t)
		})
	}

	t.Run(string(models.RoleCustomer), func(t *testing.T) {
		svc := new(mocks.AEOService)
		rec := serveAEOExport(aeoExportRouter(svc, models.RoleCustomer), "format=json&run_id=12")
		assert.Equal(t, http.StatusForbidden, rec.Code)
		envelope := decodeAEOExportEnvelope(t, rec)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
		assert.Empty(t, rec.Header().Get("Content-Disposition"))
		svc.AssertNotCalled(t, "Export", mock.Anything, mock.Anything)
	})
}

func TestAEOExportErrorMapping(t *testing.T) {
	initAEOExportLogger(t, nil)

	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unknown run", fmt.Errorf("aeo run 12 not found: %w", apperrors.ErrNotFound), http.StatusNotFound, utils.ErrCodeNotFound},
		{"too large", service.ErrAEOExportTooLarge, http.StatusUnprocessableEntity, "EXPORT_TOO_LARGE"},
		{"wrapped too large", fmt.Errorf("export: %w", service.ErrAEOExportTooLarge), http.StatusUnprocessableEntity, "EXPORT_TOO_LARGE"},
		{"database failure", errors.New("database is locked"), http.StatusInternalServerError, utils.ErrCodeInternal},
		{"unavailable reads", service.ErrAEOExportUnavailable, http.StatusInternalServerError, utils.ErrCodeInternal},
	}
	for _, tc := range cases {
		for _, format := range []string{"csv", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				svc := new(mocks.AEOService)
				svc.On("Export", mock.Anything, models.AEOExportSelection{RunID: 12}).Return(nil, tc.err).Once()
				rec := serveAEOExport(aeoExportRouter(svc, models.RoleAdmin), "run_id=12&format="+format)

				assert.Equal(t, tc.status, rec.Code)
				assert.Empty(t, rec.Header().Get("Content-Disposition"), "an error is never an attachment")
				assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
				envelope := decodeAEOExportEnvelope(t, rec)
				assert.False(t, envelope.Success)
				require.NotNil(t, envelope.Error)
				assert.Equal(t, tc.code, envelope.Error.Code)
				if tc.code == "EXPORT_TOO_LARGE" {
					assert.EqualValues(t, 10000, envelope.Error.Details["max_answers"])
					assert.Equal(t, 10000, service.AEOExportMaxAnswers)
				}
				if tc.status == http.StatusInternalServerError {
					assert.NotContains(t, rec.Body.String(), "database is locked", "the cause is never echoed")
				}
				svc.AssertExpectations(t)
			})
		}
	}
}

func TestAEOExportHandlerLogsOmitPersonalData(t *testing.T) {
	var buf bytes.Buffer
	initAEOExportLogger(t, &buf)

	const (
		promptCanary     = "PromptCanaryQ1"
		answerCanary     = "AnswerCanaryW2"
		urlCanary        = "https://url-canary-e3.example/path"
		domainCanary     = "domain-canary-r4.example"
		competitorCanary = "CompetitorCanaryT5"
		brandCanary      = "BrandCanaryY6"
		modelCanary      = "ModelCanaryU7"
		queryCanary      = "QueryCanaryI8"
	)
	content := aeoExportFixture()
	content.Answers[0].PromptText = promptCanary
	content.Answers[0].AnswerText = answerCanary + " " + brandCanary
	content.Answers[0].Model = modelCanary
	content.Answers[0].CompetitorMentions = map[string]int{competitorCanary: 1}
	content.Answers[0].Citations[0].URL = urlCanary
	content.Answers[0].Citations[0].Domain = domainCanary
	content.Answers[0].Citations[1].CompetitorName = competitorCanary

	run := func(err error, export *models.AEOExport, query string) int {
		svc := new(mocks.AEOService)
		svc.On("Export", mock.Anything, mock.Anything).Return(export, err).Maybe()
		return serveAEOExport(aeoExportRouter(svc, models.RoleAdmin), query).Code
	}

	// Validation refusals with canaries in every raw value.
	assert.Equal(t, http.StatusBadRequest, run(nil, nil, "format="+queryCanary))
	assert.Equal(t, http.StatusBadRequest, run(nil, nil, "run_id="+queryCanary))
	assert.Equal(t, http.StatusBadRequest, run(nil, nil, "days="+queryCanary))
	assert.Equal(t, http.StatusBadRequest, run(nil, nil, "run_id=1&days="+queryCanary))
	// Mapped and unmapped service errors whose text carries content.
	assert.Equal(t, http.StatusNotFound, run(fmt.Errorf("prompt %s: %w", promptCanary, apperrors.ErrNotFound), nil, "run_id=12"))
	assert.Equal(t, http.StatusUnprocessableEntity, run(fmt.Errorf("%s: %w", domainCanary, service.ErrAEOExportTooLarge), nil, "days=90"))
	assert.Equal(t, http.StatusInternalServerError, run(fmt.Errorf("read of %s %s failed", urlCanary, modelCanary), nil, "format=json"))
	// Successful exports in both formats.
	assert.Equal(t, http.StatusOK, run(nil, content, "format=csv&days=7"))
	assert.Equal(t, http.StatusOK, run(nil, content, "format=json&run_id=12"))

	output := buf.String()
	require.NotEmpty(t, output)
	assert.Contains(t, output, "AEOHandler.Export")
	assert.Contains(t, output, `"operation":"aeo_export"`)
	assert.Contains(t, output, `"error_code":"VALIDATION_ERROR"`)
	assert.Contains(t, output, `"error_code":"EXPORT_TOO_LARGE"`)
	assert.Contains(t, output, `"error_code":"NOT_FOUND"`)
	assert.Contains(t, output, `"error_code":"INTERNAL_ERROR"`)
	for _, needle := range []string{
		promptCanary, answerCanary, urlCanary, "url-canary", domainCanary, competitorCanary, brandCanary, modelCanary, queryCanary,
	} {
		assert.NotContains(t, output, needle, "no handler log line carries export content or a raw query value")
	}
}

func TestAEOExportCSVShape(t *testing.T) {
	initAEOExportLogger(t, nil)

	assert.Equal(t, []string{
		"run_id", "run_trigger", "run_status", "run_started_at",
		"answer_id", "answer_created_at", "prompt_id", "prompt_text",
		"provider", "model", "brand_mentioned", "first_mention_pos", "latency_ms",
		"error", "competitor_mentions",
		"citation_id", "citation_url", "citation_domain", "citation_is_owned", "citation_competitor_name",
	}, aeoExportCSVColumns)
	assert.NotContains(t, aeoExportCSVColumns, "answer_text")

	t.Run("window", func(t *testing.T) {
		svc := new(mocks.AEOService)
		svc.On("Export", mock.Anything, models.AEOExportSelection{Days: 30}).Return(aeoExportFixture(), nil).Once()
		rec := serveAEOExport(aeoExportRouter(svc, models.RoleSupport), "format=csv&days=30")

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))
		// selection.to is 2026-11-15T00:00Z, so the last day of the window is
		// 2026-11-14, whatever the wall clock says.
		assert.Equal(t, "attachment; filename=aeo-export-30d-2026-11-14.csv", rec.Header().Get("Content-Disposition"))
		assert.NotContains(t, rec.Body.String(), "Acme CRM is a good fit.", "no answer text in the CSV")

		records := readAEOExportCSV(t, rec.Body.Bytes())
		require.Len(t, records, 4, "header, two citation rows, one row for the answer without citations")
		assert.Equal(t, aeoExportCSVColumns, records[0])
		assert.Equal(t, []string{
			"12", "manual", "partial", "2026-11-14T06:00:00Z",
			"88", "2026-11-14T06:00:12Z", "3", "Which CRM fits a small team?",
			"anthropic", "model-a", "true", "0", "2350",
			"", "Globex CRM=2;Zeta CRM=1",
			"301", "https://acme.example/a", "acme.example", "true", "",
		}, records[1])
		assert.Equal(t, []string{
			"12", "manual", "partial", "2026-11-14T06:00:00Z",
			"88", "2026-11-14T06:00:12Z", "3", "Which CRM fits a small team?",
			"anthropic", "model-a", "true", "0", "2350",
			"", "Globex CRM=2;Zeta CRM=1",
			"302", "https://globex.example/b", "globex.example", "false", "Globex CRM",
		}, records[2])
		assert.Equal(t, []string{
			"12", "manual", "partial", "2026-11-14T06:00:00Z",
			"89", "2026-11-14T06:00:30Z", "4", "Is there a self-hosted CRM?",
			"openai", "model-b", "false", "-1", "60000",
			"openai: request timed out", "",
			"", "", "", "", "",
		}, records[3])
		svc.AssertExpectations(t)
	})

	t.Run("run", func(t *testing.T) {
		export := aeoExportFixture()
		export.Selection = models.AEOExportSelection{RunID: 12}
		svc := new(mocks.AEOService)
		svc.On("Export", mock.Anything, models.AEOExportSelection{RunID: 12}).Return(export, nil).Once()
		rec := serveAEOExport(aeoExportRouter(svc, models.RoleAdmin), "run_id=12")

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "attachment; filename=aeo-export-run-12.csv", rec.Header().Get("Content-Disposition"))
		assert.Len(t, readAEOExportCSV(t, rec.Body.Bytes()), 4)
		svc.AssertExpectations(t)
	})

	t.Run("empty selection", func(t *testing.T) {
		to := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
		from := to.AddDate(0, 0, -7)
		export := &models.AEOExport{FormatVersion: 1, Selection: models.AEOExportSelection{Days: 7, From: &from, To: &to}}
		svc := new(mocks.AEOService)
		svc.On("Export", mock.Anything, models.AEOExportSelection{Days: 7}).Return(export, nil).Once()
		rec := serveAEOExport(aeoExportRouter(svc, models.RoleAdmin), "days=7")

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "attachment; filename=aeo-export-7d-2025-12-31.csv", rec.Header().Get("Content-Disposition"))
		assert.Equal(t, [][]string{aeoExportCSVColumns}, readAEOExportCSV(t, rec.Body.Bytes()))
	})
}

func TestAEOExportCSVFormulaGuard(t *testing.T) {
	for _, input := range []string{
		"=1+1",
		"@SUM(A1)",
		"\tcmd",
		"\rcmd",
		"\ncmd",
		"+cmd|' /C calc'!A0",
		"-2+3+cmd",
		"＝1+1",
		"＋cmd",
		"－cmd",
		"－12",
		"＠SUM(A1)",
	} {
		assert.Equal(t, "'"+input, aeoCSVSafeField(input), "%q", input)
	}
	for _, unchanged := range []string{"", "plain text", "+44 20 7946 0000", "-12.5", "https://acme.example", "a=b", "Acme ＝ CRM"} {
		assert.Equal(t, unchanged, aeoCSVSafeField(unchanged), "%q", unchanged)
	}

	// csvSafeField, and so the customer and lead exports, keep their rules:
	// no guard for a leading LF or the full-width forms.
	assert.Equal(t, "'=1", csvSafeField("=1"))
	assert.Equal(t, "\ncmd", csvSafeField("\ncmd"))
	assert.Equal(t, "＝cmd", csvSafeField("＝cmd"))
	assert.Equal(t, "+44 20", csvSafeField("+44 20"))
	customer := models.Customer{FirstName: "＝A", Notes: "\nnote"}
	customerRecord := customerExportRecord(&customer)
	assert.Equal(t, "＝A", customerRecord[1])
	assert.Equal(t, "\nnote", customerRecord[7])
	lead := models.Lead{FirstName: "＠B", Notes: "\nnote"}
	leadRecord := leadExportRecord(&lead)
	assert.Equal(t, "＠B", leadRecord[1])
	assert.Equal(t, "\nnote", leadRecord[11])

	// Every free-text column goes through aeoCSVSafeField.
	freeText := []string{
		"prompt_text", "provider", "model", "error", "competitor_mentions",
		"citation_url", "citation_domain", "citation_competitor_name",
	}
	column := map[string]int{}
	for i, name := range aeoExportCSVColumns {
		column[name] = i
	}
	for _, prefix := range []string{"=", "@", "\t", "\r", "\n", "+x", "-x", "＝", "＋", "－", "＠"} {
		value := prefix + "HYPERLINK(1)"
		export := aeoExportFixture()
		answer := &export.Answers[0]
		answer.PromptText, answer.Provider, answer.Model, answer.Error = value, value, value, value
		answer.CompetitorMentions = map[string]int{value: 3}
		for i := range answer.Citations {
			answer.Citations[i].URL = value
			answer.Citations[i].Domain = value
			answer.Citations[i].CompetitorName = value
		}

		records := aeoExportCSVRecords(export)
		require.Len(t, records, 3)
		for _, row := range records[:2] {
			for _, name := range freeText {
				cell := row[column[name]]
				assert.True(t, strings.HasPrefix(cell, "'"+value), "column %s with prefix %q: %q", name, prefix, cell)
			}
		}

		// The rendered file round-trips the guarded cells (a bare CR is left
		// out: CSV readers normalise line endings inside quoted fields).
		body, err := aeoExportCSV(export)
		require.NoError(t, err)
		if prefix != "\r" {
			parsed := readAEOExportCSV(t, body)
			assert.Equal(t, "'"+value, parsed[1][column["prompt_text"]])
		}
	}
}

func TestAEOExportJSON(t *testing.T) {
	initAEOExportLogger(t, nil)

	t.Run("window", func(t *testing.T) {
		fixture := aeoExportFixture()
		svc := new(mocks.AEOService)
		svc.On("Export", mock.Anything, models.AEOExportSelection{Days: 30}).Return(fixture, nil).Once()
		rec := serveAEOExport(aeoExportRouter(svc, models.RoleAdmin), "format=json")

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
		assert.Equal(t, "attachment; filename=aeo-export-30d-2026-11-14.json", rec.Header().Get("Content-Disposition"))

		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
		for _, envelopeKey := range []string{"success", "data", "meta"} {
			assert.NotContains(t, raw, envelopeKey, "the body is the export, not the envelope")
		}
		for _, key := range []string{"format_version", "generated_at", "selection", "runs", "answers"} {
			assert.Contains(t, raw, key)
		}
		for _, key := range []string{"owner_instance", "heartbeat_at", "running_lock", "schedule_slot"} {
			assert.NotContains(t, rec.Body.String(), key, "no ownership column anywhere in the body")
		}

		var body models.AEOExport
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, 1, body.FormatVersion)
		assert.True(t, fixture.GeneratedAt.Equal(body.GeneratedAt), "generated_at is the service's value")
		assert.Equal(t, `"2026-11-14T10:15:30.123Z"`, string(raw["generated_at"]))
		assert.Equal(t, 30, body.Selection.Days)
		require.NotNil(t, body.Selection.To)
		assert.True(t, fixture.Selection.To.Equal(*body.Selection.To))
		require.Len(t, body.Runs, 1)
		assert.Equal(t, uint(12), body.Runs[0].ID)
		require.Len(t, body.Answers, 2)
		assert.Equal(t, "Acme CRM is a good fit.", body.Answers[0].AnswerText)
		assert.Equal(t, map[string]int{"Zeta CRM": 1, "Globex CRM": 2}, body.Answers[0].CompetitorMentions)
		require.Len(t, body.Answers[0].Citations, 2)
		assert.Equal(t, uint(302), body.Answers[0].Citations[1].ID)

		var answers []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw["answers"], &answers))
		assert.Equal(t, "{}", string(answers[1]["competitor_mentions"]), "competitor_mentions is an object")
		assert.Equal(t, "[]", string(answers[1]["citations"]), "citations is an array")
		svc.AssertExpectations(t)
	})

	t.Run("run", func(t *testing.T) {
		export := aeoExportFixture()
		export.Selection = models.AEOExportSelection{RunID: 12}
		svc := new(mocks.AEOService)
		svc.On("Export", mock.Anything, models.AEOExportSelection{RunID: 12}).Return(export, nil).Once()
		rec := serveAEOExport(aeoExportRouter(svc, models.RoleSales), "format=json&run_id=12")

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "attachment; filename=aeo-export-run-12.json", rec.Header().Get("Content-Disposition"))
		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
		assert.JSONEq(t, `{"run_id":12}`, string(raw["selection"]))
	})

	t.Run("empty selection has empty arrays", func(t *testing.T) {
		to := time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)
		from := to.AddDate(0, 0, -90)
		export := &models.AEOExport{FormatVersion: 1, Selection: models.AEOExportSelection{Days: 90, From: &from, To: &to}}
		svc := new(mocks.AEOService)
		svc.On("Export", mock.Anything, models.AEOExportSelection{Days: 90}).Return(export, nil).Once()
		rec := serveAEOExport(aeoExportRouter(svc, models.RoleAdmin), "format=json&days=90")

		require.Equal(t, http.StatusOK, rec.Code)
		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
		assert.Equal(t, "[]", string(raw["runs"]))
		assert.Equal(t, "[]", string(raw["answers"]))
	})
}
