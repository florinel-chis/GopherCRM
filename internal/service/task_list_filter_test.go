package service

import (
	"errors"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #427: ListFiltered forwards the filter, the page and the sort to
// ListByFilter with the task listing preloads, counts with the same filter and
// returns both, or the error of either call.
func TestTaskServiceListFiltered(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))

	assignee := uint(7)
	filter := repository.TaskListFilter{
		AssigneeID: &assignee,
		LabelID:    3,
		Search:     "tok",
		Status:     string(models.TaskStatusCompleted),
		Priority:   string(models.TaskPriorityHigh),
	}
	newService := func(t *testing.T) (*mocks.TaskRepository, TaskService) {
		repo := new(mocks.TaskRepository)
		repo.Test(t)
		t.Cleanup(func() { repo.AssertExpectations(t) })
		return repo, NewTaskService(repo, nil, nil, nil, nil, nil)
	}

	t.Run("forwards the filter, page, sort and preloads and returns the count", func(t *testing.T) {
		repo, svc := newService(t)
		rows := []models.Task{{BaseModel: models.BaseModel{ID: 11}}, {BaseModel: models.BaseModel{ID: 12}}}
		repo.On("ListByFilter", filter, 40, 20, "title", "desc", taskListPreloads).Return(rows, nil).Once()
		repo.On("CountByFilter", filter).Return(int64(57), nil).Once()

		tasks, total, err := svc.ListFiltered(filter, 40, 20, "title", "desc")
		require.NoError(t, err)
		assert.Equal(t, rows, tasks)
		assert.Equal(t, int64(57), total)
	})

	t.Run("a list error is returned without counting", func(t *testing.T) {
		repo, svc := newService(t)
		listErr := errors.New("list failed")
		repo.On("ListByFilter", filter, 0, 20, "", "", taskListPreloads).Return(nil, listErr).Once()

		tasks, total, err := svc.ListFiltered(filter, 0, 20, "", "")
		require.ErrorIs(t, err, listErr)
		assert.Nil(t, tasks)
		assert.Zero(t, total)
		repo.AssertNotCalled(t, "CountByFilter", filter)
	})

	t.Run("a count error is returned", func(t *testing.T) {
		repo, svc := newService(t)
		countErr := errors.New("count failed")
		repo.On("ListByFilter", filter, 0, 20, "", "", taskListPreloads).Return([]models.Task{{BaseModel: models.BaseModel{ID: 1}}}, nil).Once()
		repo.On("CountByFilter", filter).Return(int64(0), countErr).Once()

		tasks, total, err := svc.ListFiltered(filter, 0, 20, "", "")
		require.ErrorIs(t, err, countErr)
		assert.Nil(t, tasks)
		assert.Zero(t, total)
	})
}
