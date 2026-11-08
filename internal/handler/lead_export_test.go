package handler

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/service/mocks"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// leadExportRouter mounts LeadHandler.Export at /leads/export behind a stub
// that sets the caller's id and role, as the auth middleware does.
func leadExportRouter(svc *mocks.LeadService, userID uint, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("request_id", "lead-export-request")
		c.Set("user_id", userID)
		c.Set("user_role", role)
		c.Next()
	})
	router.GET("/leads/export", NewLeadHandler(svc).Export)
	return router
}

func leadExportGet(router *gin.Engine, query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/leads/export"+query, nil))
	return rec
}

// leadExportAssertEnvelope checks a JSON error envelope and that nothing of a
// CSV attachment was announced.
func leadExportAssertEnvelope(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assert.Equal(t, status, rec.Code)
	assert.NotContains(t, rec.Header().Get("Content-Type"), "text/csv")
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	assert.Empty(t, rec.Header().Get("Content-Disposition"))
	var response utils.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), rec.Body.String())
	assert.False(t, response.Success)
	require.NotNil(t, response.Error)
	assert.Equal(t, code, response.Error.Code)
}

func TestLeadExportHandler(t *testing.T) {
	admin := service.LeadViewer{UserID: 1, Role: models.RoleAdmin}

	t.Run("service_failure_is_the_json_500_envelope", func(t *testing.T) {
		svc := new(mocks.LeadService)
		svc.On("ExportForActor", admin, mock.Anything).Return(nil, errors.New("database unavailable")).Once()
		rec := leadExportGet(leadExportRouter(svc, 1, "admin"), "?sort_by=email")
		leadExportAssertEnvelope(t, rec, http.StatusInternalServerError, utils.ErrCodeInternal)
		svc.AssertExpectations(t)
	})

	t.Run("service_refusal_is_the_json_403_envelope", func(t *testing.T) {
		svc := new(mocks.LeadService)
		svc.On("ExportForActor", admin, mock.Anything).Return(nil, fmt.Errorf("refused: %w", apperrors.ErrForbidden)).Once()
		rec := leadExportGet(leadExportRouter(svc, 1, "admin"), "")
		leadExportAssertEnvelope(t, rec, http.StatusForbidden, utils.ErrCodeForbidden)
	})

	t.Run("other_roles_are_refused_without_calling_the_service", func(t *testing.T) {
		for _, role := range []string{"support", "customer", "", "superuser", "Admin"} {
			svc := new(mocks.LeadService)
			rec := leadExportGet(leadExportRouter(svc, 4, role), "?search=acme")
			leadExportAssertEnvelope(t, rec, http.StatusForbidden, utils.ErrCodeForbidden)
			svc.AssertNotCalled(t, "ExportForActor", mock.Anything, mock.Anything)
		}
	})

	t.Run("parameters_are_sanitised_before_the_service", func(t *testing.T) {
		cases := []struct {
			query string
			want  service.LeadExportParams
		}{
			{"", service.LeadExportParams{SortOrder: "asc"}},
			{"?sort_by=password", service.LeadExportParams{SortOrder: "asc"}},
			{"?sort_by=notes%3BDROP+TABLE+leads&sort_order=desc", service.LeadExportParams{SortOrder: "desc"}},
			{"?sort_by=email&sort_order=sideways", service.LeadExportParams{SortBy: "email", SortOrder: "asc"}},
			{"?sort_by=classification&sort_order=desc", service.LeadExportParams{SortBy: "classification", SortOrder: "desc"}},
			{"?search=acme&classification=spam&sort_by=company&sort_order=DESC",
				service.LeadExportParams{Search: "acme", Classification: "spam", SortBy: "company", SortOrder: "asc"}},
		}
		for _, tc := range cases {
			svc := new(mocks.LeadService)
			svc.On("ExportForActor", admin, tc.want).Return([]models.Lead{}, nil).Once()
			rec := leadExportGet(leadExportRouter(svc, 1, "admin"), tc.query)
			assert.Equal(t, http.StatusOK, rec.Code, tc.query)
			svc.AssertExpectations(t)
		}
	})

	t.Run("external_id_and_paging_are_not_read", func(t *testing.T) {
		for _, base := range []string{"", "search=acme&sort_by=email&sort_order=desc"} {
			extra := "external_id=crm-1&offset=40&limit=5&page=3"
			withExtra := "?" + extra
			if base != "" {
				withExtra = "?" + base + "&" + extra
			}
			plain := ""
			if base != "" {
				plain = "?" + base
			}

			var calls [][]interface{}
			svc := new(mocks.LeadService)
			svc.On("ExportForActor", mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) { calls = append(calls, []interface{}{args.Get(0), args.Get(1)}) }).
				Return([]models.Lead{}, nil)
			router := leadExportRouter(svc, 1, "admin")
			require.Equal(t, http.StatusOK, leadExportGet(router, plain).Code)
			require.Equal(t, http.StatusOK, leadExportGet(router, withExtra).Code)
			require.Len(t, calls, 2)
			assert.Equal(t, calls[0], calls[1], "external_id, offset, limit and page must not change the service call")
		}
	})

	t.Run("sales_reaches_the_service_as_itself", func(t *testing.T) {
		svc := new(mocks.LeadService)
		sales := service.LeadViewer{UserID: 7, Role: models.RoleSales}
		svc.On("ExportForActor", sales, service.LeadExportParams{Search: "acme", SortOrder: "asc"}).Return([]models.Lead{}, nil).Once()
		rec := leadExportGet(leadExportRouter(svc, 7, "sales"), "?search=acme")
		assert.Equal(t, http.StatusOK, rec.Code)
		svc.AssertExpectations(t)
	})

	t.Run("success_is_a_csv_attachment", func(t *testing.T) {
		svc := new(mocks.LeadService)
		customerID := uint(31)
		rows := []models.Lead{
			{BaseModel: models.BaseModel{ID: 5, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
				FirstName: "Ada, Jr", LastName: `O"Brien`, Email: "ada@example.test", Notes: "line one\nline two", OwnerID: 2},
			{BaseModel: models.BaseModel{ID: 6}, FirstName: "=HYPERLINK()", Status: models.LeadStatusConverted, CustomerID: &customerID, OwnerID: 2},
		}
		svc.On("ExportForActor", admin, mock.Anything).Return(rows, nil).Once()
		rec := leadExportGet(leadExportRouter(svc, 1, "admin"), "")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))
		assert.Equal(t, "attachment; filename=leads-export.csv", rec.Header().Get("Content-Disposition"))
		records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
		require.NoError(t, err)
		require.Len(t, records, 3)
		assert.Equal(t, leadExportColumns, records[0])
		assert.Equal(t, leadExportRecord(&rows[0]), records[1])
		assert.Equal(t, "Ada, Jr", records[1][1])
		assert.Equal(t, `O"Brien`, records[1][2])
		assert.Equal(t, "line one\nline two", records[1][11])
		assert.Equal(t, "'=HYPERLINK()", records[2][1])
		assert.Equal(t, "31", records[2][13])
	})
}

