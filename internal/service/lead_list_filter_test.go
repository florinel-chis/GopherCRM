package service

import (
	"bytes"
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// captureLeadFilterLogs swaps utils.Logger for a debug-level JSON logger
// writing into a buffer, restored when the test ends.
func captureLeadFilterLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	old := utils.Logger
	logger := logrus.New()
	var output bytes.Buffer
	logger.SetOutput(&output)
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.DebugLevel)
	utils.Logger = logger
	t.Cleanup(func() { utils.Logger = old })
	return &output
}

// TestLeadServiceListFiltered pins #426's service method: the rows come from
// ListByFilter with the Owner preload, the total from CountByFilter with the
// identical filter, a repository error from either call is returned with nil
// rows and a zero total, and nothing it logs carries the search term.
func TestLeadServiceListFiltered(t *testing.T) {
	const search = "zz-secret-lead-search-canary"
	owner := uint(42)
	preloads := []string{"Owner"}
	filters := []repository.LeadListFilter{
		{Status: models.LeadStatusQualified},
		{Status: models.LeadStatusNew, Search: search, Classification: models.LeadClassificationLead},
		{Status: models.LeadStatusContacted, OwnerID: &owner},
	}

	t.Run("rows and the count of the identical filter", func(t *testing.T) {
		for _, filter := range filters {
			repo := new(mocks.LeadRepository)
			repo.Test(t)
			rows := []models.Lead{{FirstName: "one"}, {FirstName: "two"}}
			repo.On("ListByFilter", filter, 30, 15, "email", "desc", preloads).Return(rows, nil).Once()
			repo.On("CountByFilter", filter).Return(int64(47), nil).Once()

			got, total, err := NewLeadService(repo, nil, nil, nil, nil).ListFiltered(filter, 30, 15, "email", "desc")
			require.NoError(t, err)
			assert.Equal(t, rows, got)
			assert.Equal(t, int64(47), total)
			repo.AssertExpectations(t)
		}
	})

	t.Run("the search term is not logged", func(t *testing.T) {
		output := captureLeadFilterLogs(t)
		filter := repository.LeadListFilter{Status: models.LeadStatusQualified, Search: search, OwnerID: &owner}
		repo := new(mocks.LeadRepository)
		repo.Test(t)
		repo.On("ListByFilter", filter, 0, 20, "", "asc", preloads).Return([]models.Lead{}, nil).Once()
		repo.On("CountByFilter", filter).Return(int64(3), nil).Once()

		_, _, err := NewLeadService(repo, nil, nil, nil, nil).ListFiltered(filter, 0, 20, "", "asc")
		require.NoError(t, err)
		repo.AssertExpectations(t)
		logs := output.String()
		assert.Contains(t, logs, `"method":"ListFiltered"`)
		assert.Contains(t, logs, `"search_filter":true`)
		assert.Contains(t, logs, `"status_filter":true`)
		assert.Contains(t, logs, `"owner_id":42`)
		assert.Contains(t, logs, `"total":3`)
		assert.NotContains(t, logs, search)
		assert.NotContains(t, logs, string(models.LeadStatusQualified), "the status is logged as a presence flag only")
	})

	t.Run("a list error is returned", func(t *testing.T) {
		captureLeadFilterLogs(t)
		boom := errors.New("list failed")
		repo := new(mocks.LeadRepository)
		repo.Test(t)
		repo.On("ListByFilter", filters[0], 0, 20, "", "asc", preloads).Return(nil, boom).Once()

		got, total, err := NewLeadService(repo, nil, nil, nil, nil).ListFiltered(filters[0], 0, 20, "", "asc")
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
		assert.Zero(t, total)
		repo.AssertExpectations(t)
		repo.AssertNotCalled(t, "CountByFilter", filters[0])
	})

	t.Run("a count error is returned", func(t *testing.T) {
		captureLeadFilterLogs(t)
		boom := errors.New("count failed")
		repo := new(mocks.LeadRepository)
		repo.Test(t)
		repo.On("ListByFilter", filters[0], 0, 20, "", "asc", preloads).Return([]models.Lead{{FirstName: "x"}}, nil).Once()
		repo.On("CountByFilter", filters[0]).Return(int64(0), boom).Once()

		got, total, err := NewLeadService(repo, nil, nil, nil, nil).ListFiltered(filters[0], 0, 20, "", "asc")
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
		assert.Zero(t, total)
		repo.AssertExpectations(t)
	})
}
