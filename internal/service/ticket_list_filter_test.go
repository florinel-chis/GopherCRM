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

// captureTicketFilterLogs swaps utils.Logger for a debug-level JSON logger
// writing into a buffer, restored when the test ends.
func captureTicketFilterLogs(t *testing.T) *bytes.Buffer {
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

// TestTicketServiceListFiltered pins #218's service method: the rows come from
// ListByFilter with the Customer and AssignedTo preloads, the total from
// CountByFilter with the identical filter, a repository error from either call
// is returned with nil rows and a zero total, and nothing it logs carries the
// search term or a raw sort value.
func TestTicketServiceListFiltered(t *testing.T) {
	const search = "zz-secret-search-canary"
	// A sort column whose name appears in no log field or message.
	const sortCanary = "title"
	assignee := uint(42)
	filters := []repository.TicketListFilter{
		{Status: "open"},
		{Priority: "urgent"},
		{Status: "resolved", Priority: "low", Search: search},
		{Status: "in_progress", Search: search, AssigneeID: &assignee},
		{Priority: "high", AssigneeID: new(uint)},
	}
	preloads := []string{"Customer", "AssignedTo"}

	t.Run("rows and the count of the identical filter", func(t *testing.T) {
		for _, filter := range filters {
			output := captureTicketFilterLogs(t)
			repo := new(mocks.TicketRepository)
			repo.Test(t)
			rows := []models.Ticket{{Title: "one"}, {Title: "two"}}
			repo.On("ListByFilter", filter, 30, 15, sortCanary, "desc", preloads).Return(rows, nil).Once()
			repo.On("CountByFilter", filter).Return(int64(47), nil).Once()

			got, total, err := NewTicketService(repo, nil, nil).ListFiltered(filter, 30, 15, sortCanary, "desc")
			require.NoError(t, err)
			assert.Equal(t, rows, got)
			assert.Equal(t, int64(47), total)
			repo.AssertExpectations(t)
			assert.NotContains(t, output.String(), search)
			assert.NotContains(t, output.String(), sortCanary, "raw sort values are not logged")
			assert.Contains(t, output.String(), `"method":"ListFiltered"`)
			assert.Contains(t, output.String(), `"total":47`)
		}
	})

	t.Run("an empty filter is passed through", func(t *testing.T) {
		captureTicketFilterLogs(t)
		repo := new(mocks.TicketRepository)
		repo.Test(t)
		repo.On("ListByFilter", repository.TicketListFilter{}, 0, 20, "", "asc", preloads).Return([]models.Ticket{}, nil).Once()
		repo.On("CountByFilter", repository.TicketListFilter{}).Return(int64(0), nil).Once()
		got, total, err := NewTicketService(repo, nil, nil).ListFiltered(repository.TicketListFilter{}, 0, 20, "", "asc")
		require.NoError(t, err)
		assert.Empty(t, got)
		assert.Equal(t, int64(0), total)
		repo.AssertExpectations(t)
	})

	t.Run("a list error is returned with nil rows and zero total, and no count runs", func(t *testing.T) {
		listErr := errors.New("list failed")
		for _, filter := range filters {
			output := captureTicketFilterLogs(t)
			repo := new(mocks.TicketRepository)
			repo.Test(t)
			repo.On("ListByFilter", filter, 0, 20, "", "asc", preloads).Return(nil, listErr).Once()

			got, total, err := NewTicketService(repo, nil, nil).ListFiltered(filter, 0, 20, "", "asc")
			assert.ErrorIs(t, err, listErr)
			assert.Nil(t, got)
			assert.Equal(t, int64(0), total)
			repo.AssertExpectations(t)
			repo.AssertNotCalled(t, "CountByFilter", filter)
			assert.NotContains(t, output.String(), search)
			assert.Contains(t, output.String(), "Failed to list filtered tickets")
		}
	})

	t.Run("a count error is returned with nil rows and zero total", func(t *testing.T) {
		countErr := errors.New("count failed")
		for _, filter := range filters {
			output := captureTicketFilterLogs(t)
			repo := new(mocks.TicketRepository)
			repo.Test(t)
			repo.On("ListByFilter", filter, 5, 5, "title", "asc", preloads).Return([]models.Ticket{{Title: "x"}}, nil).Once()
			repo.On("CountByFilter", filter).Return(int64(0), countErr).Once()

			got, total, err := NewTicketService(repo, nil, nil).ListFiltered(filter, 5, 5, "title", "asc")
			assert.ErrorIs(t, err, countErr)
			assert.Nil(t, got)
			assert.Equal(t, int64(0), total)
			repo.AssertExpectations(t)
			assert.NotContains(t, output.String(), search)
			assert.Contains(t, output.String(), "Failed to count filtered tickets")
		}
	})
}