func TestLeadExportRecord(t *testing.T) {
	assert.Equal(t, []string{
		"id", "first_name", "last_name", "email", "phone", "company", "position", "source", "status",
		"classification", "external_id", "notes", "owner_id", "customer_id", "company_id", "created_at", "updated_at",
	}, leadExportColumns)

	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		paris = time.FixedZone("UTC+2", 2*60*60)
	}
	customerID, companyID := uint(31), uint(12)

	t.Run("timestamps_are_utc_and_ids_render", func(t *testing.T) {
		lead := models.Lead{
			BaseModel: models.BaseModel{
				ID:        42,
				CreatedAt: time.Date(2026, time.July, 1, 10, 30, 0, 0, paris),
				UpdatedAt: time.Date(2026, time.July, 2, 0, 15, 0, 0, time.FixedZone("UTC-5", -5*60*60)),
			},
			FirstName:      "Ada",
			LastName:       "Lovelace",
			Email:          "ada@example.test",
			Phone:          "+44 20 7946 0000",
			Company:        "Engines",
			Position:       "CTO",
			Source:         "website",
			Status:         models.LeadStatusConverted,
			Classification: models.LeadClassificationHotLead,
			ExternalID:     "crm-1",
			Notes:          "hello",
			OwnerID:        7,
			CustomerID:     &customerID,
			CompanyID:      &companyID,
		}
		record := leadExportRecord(&lead)
		require.Len(t, record, len(leadExportColumns))
		assert.Equal(t, []string{
			"42", "Ada", "Lovelace", "ada@example.test", "+44 20 7946 0000", "Engines", "CTO", "website",
			"converted", "hot_lead", "crm-1", "hello", "7", "31", "12",
			lead.CreatedAt.UTC().Format(time.RFC3339), "2026-07-02T05:15:00Z",
		}, record)
		assert.True(t, strings.HasSuffix(record[15], "Z"), "created_at must be rendered in UTC: %s", record[15])
		assert.True(t, strings.HasSuffix(record[16], "Z"), "updated_at must be rendered in UTC: %s", record[16])
		parsed, err := time.Parse(time.RFC3339, record[15])
		require.NoError(t, err)
		assert.True(t, parsed.Equal(lead.CreatedAt))
	})

	t.Run("nil_customer_and_company_are_empty_cells", func(t *testing.T) {
		record := leadExportRecord(&models.Lead{BaseModel: models.BaseModel{ID: 1}, OwnerID: 2})
		assert.Equal(t, "", record[13])
		assert.Equal(t, "", record[14])
		assert.Equal(t, "2", record[12])
		assert.Equal(t, "0001-01-01T00:00:00Z", record[15])
	})

	t.Run("free_text_cells_go_through_csvSafeField", func(t *testing.T) {
		values := []string{
			"=1+1", "@cmd", "\tTabbed", "\rReturn", "+sum(a1)", "-not a number", "+44 20 7946 0000", "-12.5",
			"plain", "", "a, b", `quote "q"`, "multi\nline",
		}
		freeText := map[int]func(l *models.Lead, v string){
			1:  func(l *models.Lead, v string) { l.FirstName = v },
			2:  func(l *models.Lead, v string) { l.LastName = v },
			3:  func(l *models.Lead, v string) { l.Email = v },
			4:  func(l *models.Lead, v string) { l.Phone = v },
			5:  func(l *models.Lead, v string) { l.Company = v },
			6:  func(l *models.Lead, v string) { l.Position = v },
			7:  func(l *models.Lead, v string) { l.Source = v },
			10: func(l *models.Lead, v string) { l.ExternalID = v },
			11: func(l *models.Lead, v string) { l.Notes = v },
		}
		for column, set := range freeText {
			for _, value := range values {
				var lead models.Lead
				set(&lead, value)
				assert.Equal(t, csvSafeField(value), leadExportRecord(&lead)[column],
					"%s = %q", leadExportColumns[column], value)
			}
		}
		// Spot checks that the guard really fires through the record.
		lead := models.Lead{FirstName: "=1+1", Phone: "+44 20 7946 0000", Notes: "-12.5", ExternalID: "@x"}
		record := leadExportRecord(&lead)
		assert.Equal(t, "'=1+1", record[1])
		assert.Equal(t, "+44 20 7946 0000", record[4])
		assert.Equal(t, "-12.5", record[11])
		assert.Equal(t, "'@x", record[10])
	})
}

