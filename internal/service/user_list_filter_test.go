package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// TestUserServiceListFiltered pins #428's service method: the rows come from
// ListByFilter, the total from CountByFilter with the identical filter, and a
// repository error from either call is returned with nil rows and zero total.
func TestUserServiceListFiltered(t *testing.T) {
	filter := repository.UserListFilter{Search: "tok", Role: "sales"}

	t.Run("rows and the count of the identical filter", func(t *testing.T) {
		repo := new(mocks.UserRepository)
		repo.Test(t)
		rows := []models.User{{Email: "a@example.com"}, {Email: "b@example.com"}}
		repo.On("ListByFilter", filter, 10, 5, "email", "desc").Return(rows, nil).Once()
		repo.On("CountByFilter", filter).Return(int64(12), nil).Once()

		users, total, err := NewUserService(repo).ListFiltered(filter, 10, 5, "email", "desc")
		require.NoError(t, err)
		assert.Equal(t, rows, users)
		assert.Equal(t, int64(12), total)
		repo.AssertExpectations(t)
	})

	t.Run("a list error is returned and nothing is counted", func(t *testing.T) {
		repo := new(mocks.UserRepository)
		repo.Test(t)
		boom := errors.New("list failed")
		repo.On("ListByFilter", filter, 0, 20, "", "asc").Return(nil, boom).Once()

		users, total, err := NewUserService(repo).ListFiltered(filter, 0, 20, "", "asc")
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, users)
		assert.Zero(t, total)
		repo.AssertNotCalled(t, "CountByFilter", filter)
		repo.AssertExpectations(t)
	})

	t.Run("a count error is returned", func(t *testing.T) {
		repo := new(mocks.UserRepository)
		repo.Test(t)
		boom := errors.New("count failed")
		repo.On("ListByFilter", filter, 0, 20, "", "asc").Return([]models.User{{Email: "a@example.com"}}, nil).Once()
		repo.On("CountByFilter", filter).Return(int64(0), boom).Once()

		users, total, err := NewUserService(repo).ListFiltered(filter, 0, 20, "", "asc")
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, users)
		assert.Zero(t, total)
		repo.AssertExpectations(t)
	})
}
