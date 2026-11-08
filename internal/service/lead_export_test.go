package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// newLeadExportService builds a lead service whose only collaborator is the
// mocked lead repository: the export reads nothing else.
func newLeadExportService(repo *mocks.LeadRepository) LeadService {
	return NewLeadService(repo, nil, nil, nil, nil)
}

func leadExportUint(v uint) *uint { return &v }

// TestLeadServiceExportScope pins which filter each role's request reaches the
// repository with (#234): the lead list's precedence for an admin, the
// caller's own leads in id order for a sales user, whatever either sent.
func TestLeadServiceExportScope(t *testing.T) {
	admin := LeadViewer{UserID: 1, Role: models.RoleAdmin}
	sales := LeadViewer{UserID: 7, Role: models.RoleSales}
	all := LeadExportParams{Search: "acme", Classification: "spam", SortBy: "email", SortOrder: "desc"}

	cases := []struct {
		name   string
		actor  LeadViewer
		params LeadExportParams
		want   repository.LeadExportFilter
	}{
		{"admin_search_wins_and_keeps_the_sort", admin, all,
			repository.LeadExportFilter{Search: "acme", SortBy: "email", SortOrder: "desc"}},
		{"admin_search_without_sort", admin, LeadExportParams{Search: "acme", Classification: "spam", SortOrder: "desc"},
			repository.LeadExportFilter{Search: "acme"}},
		{"admin_classification_clears_the_sort", admin, LeadExportParams{Classification: "spam", SortBy: "email", SortOrder: "desc"},
			repository.LeadExportFilter{Classification: models.LeadClassificationSpam}},
		{"admin_classification_any_value_is_passed_exactly", admin, LeadExportParams{Classification: "nonsense"},
			repository.LeadExportFilter{Classification: "nonsense"}},
		{"admin_sort", admin, LeadExportParams{SortBy: "company", SortOrder: "asc"},
			repository.LeadExportFilter{SortBy: "company", SortOrder: "asc"}},
		{"admin_nothing_is_id_ascending", admin, LeadExportParams{},
			repository.LeadExportFilter{}},
		{"admin_direction_without_column_is_id_ascending", admin, LeadExportParams{SortOrder: "desc"},
			repository.LeadExportFilter{}},
		{"sales_every_parameter_ignored", sales, all,
			repository.LeadExportFilter{OwnerID: leadExportUint(7)}},
		{"sales_sort_ignored", sales, LeadExportParams{SortBy: "email", SortOrder: "desc"},
			repository.LeadExportFilter{OwnerID: leadExportUint(7)}},
		{"sales_nothing", sales, LeadExportParams{},
			repository.LeadExportFilter{OwnerID: leadExportUint(7)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := new(mocks.LeadRepository)
			rows := []models.Lead{{BaseModel: models.BaseModel{ID: 3}, OwnerID: tc.actor.UserID}}
			repo.On("ListAllForExport", tc.want).Return(rows, nil).Once()

			got, err := newLeadExportService(repo).ExportForActor(tc.actor, tc.params)
			require.NoError(t, err)
			assert.Equal(t, rows, got)
			repo.AssertExpectations(t)
			repo.AssertNumberOfCalls(t, "ListAllForExport", 1)
		})
	}

	t.Run("a_repository_failure_is_returned", func(t *testing.T) {
		repo := new(mocks.LeadRepository)
		failure := errors.New("database unavailable")
		repo.On("ListAllForExport", repository.LeadExportFilter{}).Return(nil, failure).Once()
		got, err := newLeadExportService(repo).ExportForActor(admin, LeadExportParams{})
		require.ErrorIs(t, err, failure)
		assert.Nil(t, got)
	})
}