// TestLeadExportLogsOmitPersonalData swaps the shared logger for a captured
// one at Debug level, restores it, and checks that a successful and a failed
// export each log a handler line with only the allowed fields.
func TestLeadExportLogsOmitPersonalData(t *testing.T) {
	const (
		search = "search-term-canary"
		first  = "first-name-canary"
		last   = "last-name-canary"
		email  = "email-canary@example.test"
		phone  = "+44 0000 phone-canary"
		notes  = "notes-canary text"
	)
	canaries := []string{search, first, last, email, phone, "notes-canary"}
	allowed := map[string]bool{
		"operation": true, "role": true, "user_id": true, "count": true,
		"has_search": true, "has_classification": true, "has_sort": true,
		"level": true, "msg": true, "time": true, "error": true,
	}
	lead := models.Lead{BaseModel: models.BaseModel{ID: 9}, FirstName: first, LastName: last, Email: email, Phone: phone, Notes: notes, OwnerID: 3}

	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprintf("success=%t", success), func(t *testing.T) {
			previous := utils.Logger
			defer func() { utils.Logger = previous }()
			var output bytes.Buffer
			utils.Logger = logrus.New()
			utils.Logger.SetLevel(logrus.DebugLevel)
			utils.Logger.SetFormatter(&logrus.JSONFormatter{})
			utils.Logger.SetOutput(&output)

			svc := new(mocks.LeadService)
			if success {
				svc.On("ExportForActor", mock.Anything, mock.Anything).Return([]models.Lead{lead}, nil).Once()
			} else {
				svc.On("ExportForActor", mock.Anything, mock.Anything).Return(nil, fmt.Errorf("export failed for %q", search)).Once()
			}
			rec := leadExportGet(leadExportRouter(svc, 3, "sales"), "?search="+search+"&sort_by=email")
			if success {
				require.Equal(t, http.StatusOK, rec.Code)
				assert.Contains(t, rec.Body.String(), email, "the file itself carries the data")
			} else {
				require.Equal(t, http.StatusInternalServerError, rec.Code)
			}

			handlerLines := 0
			scanner := bufio.NewScanner(bytes.NewReader(output.Bytes()))
			for scanner.Scan() {
				var line map[string]interface{}
				require.NoError(t, json.Unmarshal(scanner.Bytes(), &line), scanner.Text())
				if line["operation"] != "LeadHandler.Export" {
					continue
				}
				handlerLines++
				assert.Equal(t, "sales", line["role"])
				assert.Equal(t, float64(3), line["user_id"])
				assert.Equal(t, true, line["has_search"])
				for key := range line {
					assert.True(t, allowed[key], "unexpected log field %q", key)
				}
				if success {
					assert.Equal(t, float64(1), line["count"])
				}
			}
			assert.GreaterOrEqual(t, handlerLines, 1, "the export must log at least one handler line")
			raw := output.String()
			for _, canary := range canaries {
				assert.NotContains(t, raw, canary)
			}
			assert.NotContains(t, raw, strconv.Quote("email"), "the raw sort column must not be logged")
		})
	}
}