// TestLeadServiceExportRefusesOtherRoles: support, customer and any unknown
// role are refused with apperrors.ErrForbidden before the repository is called.
func TestLeadServiceExportRefusesOtherRoles(t *testing.T) {
	actors := []LeadViewer{
		{UserID: 3, Role: models.RoleSupport},
		{UserID: 4, Role: models.RoleCustomer},
		{UserID: 5, Role: ""},
		{UserID: 6, Role: "superuser"},
		{UserID: 7, Role: "Admin"},
		{UserID: 0, Role: models.RoleSales}, // a sales actor without an id owns nothing
	}
	for _, actor := range actors {
		t.Run(fmt.Sprintf("%s_%d", actor.Role, actor.UserID), func(t *testing.T) {
			repo := new(mocks.LeadRepository)
			got, err := newLeadExportService(repo).ExportForActor(actor,
				LeadExportParams{Search: "acme", Classification: "spam", SortBy: "email", SortOrder: "asc"})
			require.Error(t, err)
			assert.True(t, errors.Is(err, apperrors.ErrForbidden), "got %v", err)
			assert.Nil(t, got)
			repo.AssertNotCalled(t, "ListAllForExport", mock.Anything)
		})
	}
}

// leadExportCaptureLogs swaps the shared logger for a JSON logger at Debug
// level (overriding the package's Warn init) and restores it afterwards.
func leadExportCaptureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous := utils.Logger
	t.Cleanup(func() { utils.Logger = previous })
	var output bytes.Buffer
	utils.Logger = logrus.New()
	utils.Logger.SetLevel(logrus.DebugLevel)
	utils.Logger.SetFormatter(&logrus.JSONFormatter{})
	utils.Logger.SetOutput(&output)
	return &output
}

// leadExportLogLines parses every captured JSON line.
func leadExportLogLines(t *testing.T, output *bytes.Buffer) []map[string]interface{} {
	t.Helper()
	var lines []map[string]interface{}
	scanner := bufio.NewScanner(bytes.NewReader(output.Bytes()))
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var line map[string]interface{}
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &line), scanner.Text())
		lines = append(lines, line)
	}
	return lines
}

// TestLeadServiceExportLogsOmitPersonalData: the export's log lines carry the
// operation, the role, the user id, the count and presence flags only.
func TestLeadServiceExportLogsOmitPersonalData(t *testing.T) {
	const (
		search = "search-term-canary"
		first  = "first-name-canary"
		last   = "last-name-canary"
		email  = "email-canary@example.test"
		phone  = "+44 0000 phone-canary"
		notes  = "notes-canary text"
		sortBy = "company"
	)
	canaries := []string{search, first, last, email, phone, notes, "notes-canary"}
	allowed := map[string]bool{
		"operation": true, "role": true, "user_id": true, "count": true,
		"has_search": true, "has_classification": true, "has_sort": true,
		// logrus's own keys, and the redacted error of a failure
		"level": true, "msg": true, "time": true, "error": true,
	}
	lead := models.Lead{BaseModel: models.BaseModel{ID: 9}, FirstName: first, LastName: last, Email: email, Phone: phone, Notes: notes}

	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprintf("success=%t", success), func(t *testing.T) {
			output := leadExportCaptureLogs(t)
			repo := new(mocks.LeadRepository)
			want := repository.LeadExportFilter{Search: search, SortBy: sortBy, SortOrder: "asc"}
			if success {
				repo.On("ListAllForExport", want).Return([]models.Lead{lead}, nil).Once()
			} else {
				// A driver error that quotes the term: LogError cuts it at the quote.
				repo.On("ListAllForExport", want).Return(nil, fmt.Errorf("query failed for %q", search)).Once()
			}

			_, err := newLeadExportService(repo).ExportForActor(LeadViewer{UserID: 1, Role: models.RoleAdmin},
				LeadExportParams{Search: search, SortBy: sortBy, SortOrder: "asc"})
			assert.Equal(t, !success, err != nil)

			lines := leadExportLogLines(t, output)
			exportLines := 0
			for _, line := range lines {
				if line["operation"] == "LeadService.ExportForActor" {
					exportLines++
					assert.Equal(t, "admin", line["role"])
					assert.Equal(t, float64(1), line["user_id"])
					assert.Equal(t, true, line["has_search"])
					assert.Equal(t, true, line["has_sort"])
					for key := range line {
						assert.True(t, allowed[key], "unexpected log field %q", key)
					}
				}
			}
			assert.GreaterOrEqual(t, exportLines, 1, "the export must log at least one line")
			if success {
				assert.Contains(t, output.String(), `"count":1`)
			}
			raw := output.String()
			for _, canary := range canaries {
				assert.NotContains(t, raw, canary)
			}
			assert.NotContains(t, raw, `"`+sortBy+`"`, "the raw sort column must not be logged")
		})
	}
}
